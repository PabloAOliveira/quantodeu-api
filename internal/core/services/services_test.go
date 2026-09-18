package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// ---------------------------------------------------------------------------
// Fakes em memória das portas secundárias
// ---------------------------------------------------------------------------

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time          { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) Advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

type fakeUsers struct {
	mu   sync.Mutex
	byID map[string]*domain.User
	seq  int
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byID: map[string]*domain.User{}} }

func (f *fakeUsers) Create(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.byID {
		if x.Email == u.Email {
			return domain.ErrEmailAlreadyExists
		}
		if x.Telefone == u.Telefone {
			return domain.ErrPhoneAlreadyExists
		}
	}
	f.seq++
	u.ID = fmt.Sprintf("user-%d", f.seq)
	cp := *u
	f.byID[u.ID] = &cp
	return nil
}
func (f *fakeUsers) get(id string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeUsers) FindByID(_ context.Context, id string) (*domain.User, error) { return f.get(id) }
func (f *fakeUsers) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byID {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeUsers) FindByPhones(_ context.Context, phones []string) (*domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range phones {
		for _, u := range f.byID {
			if u.Telefone == p {
				cp := *u
				return &cp, nil
			}
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeUsers) UpdatePasswordHash(_ context.Context, id, h string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].PasswordHash = h
	return nil
}
func (f *fakeUsers) MarkEmailVerified(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].EmailVerificadoEm = &at
	return nil
}

func (f *fakeUsers) MarkPhoneVerified(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].TelefoneVerificadoEm = &at
	return nil
}

type fakeSessions struct {
	mu sync.Mutex
	m  map[string]*domain.Session
}

func newFakeSessions() *fakeSessions { return &fakeSessions{m: map[string]*domain.Session{}} }

func (f *fakeSessions) Create(_ context.Context, s *domain.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[s.TokenHash] = s
	return nil
}
func (f *fakeSessions) FindByTokenHash(_ context.Context, h string) (*domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.m[h]; ok {
		return s, nil
	}
	return nil, domain.ErrSessionNotFound
}
func (f *fakeSessions) Touch(_ context.Context, h string, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[h].LastSeenAt = t
	return nil
}
func (f *fakeSessions) Delete(_ context.Context, h string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, h)
	return nil
}
func (f *fakeSessions) DeleteAllForUser(_ context.Context, userID, except string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for h, s := range f.m {
		if s.UserID == userID && h != except {
			delete(f.m, h)
			n++
		}
	}
	return n, nil
}
func (f *fakeSessions) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeAttempts struct {
	mu sync.Mutex
	m  map[string]*struct {
		st    domain.LoginAttemptState
		first time.Time
	}
}

func newFakeAttempts() *fakeAttempts {
	return &fakeAttempts{m: map[string]*struct {
		st    domain.LoginAttemptState
		first time.Time
	}{}}
}
func (f *fakeAttempts) Get(_ context.Context, k string) (domain.LoginAttemptState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.m[k]; ok {
		return e.st, nil
	}
	return domain.LoginAttemptState{}, nil
}
func (f *fakeAttempts) RegisterFailure(_ context.Context, k string, now time.Time, window time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.m[k]
	if !ok || now.Sub(e.first) > window {
		e = &struct {
			st    domain.LoginAttemptState
			first time.Time
		}{first: now}
		f.m[k] = e
	}
	e.st.Failures++
	return e.st.Failures, nil
}
func (f *fakeAttempts) Lock(_ context.Context, k string, until time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[k].st.LockedUntil = until
	return nil
}
func (f *fakeAttempts) Reset(_ context.Context, k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, k)
	return nil
}

type fakeVerifications struct {
	mu sync.Mutex
	m  map[string]*domain.PhoneVerification
}

func (f *fakeVerifications) Save(_ context.Context, v *domain.PhoneVerification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *v
	f.m[v.UserID] = &cp
	return nil
}
func (f *fakeVerifications) Get(_ context.Context, id string) (*domain.PhoneVerification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeVerifications) IncrementAttempts(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id].Attempts++
	return f.m[id].Attempts, nil
}
func (f *fakeVerifications) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
	return nil
}

