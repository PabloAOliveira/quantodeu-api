package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// AuthConfig parametriza o ciclo de vida das sessões e o bloqueio de login.
type AuthConfig struct {
	SessionTTL         time.Duration // expiração absoluta
	SessionIdleTimeout time.Duration // expiração por inatividade (0 = desativado)
	TouchInterval      time.Duration // intervalo mínimo entre atualizações de last_seen
	Lockout            domain.LoginLockoutPolicy
}

// AuthService implementa ports.AuthUseCase.
type AuthService struct {
	users    ports.UserRepository
	sessions ports.SessionStore
	attempts ports.LoginAttemptStore
	hasher   ports.PasswordHasher
	tokens   ports.TokenManager
	clock    ports.Clock
	metrics  ports.Metrics
	emailVer ports.EmailVerificationUseCase
	cfg      AuthConfig
	log      *slog.Logger

	// dummyHash é verificado quando o e-mail não existe, para que o tempo de
	// resposta seja equivalente e não permita enumerar usuários.
	dummyHash string
}

var _ ports.AuthUseCase = (*AuthService)(nil)

// AuthDeps agrupa as dependências do AuthService.
type AuthDeps struct {
	Users    ports.UserRepository
	Sessions ports.SessionStore
	Attempts ports.LoginAttemptStore
	Hasher   ports.PasswordHasher
	Tokens   ports.TokenManager
	Clock    ports.Clock
	Metrics  ports.Metrics
	Log      *slog.Logger
	// EmailVerification (opcional) envia o código de confirmação logo após o
	// cadastro. Falha no envio não impede o cadastro: o app pode reenviar.
	EmailVerification ports.EmailVerificationUseCase
}

// NewAuthService cria o serviço de autenticação.
func NewAuthService(d AuthDeps, cfg AuthConfig) (*AuthService, error) {
	if cfg.SessionTTL <= 0 {
		return nil, errors.New("auth: SessionTTL deve ser > 0")
	}
	if cfg.TouchInterval <= 0 {
		cfg.TouchInterval = 5 * time.Minute
	}
	if cfg.Lockout.FreeAttempts <= 0 {
		cfg.Lockout = domain.DefaultLoginLockoutPolicy()
	}
	if d.Metrics == nil {
		d.Metrics = NoopMetrics{}
	}
	dummy, err := d.Hasher.Hash("quantodeu-timing-equalizer-2f7c1a")
	if err != nil {
		return nil, fmt.Errorf("auth: gerar dummy hash: %w", err)
	}
	return &AuthService{
		users: d.Users, sessions: d.Sessions, attempts: d.Attempts, hasher: d.Hasher, tokens: d.Tokens,
		clock: d.Clock, metrics: d.Metrics, emailVer: d.EmailVerification, cfg: cfg, log: d.Log, dummyHash: dummy,
	}, nil
}

// Register valida, gera o hash da senha e persiste o usuário.
func (s *AuthService) Register(ctx context.Context, in ports.RegisterInput) (*domain.User, error) {
	u, err := domain.NewUser(domain.NewUserInput{
		Nome: in.Nome, Email: in.Email, Telefone: in.Telefone,
		Senha: in.Senha, SaldoInicial: in.SaldoInicial,
	})
	if err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(in.Senha)
	if err != nil {
		return nil, fmt.Errorf("register: hash: %w", err)
	}
	u.PasswordHash = hash

	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "usuário registrado", slog.String("user_id", u.ID))

	if s.emailVer != nil {
		if _, err := s.emailVer.Start(ctx, u.ID); err != nil {
			s.log.WarnContext(ctx, "cadastro: código de confirmação não enviado (o app pode reenviar)",
				slog.String("user_id", u.ID), slog.Any("err", err))
		}
	}
	return u, nil
}

// Login autentica aplicando bloqueio progressivo por conta e cria uma nova
// sessão (sempre um token novo: evita session fixation).
func (s *AuthService) Login(ctx context.Context, in ports.LoginInput) (*ports.LoginOutput, error) {
	now := s.clock.Now()
	lockKey := domain.LockKeyFromEmail(in.Email)

	// 1) Conta bloqueada? (vale também para e-mails inexistentes)
	if s.attempts != nil && lockKey != "" {
		st, err := s.attempts.Get(ctx, lockKey)
		if err != nil {
			s.log.WarnContext(ctx, "login: falha ao consultar bloqueio (seguindo sem bloqueio)", slog.Any("err", err))
		} else if st.LockedUntil.After(now) {
			s.metrics.LoginTentativa("bloqueado")
			return nil, lockedError(st.LockedUntil.Sub(now))
		}
	}

	user, err := s.checkCredentials(ctx, in.Email, in.Senha)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			s.metrics.LoginTentativa("credenciais_invalidas")
			if lockErr := s.registerFailure(ctx, lockKey, now, in.IP); lockErr != nil {
				return nil, lockErr
			}
		}
		return nil, err
	}

	if s.attempts != nil {
		if err := s.attempts.Reset(ctx, lockKey); err != nil {
			s.log.WarnContext(ctx, "login: falha ao zerar tentativas", slog.Any("err", err))
		}
	}

	out, err := s.newSession(ctx, user, in.UserAgent, in.IP)
	if err != nil {
		return nil, err
	}
	s.metrics.LoginTentativa("sucesso")
	s.log.InfoContext(ctx, "login efetuado", slog.String("user_id", user.ID))
	return out, nil
}

