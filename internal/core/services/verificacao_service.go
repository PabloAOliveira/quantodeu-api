package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// PhoneVerificationService implementa ports.PhoneVerificationUseCase e é
// usado pelo WebhookService para o método "mensagem".
type PhoneVerificationService struct {
	users         ports.UserRepository
	store         ports.PhoneVerificationStore
	codes         ports.CodeManager
	sender        ports.WhatsAppSender
	senderEnabled bool
	clock         ports.Clock
	metrics       ports.Metrics
	policy        domain.PhoneVerificationPolicy
	log           *slog.Logger
}

var _ ports.PhoneVerificationUseCase = (*PhoneVerificationService)(nil)

// PhoneVerificationDeps agrupa as dependências.
type PhoneVerificationDeps struct {
	Users         ports.UserRepository
	Store         ports.PhoneVerificationStore
	Codes         ports.CodeManager
	Sender        ports.WhatsAppSender
	SenderEnabled bool // false => apenas o método "mensagem" fica disponível
	Clock         ports.Clock
	Metrics       ports.Metrics
	Log           *slog.Logger
}

// NewPhoneVerificationService cria o serviço.
func NewPhoneVerificationService(d PhoneVerificationDeps, policy domain.PhoneVerificationPolicy) *PhoneVerificationService {
	if policy.TTL <= 0 {
		policy = domain.DefaultPhoneVerificationPolicy()
	}
	if d.Metrics == nil {
		d.Metrics = NoopMetrics{}
	}
	return &PhoneVerificationService{
		users: d.Users, store: d.Store, codes: d.Codes, sender: d.Sender, senderEnabled: d.SenderEnabled,
		clock: d.Clock, metrics: d.Metrics, policy: policy, log: d.Log,
	}
}

const digitosCodigo = 6

var reCodigo = regexp.MustCompile(`^\d{6}$`)