// hasher trivial (o Argon2id real é testado no adaptador).
type fakeHasher struct{}

func (fakeHasher) Hash(p string) (string, error) { return "h:" + p, nil }
func (fakeHasher) Verify(p, h string) (bool, bool, error) {
	return h == "h:"+p, false, nil
}

type fakeTokens struct {
	mu sync.Mutex
	n  int
}

func (f *fakeTokens) Generate() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return fmt.Sprintf("tok%d", f.n), nil
}
func (f *fakeTokens) Hash(t string) string { return "hash-" + t }

type fakeCodes struct{ next string }

func (f *fakeCodes) GenerateNumeric(int) (string, error) { return f.next, nil }
func (f *fakeCodes) Hash(u, c string) string {
	s := sha256.Sum256([]byte(u + ":" + c))
	return hex.EncodeToString(s[:])
}
func (f *fakeCodes) Equal(a, b string) bool { return a == b }

type fakeTransacoes struct {
	mu    sync.Mutex
	items []*domain.Transacao
	seq   int
}

func (f *fakeTransacoes) Create(_ context.Context, userID string, t *domain.Transacao) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.items {
		if t.ExternalID != "" && x.UserID == userID && x.ExternalID == t.ExternalID {
			return domain.ErrDuplicateMessage
		}
	}
	f.seq++
	t.ID, t.UserID = fmt.Sprintf("tx-%d", f.seq), userID
	f.items = append(f.items, t)
	return nil
}
func (f *fakeTransacoes) FindByID(_ context.Context, userID, id string) (*domain.Transacao, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.items {
		if x.ID == id && x.UserID == userID {
			cp := *x
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeTransacoes) Update(_ context.Context, userID string, t *domain.Transacao) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.items {
		if x.ID == t.ID && x.UserID == userID {
			cp := *t
			f.items[i] = &cp
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeTransacoes) Delete(_ context.Context, userID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.items {
		if x.ID == id && x.UserID == userID {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func (f *fakeTransacoes) List(context.Context, string, ports.TransacaoFilter) ([]*domain.Transacao, int64, error) {
	return f.items, int64(len(f.items)), nil
}
func (f *fakeTransacoes) Totais(_ context.Context, userID string, ini, fim time.Time) (domain.TotaisPeriodo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t domain.TotaisPeriodo
	for _, x := range f.items {
		if x.UserID != userID || x.Data.Before(ini) || !x.Data.Before(fim) {
			continue
		}
		if x.Tipo == domain.TipoEntrada {
			t.Entradas += x.Valor
		} else {
			t.Saidas += x.Valor
		}
	}
	return t, nil
}
func (f *fakeTransacoes) SaldoAte(_ context.Context, userID string, ate time.Time) (domain.Money, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s domain.Money
	for _, x := range f.items {
		if x.UserID == userID && x.Data.Before(ate) {
			s += x.ValorAssinado()
		}
	}
	return s, nil
}
func (f *fakeTransacoes) TotaisPorCategoria(context.Context, string, time.Time, time.Time) ([]domain.TotalCategoria, error) {
	return nil, nil
}
func (f *fakeTransacoes) ListParcelas(context.Context, string, time.Time, time.Time) ([]*domain.Transacao, error) {
	return nil, nil
}

// fakeSender entrega as mensagens num canal (o envio do webhook é assíncrono).
type fakeSender struct{ ch chan string }

func newFakeSender() *fakeSender { return &fakeSender{ch: make(chan string, 16)} }

func (f *fakeSender) SendText(_ context.Context, _, texto string) error {
	f.ch <- texto
	return nil
}
func (f *fakeSender) next(t *testing.T) string {
	t.Helper()
	select {
	case m := <-f.ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("nenhuma mensagem enviada")
		return ""
	}
}

type countingMetrics struct {
	NoopMetrics
	mu     sync.Mutex
	logins map[string]int
}

func (m *countingMetrics) LoginTentativa(r string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.logins == nil {
		m.logins = map[string]int{}
	}
	m.logins[r]++
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newAuth(t *testing.T, clk *fakeClock) (*AuthService, *fakeUsers, *fakeSessions, *countingMetrics) {
	t.Helper()
	users, sessions, metrics := newFakeUsers(), newFakeSessions(), &countingMetrics{}
	svc, err := NewAuthService(AuthDeps{
		Users: users, Sessions: sessions, Attempts: newFakeAttempts(), Hasher: fakeHasher{},
		Tokens: &fakeTokens{}, Clock: clk, Metrics: metrics, Log: discardLogger(),
	}, AuthConfig{SessionTTL: 24 * time.Hour, SessionIdleTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return svc, users, sessions, metrics
}

// ---------------------------------------------------------------------------
// AuthService
// ---------------------------------------------------------------------------

func TestAuthService_FluxoCompleto(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	svc, _, sessions, _ := newAuth(t, clk)

	_, err := svc.Register(ctx, ports.RegisterInput{Nome: "Ana", Email: "ana@ex.com", Telefone: "11987654321", Senha: "segredo123", SaldoInicial: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, ports.RegisterInput{Nome: "Ana 2", Email: "ANA@ex.com", Telefone: "11911112222", Senha: "segredo123"}); !errors.Is(err, domain.ErrEmailAlreadyExists) {
		t.Fatalf("e-mail duplicado: %v", err)
	}
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "errada123"}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("senha errada: %v", err)
	}
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "naoexiste@ex.com", Senha: "segredo123"}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("usuário inexistente deve devolver o mesmo erro: %v", err)
	}

	out, err := svc.Login(ctx, ports.LoginInput{Email: " Ana@Ex.com ", Senha: "segredo123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, stored := sessions.m[out.Token]; stored {
		t.Fatal("o token em claro não pode ser a chave persistida")
	}
	sess, err := svc.Authenticate(ctx, out.Token)
	if err != nil || sess.UserID != out.User.ID {
		t.Fatalf("Authenticate = %v, %v", sess, err)
	}

	clk.Advance(2 * time.Hour) // excede idle timeout
	if _, err := svc.Authenticate(ctx, out.Token); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("sessão ociosa deveria expirar: %v", err)
	}

	out2, _ := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"})
	if err := svc.Logout(ctx, out2.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, out2.Token); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatal("sessão deveria ser inválida após logout")
	}
}

func TestAuthService_BloqueioProgressivo(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	svc, _, _, metrics := newAuth(t, clk)
	_, _ = svc.Register(ctx, ports.RegisterInput{Nome: "Ana", Email: "ana@ex.com", Telefone: "11987654321", Senha: "segredo123"})

	for i := 1; i <= 5; i++ {
		if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "errada123"}); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("falha %d: %v", i, err)
		}
	}
	// 6ª falha aplica bloqueio de 1 min.
	_, err := svc.Login(ctx, ports.LoginInput{Email: "ANA@ex.com", Senha: "errada123"})
	re, ok := domain.IsRetryAfter(err)
	if !ok || re.RetryAfter != time.Minute {
		t.Fatalf("6ª falha deveria bloquear 1 min: %v", err)
	}
	// Mesmo com a senha correta, a conta segue bloqueada.
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"}); err == nil {
		t.Fatal("login durante bloqueio deveria falhar")
	}

	clk.Advance(61 * time.Second)
	_, err = svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "errada123"}) // 7ª => 2 min
	if re, ok := domain.IsRetryAfter(err); !ok || re.RetryAfter != 2*time.Minute {
		t.Fatalf("7ª falha deveria bloquear 2 min: %v", err)
	}

	clk.Advance(121 * time.Second)
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"}); err != nil {
		t.Fatalf("após o bloqueio a senha correta deve funcionar: %v", err)
	}
	// Sucesso zera o contador.
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "errada123"}); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("contador deveria ter sido zerado: %v", err)
	}

	// E-mail inexistente também é bloqueado (não revela cadastro).
	for i := 0; i < 6; i++ {
		_, err = svc.Login(ctx, ports.LoginInput{Email: "fantasma@ex.com", Senha: "qualquer123"})
	}
	if _, ok := domain.IsRetryAfter(err); !ok {
		t.Fatalf("e-mail inexistente deveria ser bloqueado igualmente: %v", err)
	}
	if metrics.logins["bloqueado"] == 0 || metrics.logins["sucesso"] != 1 {
		t.Fatalf("métricas = %v", metrics.logins)
	}
}