// checkCredentials valida e-mail/senha em tempo equivalente para contas
// existentes e inexistentes, e faz rehash transparente quando necessário.
func (s *AuthService) checkCredentials(ctx context.Context, rawEmail, senha string) (*domain.User, error) {
	if utf8.RuneCountInString(senha) > domain.PasswordMaxLen {
		return nil, domain.ErrInvalidCredentials
	}
	var user *domain.User
	if email, err := domain.NormalizeEmail(rawEmail); err == nil {
		u, err := s.users.FindByEmail(ctx, email)
		switch {
		case err == nil:
			user = u
		case !errors.Is(err, domain.ErrNotFound):
			return nil, fmt.Errorf("login: buscar usuário: %w", err)
		}
	}
	if user == nil {
		_, _, _ = s.hasher.Verify(senha, s.dummyHash) // equaliza tempo
		return nil, domain.ErrInvalidCredentials
	}

	ok, needsRehash, err := s.hasher.Verify(senha, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("login: verificar senha: %w", err)
	}
	if !ok {
		return nil, domain.ErrInvalidCredentials
	}
	if needsRehash {
		if h, err := s.hasher.Hash(senha); err == nil {
			if err := s.users.UpdatePasswordHash(ctx, user.ID, h); err != nil {
				s.log.WarnContext(ctx, "falha ao atualizar hash", slog.String("user_id", user.ID), slog.Any("err", err))
			}
		}
	}
	return user, nil
}

// registerFailure contabiliza a falha e, se a política exigir, bloqueia a
// chave. Devolve um RetryAfterError quando a falha atual gerou bloqueio.
func (s *AuthService) registerFailure(ctx context.Context, key string, now time.Time, ip string) error {
	if s.attempts == nil || key == "" {
		return nil
	}
	failures, err := s.attempts.RegisterFailure(ctx, key, now, s.cfg.Lockout.Window)
	if err != nil {
		s.log.WarnContext(ctx, "login: falha ao registrar tentativa", slog.Any("err", err))
		return nil
	}
	lock := s.cfg.Lockout.LockDuration(failures)
	s.log.WarnContext(ctx, "falha de login", slog.Int("falhas", failures), slog.String("ip", ip), slog.Duration("bloqueio", lock))
	if lock <= 0 {
		return nil
	}
	if err := s.attempts.Lock(ctx, key, now.Add(lock)); err != nil {
		s.log.WarnContext(ctx, "login: falha ao bloquear conta", slog.Any("err", err))
		return nil
	}
	s.metrics.LoginTentativa("bloqueio_aplicado")
	return lockedError(lock)
}

func lockedError(d time.Duration) error {
	return &domain.RetryAfterError{
		Code:       "account_locked",
		Message:    fmt.Sprintf("muitas tentativas de login; tente novamente em %d segundo(s)", int(math.Ceil(d.Seconds()))),
		RetryAfter: d,
	}
}

func (s *AuthService) newSession(ctx context.Context, user *domain.User, userAgent, ip string) (*ports.LoginOutput, error) {
	token, err := s.tokens.Generate()
	if err != nil {
		return nil, fmt.Errorf("sessão: gerar token: %w", err)
	}
	now := s.clock.Now()
	sess := &domain.Session{
		TokenHash:  s.tokens.Hash(token),
		UserID:     user.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.cfg.SessionTTL),
		LastSeenAt: now,
		UserAgent:  truncate(userAgent, 255),
		IP:         truncate(ip, 64),
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return nil, fmt.Errorf("sessão: criar: %w", err)
	}
	return &ports.LoginOutput{Token: token, Session: sess, User: user}, nil
}