// Start cria (ou substitui) o desafio de verificação respeitando o cooldown.
func (s *PhoneVerificationService) Start(ctx context.Context, userID string, metodo domain.MetodoVerificacao) (*ports.StartVerificationOutput, error) {
	if metodo == "" {
		metodo = domain.VerificacaoMensagem
		if s.senderEnabled {
			metodo = domain.VerificacaoCodigoEnviado
		}
	}
	if !metodo.Valid() {
		return nil, domain.NewValidationError("metodo", "deve ser 'codigo' ou 'mensagem'")
	}
	if metodo == domain.VerificacaoCodigoEnviado && !s.senderEnabled {
		return nil, domain.ErrVerificationUnavailable
	}

	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.TelefoneVerificado() {
		return nil, domain.ErrPhoneAlreadyVerified
	}

	now := s.clock.Now()
	if atual, err := s.store.Get(ctx, userID); err == nil {
		if wait := atual.CreatedAt.Add(s.policy.ResendCooldown).Sub(now); wait > 0 {
			return nil, &domain.RetryAfterError{
				Code:       "verification_cooldown",
				Message:    fmt.Sprintf("aguarde %d segundo(s) para gerar um novo código", int(math.Ceil(wait.Seconds()))),
				RetryAfter: wait,
			}
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("verificação: consultar desafio: %w", err)
	}

	code, err := s.codes.GenerateNumeric(digitosCodigo)
	if err != nil {
		return nil, fmt.Errorf("verificação: gerar código: %w", err)
	}
	v := &domain.PhoneVerification{
		UserID:    userID,
		Telefone:  user.Telefone,
		Metodo:    metodo,
		CodeHash:  s.codes.Hash(userID, code),
		ExpiresAt: now.Add(s.policy.TTL),
		CreatedAt: now,
	}
	if err := s.store.Save(ctx, v); err != nil {
		return nil, fmt.Errorf("verificação: salvar desafio: %w", err)
	}

	out := &ports.StartVerificationOutput{Metodo: metodo, ExpiresAt: v.ExpiresAt}
	minutos := int(s.policy.TTL.Minutes())
	switch metodo {
	case domain.VerificacaoCodigoEnviado:
		msg := fmt.Sprintf("🔐 Seu código de verificação do QuantoDeu é *%s*.\nEle expira em %d minutos. Não compartilhe este código.", code, minutos)
		if err := s.sender.SendText(ctx, user.Telefone, msg); err != nil {
			_ = s.store.Delete(ctx, userID)
			return nil, fmt.Errorf("verificação: enviar código: %w", err)
		}
		out.Instrucao = "Enviamos um código de 6 dígitos para o seu WhatsApp. Confirme em POST /api/v1/me/telefone/verificacao/confirmar."
	case domain.VerificacaoMensagem:
		out.CodigoParaEnviar = code
		out.Instrucao = fmt.Sprintf("Envie a mensagem \"verificar %s\" do WhatsApp %s para o número do QuantoDeu em até %d minutos.", code, user.Telefone, minutos)
	}
	s.metrics.VerificacaoTelefone("iniciada_" + string(metodo))
	s.log.InfoContext(ctx, "verificação de telefone iniciada", slog.String("user_id", userID), slog.String("metodo", string(metodo)))
	return out, nil
}

// Confirm valida o código digitado no app (apenas método "codigo": no método
// "mensagem" o código já é conhecido pelo cliente e só vale vindo do WhatsApp).
func (s *PhoneVerificationService) Confirm(ctx context.Context, userID, codigo string) error {
	return s.confirm(ctx, userID, codigo, domain.VerificacaoCodigoEnviado)
}

// ConfirmFromWhatsApp valida o código enviado pelo próprio número do usuário.
func (s *PhoneVerificationService) ConfirmFromWhatsApp(ctx context.Context, userID, codigo string) error {
	return s.confirm(ctx, userID, codigo, domain.VerificacaoMensagem)
}

func (s *PhoneVerificationService) confirm(ctx context.Context, userID, codigo string, metodo domain.MetodoVerificacao) error {
	if !reCodigo.MatchString(codigo) {
		return domain.NewValidationError("codigo", "deve conter 6 dígitos")
	}
	v, err := s.store.Get(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		s.metrics.VerificacaoTelefone("codigo_invalido")
		return domain.ErrVerificationInvalid
	}
	if err != nil {
		return fmt.Errorf("verificação: consultar desafio: %w", err)
	}

	now := s.clock.Now()
	if v.Metodo != metodo || v.IsExpired(now) || v.Attempts >= s.policy.MaxAttempts {
		if v.IsExpired(now) || v.Attempts >= s.policy.MaxAttempts {
			_ = s.store.Delete(ctx, userID)
		}
		s.metrics.VerificacaoTelefone("codigo_invalido")
		return domain.ErrVerificationInvalid
	}

	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	// Se o telefone mudou depois de gerar o código, o desafio não vale mais.
	if user.Telefone != v.Telefone {
		_ = s.store.Delete(ctx, userID)
		return domain.ErrVerificationInvalid
	}

	if !s.codes.Equal(v.CodeHash, s.codes.Hash(userID, codigo)) {
		n, err := s.store.IncrementAttempts(ctx, userID)
		if err == nil && n >= s.policy.MaxAttempts {
			_ = s.store.Delete(ctx, userID)
		}
		s.metrics.VerificacaoTelefone("codigo_invalido")
		return domain.ErrVerificationInvalid
	}

	if err := s.users.MarkPhoneVerified(ctx, userID, now); err != nil {
		return fmt.Errorf("verificação: marcar telefone: %w", err)
	}
	_ = s.store.Delete(ctx, userID)
	s.metrics.VerificacaoTelefone("confirmada_" + string(metodo))
	s.log.InfoContext(ctx, "telefone verificado", slog.String("user_id", userID), slog.String("metodo", string(metodo)))
	return nil
}