func TestLoginLockoutPolicy(t *testing.T) {
	p := domain.DefaultLoginLockoutPolicy()
	want := map[int]time.Duration{1: 0, 5: 0, 6: time.Minute, 7: 2 * time.Minute, 8: 4 * time.Minute, 10: 16 * time.Minute, 11: 30 * time.Minute, 50: 30 * time.Minute}
	for f, d := range want {
		if got := p.LockDuration(f); got != d {
			t.Errorf("LockDuration(%d) = %s; want %s", f, got, d)
		}
	}
}

func TestAuthService_TrocaDeSenhaERevogacao(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	svc, _, sessions, _ := newAuth(t, clk)
	u, _ := svc.Register(ctx, ports.RegisterInput{Nome: "Ana", Email: "ana@ex.com", Telefone: "11987654321", Senha: "segredo123"})

	celular, _ := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"})
	notebook, _ := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"})
	tablet, _ := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"})

	// Encerrar as OUTRAS sessões mantém a atual.
	n, err := svc.RevokeSessions(ctx, u.ID, notebook.Token)
	if err != nil || n != 2 {
		t.Fatalf("revogar outras = %d, %v", n, err)
	}
	if _, err := svc.Authenticate(ctx, notebook.Token); err != nil {
		t.Fatal("sessão atual deveria continuar válida")
	}
	if _, err := svc.Authenticate(ctx, celular.Token); err == nil {
		t.Fatal("sessão do celular deveria ter sido revogada")
	}
	_ = tablet

	// Validações da troca de senha.
	if _, err := svc.ChangePassword(ctx, ports.ChangePasswordInput{UserID: u.ID, SenhaAtual: "errada123", NovaSenha: "novaSenha456"}); err == nil {
		t.Fatal("senha atual incorreta deveria falhar")
	}
	if _, err := svc.ChangePassword(ctx, ports.ChangePasswordInput{UserID: u.ID, SenhaAtual: "segredo123", NovaSenha: "segredo123"}); !errors.Is(err, domain.ErrPasswordReuse) {
		t.Fatalf("reuso de senha: %v", err)
	}
	if _, err := svc.ChangePassword(ctx, ports.ChangePasswordInput{UserID: u.ID, SenhaAtual: "segredo123", NovaSenha: "curta"}); err == nil {
		t.Fatal("nova senha fraca deveria falhar")
	}

	out, err := svc.ChangePassword(ctx, ports.ChangePasswordInput{UserID: u.ID, SenhaAtual: "segredo123", NovaSenha: "novaSenha456"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, notebook.Token); err == nil {
		t.Fatal("troca de senha deve encerrar TODAS as sessões anteriores")
	}
	if _, err := svc.Authenticate(ctx, out.Token); err != nil {
		t.Fatal("a nova sessão emitida deveria ser válida")
	}
	if len(sessions.m) != 1 {
		t.Fatalf("deveria restar 1 sessão, restam %d", len(sessions.m))
	}
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "segredo123"}); err == nil {
		t.Fatal("senha antiga não pode funcionar")
	}
	if _, err := svc.Login(ctx, ports.LoginInput{Email: "ana@ex.com", Senha: "novaSenha456"}); err != nil {
		t.Fatalf("nova senha: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Transações
// ---------------------------------------------------------------------------

func TestTransacaoService_EditarExcluir(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	txs := &fakeTransacoes{}
	fin := NewFinancasServices(newFakeUsers(), txs, nil, clk, nil, time.UTC, discardLogger())

	tx, err := fin.Transacoes.Create(ctx, "u1", ports.CreateTransacaoInput{Tipo: domain.TipoSaida, Valor: 1000, Categoria: "mercado"})
	if err != nil {
		t.Fatal(err)
	}
	data := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	up, err := fin.Transacoes.Update(ctx, "u1", tx.ID, ports.UpdateTransacaoInput{Tipo: domain.TipoEntrada, Valor: 2500, Categoria: "Freela", Descricao: "site", Data: &data})
	if err != nil || up.Valor != 2500 || up.Categoria != "freela" || up.Tipo != domain.TipoEntrada || up.Data.Day() != 1 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := fin.Transacoes.Update(ctx, "u1", tx.ID, ports.UpdateTransacaoInput{Tipo: domain.TipoSaida, Valor: 0}); err == nil {
		t.Fatal("valor zero deveria falhar")
	}

	// Outro usuário não enxerga nem altera.
	if _, err := fin.Transacoes.Get(ctx, "u2", tx.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("u2 enxergou transação de u1: %v", err)
	}
	if err := fin.Transacoes.Delete(ctx, "u2", tx.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("u2 excluiu transação de u1: %v", err)
	}

	// Parcela: pode editar valor, não pode virar entrada nem ser excluída avulsa.
	pid, n := "p1", 1
	parcela := &domain.Transacao{Tipo: domain.TipoSaida, Valor: 500, Categoria: "tv", Data: data, Origem: domain.OrigemParcelamento, ParcelamentoID: &pid, NumeroParcela: &n}
	_ = txs.Create(ctx, "u1", parcela)
	if _, err := fin.Transacoes.Update(ctx, "u1", parcela.ID, ports.UpdateTransacaoInput{Tipo: domain.TipoEntrada, Valor: 500}); err == nil {
		t.Fatal("parcela não pode virar entrada")
	}
	if _, err := fin.Transacoes.Update(ctx, "u1", parcela.ID, ports.UpdateTransacaoInput{Tipo: domain.TipoSaida, Valor: 550, Categoria: "tv"}); err != nil {
		t.Fatalf("editar valor da parcela: %v", err)
	}
	if err := fin.Transacoes.Delete(ctx, "u1", parcela.ID); !errors.Is(err, domain.ErrParcelaGerenciada) {
		t.Fatalf("excluir parcela avulsa: %v", err)
	}
	if err := fin.Transacoes.Delete(ctx, "u1", tx.ID); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Verificação de telefone + Webhook
// ---------------------------------------------------------------------------

type webhookFixture struct {
	users    *fakeUsers
	txs      *fakeTransacoes
	sender   *fakeSender
	codes    *fakeCodes
	clock    *fakeClock
	verifier *PhoneVerificationService
	webhook  *WebhookService
}

func newWebhookFixture(senderEnabled bool) *webhookFixture {
	f := &webhookFixture{
		users: newFakeUsers(), txs: &fakeTransacoes{}, sender: newFakeSender(), codes: &fakeCodes{next: "123456"},
		clock: &fakeClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)},
	}
	f.verifier = NewPhoneVerificationService(PhoneVerificationDeps{
		Users: f.users, Store: &fakeVerifications{m: map[string]*domain.PhoneVerification{}}, Codes: f.codes,
		Sender: f.sender, SenderEnabled: senderEnabled, Clock: f.clock, Log: discardLogger(),
	}, domain.DefaultPhoneVerificationPolicy())
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	f.webhook = NewWebhookService(WebhookDeps{
		Users: f.users, Transacoes: f.txs, Parser: NewRegexParserService(), Sender: f.sender,
		Verifier: f.verifier, Clock: f.clock, Location: loc, Log: discardLogger(),
	})
	return f
}

func TestPhoneVerification_MetodoMensagemPeloWhatsApp(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(false)
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})
	msg := func(id, texto string) domain.MensagemRecebida {
		return domain.MensagemRecebida{Provider: "evolution", MessageID: id, Telefone: "551187654321@s.whatsapp.net", Texto: texto}
	}

	// Sem verificação: não lança.
	res, _ := f.webhook.ProcessMessage(ctx, msg("M0", "Gastei 10 mercado"))
	if res.Status != domain.StatusTelefoneNaoVerificado || len(f.txs.items) != 0 {
		t.Fatalf("não verificado: %s", res.Status)
	}
	f.sender.next(t)

	// Método "codigo" indisponível sem sender.
	if _, err := f.verifier.Start(ctx, "user-1", domain.VerificacaoCodigoEnviado); !errors.Is(err, domain.ErrVerificationUnavailable) {
		t.Fatalf("codigo sem sender: %v", err)
	}

	out, err := f.verifier.Start(ctx, "user-1", "")
	if err != nil || out.Metodo != domain.VerificacaoMensagem || out.CodigoParaEnviar != "123456" {
		t.Fatalf("start = %+v, %v", out, err)
	}
	// Cooldown.
	if _, err := f.verifier.Start(ctx, "user-1", ""); err == nil {
		t.Fatal("reenvio imediato deveria respeitar cooldown")
	}
	// O código exibido no app NÃO pode ser confirmado pelo próprio app.
	if err := f.verifier.Confirm(ctx, "user-1", "123456"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatalf("método mensagem não pode ser confirmado pelo app: %v", err)
	}

	res, _ = f.webhook.ProcessMessage(ctx, msg("M1", "verificar 000000"))
	if res.Status != domain.StatusVerificacaoInvalida {
		t.Fatalf("código errado: %s", res.Status)
	}
	f.sender.next(t)

	res, _ = f.webhook.ProcessMessage(ctx, msg("M2", "Verificar 123456"))
	if res.Status != domain.StatusVerificacaoConfirmada {
		t.Fatalf("código certo: %s", res.Status)
	}
	f.sender.next(t)

	res, _ = f.webhook.ProcessMessage(ctx, msg("M3", "Gastei 150,00 mercado"))
	if res.Status != domain.StatusCriada || res.Transacao.UserID != "user-1" {
		t.Fatalf("após verificar: %+v", res)
	}
	if !strings.Contains(f.sender.next(t), "R$ 150,00") {
		t.Fatal("confirmação deveria citar o valor")
	}
	if res, _ := f.webhook.ProcessMessage(ctx, msg("M3", "Gastei 150,00 mercado")); res.Status != domain.StatusDuplicada {
		t.Fatalf("reentrega: %s", res.Status)
	}
}