// Logout invalida a sessão. É idempotente.
func (s *AuthService) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.sessions.Delete(ctx, s.tokens.Hash(token)); err != nil && !errors.Is(err, domain.ErrSessionNotFound) {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

// Authenticate valida o token e aplica expiração absoluta e por inatividade.
func (s *AuthService) Authenticate(ctx context.Context, token string) (*domain.Session, error) {
	if token == "" {
		return nil, domain.ErrSessionNotFound
	}
	hash := s.tokens.Hash(token)
	sess, err := s.sessions.FindByTokenHash(ctx, hash)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	idleExpired := s.cfg.SessionIdleTimeout > 0 && now.Sub(sess.LastSeenAt) > s.cfg.SessionIdleTimeout
	if sess.IsExpired(now) || idleExpired {
		_ = s.sessions.Delete(ctx, hash)
		return nil, domain.ErrSessionNotFound
	}

	if now.Sub(sess.LastSeenAt) >= s.cfg.TouchInterval {
		if err := s.sessions.Touch(ctx, hash, now); err != nil {
			s.log.WarnContext(ctx, "falha ao atualizar last_seen", slog.Any("err", err))
		}
		sess.LastSeenAt = now
	}
	return sess, nil
}

// ChangePassword valida a senha atual, grava a nova, encerra TODAS as sessões
// (inclusive a atual) e cria uma sessão nova para o dispositivo que trocou.
func (s *AuthService) ChangePassword(ctx context.Context, in ports.ChangePasswordInput) (*ports.LoginOutput, error) {
	user, err := s.users.FindByID(ctx, in.UserID)
	if err != nil {
		return nil, err
	}
	ok, _, err := s.hasher.Verify(in.SenhaAtual, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("trocar senha: verificar: %w", err)
	}
	if !ok {
		return nil, domain.NewValidationError("senha_atual", "senha atual incorreta")
	}
	if err := domain.ValidatePassword(in.NovaSenha); err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			return nil, domain.NewValidationError("nova_senha", ve.Message)
		}
		return nil, err
	}
	if in.NovaSenha == in.SenhaAtual {
		return nil, domain.ErrPasswordReuse
	}

	hash, err := s.hasher.Hash(in.NovaSenha)
	if err != nil {
		return nil, fmt.Errorf("trocar senha: hash: %w", err)
	}
	if err := s.users.UpdatePasswordHash(ctx, user.ID, hash); err != nil {
		return nil, fmt.Errorf("trocar senha: gravar: %w", err)
	}

	n, err := s.sessions.DeleteAllForUser(ctx, user.ID, "")
	if err != nil {
		return nil, fmt.Errorf("trocar senha: revogar sessões: %w", err)
	}
	s.metrics.SessoesRevogadas("troca_de_senha", n)
	if s.attempts != nil {
		_ = s.attempts.Reset(ctx, domain.LockKeyFromEmail(user.Email))
	}

	out, err := s.newSession(ctx, user, in.UserAgent, in.IP)
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "senha alterada; sessões revogadas", slog.String("user_id", user.ID), slog.Int64("sessoes", n))
	return out, nil
}

// RevokeSessions encerra as sessões do usuário, opcionalmente preservando a atual.
func (s *AuthService) RevokeSessions(ctx context.Context, userID, keepToken string) (int64, error) {
	keep := ""
	if keepToken != "" {
		keep = s.tokens.Hash(keepToken)
	}
	n, err := s.sessions.DeleteAllForUser(ctx, userID, keep)
	if err != nil {
		return 0, fmt.Errorf("revogar sessões: %w", err)
	}
	motivo := "todas"
	if keep != "" {
		motivo = "outras"
	}
	s.metrics.SessoesRevogadas(motivo, n)
	s.log.InfoContext(ctx, "sessões revogadas", slog.String("user_id", userID), slog.Int64("total", n), slog.String("escopo", motivo))
	return n, nil
}

func truncate(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// NoopMetrics descarta métricas (testes ou quando a observabilidade está desligada).
type NoopMetrics struct{}

// TransacaoRegistrada implementa ports.Metrics.
func (NoopMetrics) TransacaoRegistrada(string, string) {}

// WebhookProcessado implementa ports.Metrics.
func (NoopMetrics) WebhookProcessado(string, string) {}

// LoginTentativa implementa ports.Metrics.
func (NoopMetrics) LoginTentativa(string) {}

// VerificacaoTelefone implementa ports.Metrics.
func (NoopMetrics) VerificacaoTelefone(string) {}

// VerificacaoEmail implementa ports.Metrics.
func (NoopMetrics) VerificacaoEmail(string) {}

// SessoesRevogadas implementa ports.Metrics.
func (NoopMetrics) SessoesRevogadas(string, int64) {}
