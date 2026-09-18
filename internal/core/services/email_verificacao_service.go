package services

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// EmailVerificationService implementa ports.EmailVerificationUseCase:
// envia um código de 6 dígitos por e-mail e o confirma quando digitado no app.
type EmailVerificationService struct {
	users   ports.UserRepository
	store   ports.EmailVerificationStore
	codes   ports.CodeManager
	sender  ports.EmailSender
	clock   ports.Clock
	metrics ports.Metrics
	policy  domain.EmailVerificationPolicy
	appName string
	log     *slog.Logger

	// verified guarda em memória os usuários já confirmados. A confirmação é
	// definitiva (não há troca de e-mail), então o cache positivo é seguro e
	// poupa uma consulta ao banco por requisição protegida.
	verified sync.Map // userID -> struct{}
}

var _ ports.EmailVerificationUseCase = (*EmailVerificationService)(nil)

// EmailVerificationDeps agrupa as dependências.
type EmailVerificationDeps struct {
	Users   ports.UserRepository
	Store   ports.EmailVerificationStore
	Codes   ports.CodeManager
	Sender  ports.EmailSender
	Clock   ports.Clock
	Metrics ports.Metrics
	AppName string // nome exibido no e-mail (padrão: QuantoDeu)
	Log     *slog.Logger
}

// NewEmailVerificationService cria o serviço.
func NewEmailVerificationService(d EmailVerificationDeps, policy domain.EmailVerificationPolicy) *EmailVerificationService {
	if policy.TTL <= 0 {
		policy = domain.DefaultEmailVerificationPolicy()
	}
	if d.Metrics == nil {
		d.Metrics = NoopMetrics{}
	}
	if d.AppName == "" {
		d.AppName = "QuantoDeu"
	}
	return &EmailVerificationService{
		users: d.Users, store: d.Store, codes: d.Codes, sender: d.Sender, clock: d.Clock,
		metrics: d.Metrics, policy: policy, appName: d.AppName, log: d.Log,
	}
}

// hashScope separa os códigos de e-mail dos de telefone: um HMAC de um nunca
// vale para o outro, mesmo com o mesmo pepper.
func emailCodeScope(userID string) string { return "email:" + userID }