func TestPhoneVerification_MetodoCodigoEnviado(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(true)
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})

	out, err := f.verifier.Start(ctx, "user-1", "")
	if err != nil || out.Metodo != domain.VerificacaoCodigoEnviado || out.CodigoParaEnviar != "" {
		t.Fatalf("start = %+v, %v", out, err)
	}
	if !strings.Contains(f.sender.next(t), "123456") {
		t.Fatal("código deveria ter sido enviado por WhatsApp")
	}

	// 5 tentativas erradas invalidam o desafio.
	for i := 0; i < 5; i++ {
		if err := f.verifier.Confirm(ctx, "user-1", "999999"); !errors.Is(err, domain.ErrVerificationInvalid) {
			t.Fatalf("tentativa %d: %v", i, err)
		}
	}
	if err := f.verifier.Confirm(ctx, "user-1", "123456"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatal("após 5 erros o código correto não pode mais valer")
	}

	f.clock.Advance(61 * time.Second)
	f.codes.next = "654321"
	if _, err := f.verifier.Start(ctx, "user-1", domain.VerificacaoCodigoEnviado); err != nil {
		t.Fatal(err)
	}
	f.sender.next(t)
	f.clock.Advance(11 * time.Minute) // expira
	if err := f.verifier.Confirm(ctx, "user-1", "654321"); !errors.Is(err, domain.ErrVerificationInvalid) {
		t.Fatal("código expirado não pode valer")
	}

	f.codes.next = "111222"
	_, _ = f.verifier.Start(ctx, "user-1", domain.VerificacaoCodigoEnviado)
	f.sender.next(t)
	if err := f.verifier.Confirm(ctx, "user-1", "111222"); err != nil {
		t.Fatalf("confirmação válida: %v", err)
	}
	if _, err := f.verifier.Start(ctx, "user-1", ""); !errors.Is(err, domain.ErrPhoneAlreadyVerified) {
		t.Fatalf("já verificado: %v", err)
	}
}

