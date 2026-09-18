package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

type fakeEmailStore struct {
	mu sync.Mutex
	m  map[string]*domain.EmailVerification
}

func (f *fakeEmailStore) Save(_ context.Context, v *domain.EmailVerification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *v
	f.m[v.UserID] = &cp
	return nil
}
func (f *fakeEmailStore) Get(_ context.Context, id string) (*domain.EmailVerification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeEmailStore) IncrementAttempts(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id].Attempts++
	return f.m[id].Attempts, nil
}
func (f *fakeEmailStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

type fakeEmailSender struct {
	mu   sync.Mutex
	sent []domain.EmailMessage
	err  error
}

func (f *fakeEmailSender) Send(_ context.Context, m domain.EmailMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

type emailFixture struct {
	users  *fakeUsers
	store  *fakeEmailStore
	sender *fakeEmailSender
	codes  *fakeCodes
	clock  *fakeClock
	svc    *EmailVerificationService
}

func newEmailFixture() *emailFixture {
	f := &emailFixture{
		users: newFakeUsers(), store: &fakeEmailStore{m: map[string]*domain.EmailVerification{}},
		sender: &fakeEmailSender{}, codes: &fakeCodes{next: "654321"},
		clock: &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)},
	}
	f.svc = NewEmailVerificationService(EmailVerificationDeps{
		Users: f.users, Store: f.store, Codes: f.codes, Sender: f.sender, Clock: f.clock, Log: discardLogger(),
	}, domain.DefaultEmailVerificationPolicy())
	return f
}

func TestEmailVerification_FluxoCompleto(t *testing.T) {
	ctx := context.Background()
	f := newEmailFixture()
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana Souza", Email: "ana@ex.com", Telefone: "5511987654321"})

	out, err := f.svc.Start(ctx, "user-1")
	if err != nil || out.Email != "ana@ex.com" {
		t.Fatalf("start: %+v, %v", out, err)
	}
	if len(f.sender.sent) != 1 {
		t.Fatalf("e-mails enviados = %d", len(f.sender.sent))
	}
	m := f.sender.sent[0]
	if m.To != "ana@ex.com" || !strings.Contains(m.Text, "654321") || !strings.Contains(m.HTML, "654321") || !strings.Contains(m.Text, "Olá, Ana!") {
		t.Fatalf("mensagem inesperada: %+v", m)
	}
	// Código não fica em claro no store.
	if v, _ := f.store.Get(ctx, "user-1"); strings.Contains(v.CodeHash, "654321") {
		t.Fatal("código gravado em claro")
	}

	// Reenvio antes do cooldown -> 429.
	if _, err := f.svc.Start(ctx, "user-1"); err == nil {
		t.Fatal("esperava cooldown")
	} else if _, ok := domain.IsRetryAfter(err); !ok {
		t.Fatalf("esperava RetryAfter: %v", err)
	}

	if ok, _ := f.svc.IsVerified(ctx, "user-1"); ok {
		t.Fatal("não deveria estar verificado")
	}
	if err := f.svc.Confirm(ctx, "user-1", "000000"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatalf("código errado: %v", err)
	}
	if err := f.svc.Confirm(ctx, "user-1", "abc"); err == nil {
		t.Fatal("formato inválido deveria falhar")
	}
	if err := f.svc.Confirm(ctx, "user-1", "654321"); err != nil {
		t.Fatalf("confirmar: %v", err)
	}
	if ok, _ := f.svc.IsVerified(ctx, "user-1"); !ok {
		t.Fatal("deveria estar verificado")
	}
	if _, err := f.svc.Start(ctx, "user-1"); !errors.Is(err, domain.ErrEmailAlreadyVerified) {
		t.Fatalf("já verificado: %v", err)
	}
}

func TestEmailVerification_CodigoDeTelefoneNaoValeParaEmail(t *testing.T) {
	ctx := context.Background()
	f := newEmailFixture()
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})
	// Desafio gravado com o hash no escopo do TELEFONE (sem prefixo "email:").
	_ = f.store.Save(ctx, &domain.EmailVerification{UserID: "user-1", Email: "ana@ex.com",
		CodeHash: f.codes.Hash("user-1", "654321"), ExpiresAt: f.clock.Now().Add(time.Hour), CreatedAt: f.clock.Now()})
	if err := f.svc.Confirm(ctx, "user-1", "654321"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatalf("hash de outro escopo foi aceito: %v", err)
	}
}