// Start gera e envia um novo código respeitando o cooldown.
func (s *EmailVerificationService) Start(ctx context.Context, userID string) (*ports.StartEmailVerificationOutput, error) {
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.EmailVerificado() {
		s.verified.Store(userID, struct{}{})
		return nil, domain.ErrEmailAlreadyVerified
	}

	now := s.clock.Now()
	if atual, err := s.store.Get(ctx, userID); err == nil {
		if wait := atual.CreatedAt.Add(s.policy.ResendCooldown).Sub(now); wait > 0 {
			return nil, &domain.RetryAfterError{
				Code:       "verification_cooldown",
				Message:    fmt.Sprintf("aguarde %d segundo(s) para reenviar o código", int(math.Ceil(wait.Seconds()))),
				RetryAfter: wait,
			}
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("verificação de e-mail: consultar desafio: %w", err)
	}

	code, err := s.codes.GenerateNumeric(digitosCodigo)
	if err != nil {
		return nil, fmt.Errorf("verificação de e-mail: gerar código: %w", err)
	}
	v := &domain.EmailVerification{
		UserID:    userID,
		Email:     user.Email,
		CodeHash:  s.codes.Hash(emailCodeScope(userID), code),
		ExpiresAt: now.Add(s.policy.TTL),
		CreatedAt: now,
	}
	if err := s.store.Save(ctx, v); err != nil {
		return nil, fmt.Errorf("verificação de e-mail: salvar desafio: %w", err)
	}

	if err := s.sender.Send(ctx, s.mensagem(user, code)); err != nil {
		_ = s.store.Delete(ctx, userID) // permite reenviar sem esperar o cooldown
		s.metrics.VerificacaoEmail("falha_envio")
		s.log.ErrorContext(ctx, "verificação de e-mail: falha no envio", slog.String("user_id", userID), slog.Any("err", err))
		return nil, domain.ErrEmailDeliveryFailed
	}

	s.metrics.VerificacaoEmail("enviado")
	s.log.InfoContext(ctx, "código de verificação de e-mail enviado", slog.String("user_id", userID))
	return &ports.StartEmailVerificationOutput{Email: user.Email, ExpiresAt: v.ExpiresAt}, nil
}

// Confirm valida o código digitado no app.
func (s *EmailVerificationService) Confirm(ctx context.Context, userID, codigo string) error {
	codigo = strings.TrimSpace(codigo)
	if !reCodigo.MatchString(codigo) {
		return domain.NewValidationError("codigo", "deve conter 6 dígitos")
	}
	v, err := s.store.Get(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		s.metrics.VerificacaoEmail("codigo_invalido")
		return domain.ErrVerificationInvalid
	}
	if err != nil {
		return fmt.Errorf("verificação de e-mail: consultar desafio: %w", err)
	}

	now := s.clock.Now()
	if v.IsExpired(now) || v.Attempts >= s.policy.MaxAttempts {
		_ = s.store.Delete(ctx, userID)
		s.metrics.VerificacaoEmail("codigo_invalido")
		return domain.ErrVerificationInvalid
	}

	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Email != v.Email { // e-mail mudou depois do envio
		_ = s.store.Delete(ctx, userID)
		return domain.ErrVerificationInvalid
	}

	if !s.codes.Equal(v.CodeHash, s.codes.Hash(emailCodeScope(userID), codigo)) {
		n, err := s.store.IncrementAttempts(ctx, userID)
		if err == nil && n >= s.policy.MaxAttempts {
			_ = s.store.Delete(ctx, userID)
		}
		s.metrics.VerificacaoEmail("codigo_invalido")
		return domain.ErrVerificationInvalid
	}

	if !user.EmailVerificado() {
		if err := s.users.MarkEmailVerified(ctx, userID, now); err != nil {
			return fmt.Errorf("verificação de e-mail: marcar e-mail: %w", err)
		}
	}
	_ = s.store.Delete(ctx, userID)
	s.verified.Store(userID, struct{}{})
	s.metrics.VerificacaoEmail("confirmada")
	s.log.InfoContext(ctx, "e-mail verificado", slog.String("user_id", userID))
	return nil
}

// IsVerified informa se o e-mail do usuário está confirmado.
func (s *EmailVerificationService) IsVerified(ctx context.Context, userID string) (bool, error) {
	if _, ok := s.verified.Load(userID); ok {
		return true, nil
	}
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return false, err
	}
	if user.EmailVerificado() {
		s.verified.Store(userID, struct{}{})
		return true, nil
	}
	return false, nil
}

// mensagem monta o e-mail com o código (texto + HTML simples).
func (s *EmailVerificationService) mensagem(u *domain.User, code string) domain.EmailMessage {
	minutos := int(s.policy.TTL / time.Minute)
	primeiroNome := strings.Fields(u.Nome)[0]
	text := fmt.Sprintf("Olá, %s!\n\nSeu código de confirmação do %s é: %s\n\n"+
		"Digite esse código no app. Ele expira em %d minutos.\n\n"+
		"Se você não criou uma conta no %s, ignore este e-mail.\n",
		primeiroNome, s.appName, code, minutos, s.appName)

	esc := html.EscapeString
	htmlBody := fmt.Sprintf(`<!doctype html>
<html lang="pt-BR"><body style="margin:0;padding:24px;background:#f4f5f7;font-family:Arial,Helvetica,sans-serif;color:#1f2933">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0"><tr><td align="center">
<table role="presentation" width="100%%" style="max-width:480px;background:#ffffff;border-radius:12px;padding:32px" cellspacing="0" cellpadding="0">
<tr><td style="font-size:20px;font-weight:bold;padding-bottom:16px">%s</td></tr>
<tr><td style="font-size:15px;line-height:22px;padding-bottom:16px">Olá, %s! Use o código abaixo para confirmar seu e-mail:</td></tr>
<tr><td align="center" style="font-size:34px;letter-spacing:8px;font-weight:bold;padding:16px 0;background:#f0f4ff;border-radius:8px">%s</td></tr>
<tr><td style="font-size:13px;line-height:20px;color:#52606d;padding-top:16px">O código expira em %d minutos. Se você não criou uma conta no %s, ignore este e-mail.</td></tr>
</table></td></tr></table></body></html>`,
		esc(s.appName), esc(primeiroNome), code, minutos, esc(s.appName))

	return domain.EmailMessage{
		To:      u.Email,
		Subject: fmt.Sprintf("%s é o seu código do %s", code, s.appName),
		Text:    text,
		HTML:    htmlBody,
	}
}