func TestWebhookService_ProcessMessage(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(false)
	agora := f.clock.Now()
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321", TelefoneVerificadoEm: &agora})
	_ = f.users.Create(ctx, &domain.User{Nome: "Beto", Email: "beto@ex.com", Telefone: "5521999998888", TelefoneVerificadoEm: &agora})

	msg := domain.MensagemRecebida{Provider: "evolution", MessageID: "M1", Telefone: "551187654321@s.whatsapp.net", Texto: "Gastei 150,00 mercado"}
	res, err := f.webhook.ProcessMessage(ctx, msg)
	if err != nil || res.Status != domain.StatusCriada || res.Transacao.UserID != "user-1" || res.Transacao.Valor != 15000 {
		t.Fatalf("resultado = %+v, %v", res, err)
	}
	f.sender.next(t)

	// Mesmo message ID em OUTRO usuário não colide (isolamento).
	other := msg
	other.Telefone = "5521999998888"
	if res, _ := f.webhook.ProcessMessage(ctx, other); res.Status != domain.StatusCriada || res.Transacao.UserID != "user-2" {
		t.Fatalf("outro usuário: %+v", res)
	}
	f.sender.next(t)

	cases := []struct {
		msg  domain.MensagemRecebida
		want domain.StatusProcessamento
	}{
		{domain.MensagemRecebida{Telefone: "5511987654321", Texto: "Gastei 10 x", FromMe: true}, domain.StatusIgnorada},
		{domain.MensagemRecebida{Telefone: "5511987654321", Texto: "Gastei 10 x", IsGroup: true}, domain.StatusIgnorada},
		{domain.MensagemRecebida{Telefone: "5531900000000", Texto: "Gastei 10 x"}, domain.StatusUsuarioNaoAchado},
	}
	for _, c := range cases {
		if res, _ := f.webhook.ProcessMessage(ctx, c.msg); res.Status != c.want {
			t.Errorf("%+v -> %s; want %s", c.msg, res.Status, c.want)
		}
	}

	if res, _ := f.webhook.ProcessMessage(ctx, domain.MensagemRecebida{Telefone: "5511987654321", Texto: "bom dia"}); res.Status != domain.StatusNaoInterpretada {
		t.Errorf("texto livre -> %s", res.Status)
	}
	f.sender.next(t)
	if res, _ := f.webhook.ProcessMessage(ctx, domain.MensagemRecebida{Telefone: "5511987654321", Texto: "Saldo?"}); res.Status != domain.StatusConsulta {
		t.Errorf("saldo -> %s", res.Status)
	}
	if !strings.Contains(f.sender.next(t), "Saldo atual") {
		t.Error("resposta de saldo inesperada")
	}
}