func TestEmailVerification_ExpiracaoETentativas(t *testing.T) {
	ctx := context.Background()
	f := newEmailFixture()
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})

	if _, err := f.svc.Start(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = f.svc.Confirm(ctx, "user-1", "111111")
	}
	// Após 5 erros o desafio é descartado: nem o código certo vale.
	if err := f.svc.Confirm(ctx, "user-1", "654321"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatalf("após tentativas: %v", err)
	}

	f.clock.now = f.clock.now.Add(2 * time.Minute)
	if _, err := f.svc.Start(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	f.clock.now = f.clock.now.Add(31 * time.Minute)
	if err := f.svc.Confirm(ctx, "user-1", "654321"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatalf("expirado: %v", err)
	}
}

func TestEmailVerification_FalhaNoEnvioPermiteReenviar(t *testing.T) {
	ctx := context.Background()
	f := newEmailFixture()
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})
	f.sender.err = errors.New("smtp fora")
	if _, err := f.svc.Start(ctx, "user-1"); !errors.Is(err, domain.ErrEmailDeliveryFailed) {
		t.Fatalf("esperava falha de envio: %v", err)
	}
	f.sender.err = nil
	if _, err := f.svc.Start(ctx, "user-1"); err != nil {
		t.Fatalf("reenvio imediato após falha deveria funcionar: %v", err)
	}
}

func TestAuthService_RegisterEnviaCodigoPorEmail(t *testing.T) {
	ctx := context.Background()
	f := newEmailFixture()
	auth, err := NewAuthService(AuthDeps{
		Users: f.users, Sessions: newFakeSessions(), Attempts: newFakeAttempts(), Hasher: fakeHasher{},
		Tokens: &fakeTokens{}, Clock: f.clock, Log: discardLogger(), EmailVerification: f.svc,
	}, AuthConfig{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	u, err := auth.Register(ctx, ports.RegisterInput{Nome: "Ana", Email: "ana@ex.com", Telefone: "11987654321", Senha: "segredo123"})
	if err != nil || u.EmailVerificado() {
		t.Fatalf("register: %+v, %v", u, err)
	}
	if len(f.sender.sent) != 1 {
		t.Fatalf("cadastro deveria enviar 1 e-mail, enviou %d", len(f.sender.sent))
	}

	// SMTP fora do ar não impede o cadastro.
	f.sender.err = errors.New("smtp fora")
	if _, err := auth.Register(ctx, ports.RegisterInput{Nome: "Beto", Email: "beto@ex.com", Telefone: "21999998888", Senha: "segredo123"}); err != nil {
		t.Fatalf("cadastro não deveria falhar por causa do e-mail: %v", err)
	}
}

func TestWebhookService_ExigeEmailVerificado(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(false)
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	f.webhook = NewWebhookService(WebhookDeps{
		Users: f.users, Transacoes: f.txs, Parser: NewRegexParserService(), Sender: f.sender,
		Verifier: f.verifier, SkipPhoneVerification: true, RequireVerifiedEmail: true, Clock: f.clock, Location: loc, Log: discardLogger(),
	})
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})
	msg := domain.MensagemRecebida{Provider: "evolution", MessageID: "E1", Telefone: "5511987654321", Texto: "Gastei 10 mercado"}

	if res, _ := f.webhook.ProcessMessage(ctx, msg); res.Status != domain.StatusEmailNaoVerificado || len(f.txs.items) != 0 {
		t.Fatalf("sem e-mail verificado: %s", res.Status)
	}
	f.sender.next(t)

	_ = f.users.MarkEmailVerified(ctx, "user-1", f.clock.Now())
	msg.MessageID = "E2"
	if res, _ := f.webhook.ProcessMessage(ctx, msg); res.Status != domain.StatusCriada {
		t.Fatalf("com e-mail verificado: %s", res.Status)
	}
}