func TestWebhookService_VerificacaoDesligada(t *testing.T) {
	ctx := context.Background()
	f := newWebhookFixture(false)
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	f.webhook = NewWebhookService(WebhookDeps{
		Users: f.users, Transacoes: f.txs, Parser: NewRegexParserService(), Sender: f.sender,
		Verifier: f.verifier, SkipPhoneVerification: true, Clock: f.clock, Location: loc, Log: discardLogger(),
	})
	_ = f.users.Create(ctx, &domain.User{Nome: "Ana", Email: "ana@ex.com", Telefone: "5511987654321"})

	res, err := f.webhook.ProcessMessage(ctx, domain.MensagemRecebida{Provider: "evolution", MessageID: "S1", Telefone: "5511987654321", Texto: "Gastei 10 mercado"})
	if err != nil || res.Status != domain.StatusCriada {
		t.Fatalf("com verificação desligada deveria lançar: %+v, %v", res, err)
	}
	// Número desconhecido continua sendo rejeitado.
	if res, _ := f.webhook.ProcessMessage(ctx, domain.MensagemRecebida{Telefone: "5531900000000", Texto: "Gastei 10 x"}); res.Status != domain.StatusUsuarioNaoAchado {
		t.Fatalf("desconhecido: %s", res.Status)
	}
}
