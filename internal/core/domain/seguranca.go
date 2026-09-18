package domain

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Bloqueio progressivo de login
// ---------------------------------------------------------------------------

// LoginLockoutPolicy define o bloqueio progressivo por conta (e-mail).
//
// Após FreeAttempts falhas dentro de Window, cada nova falha bloqueia a conta
// por BaseLock * 2^(falhas-FreeAttempts), limitado a MaxLock.
// Ex. padrão: 5 falhas livres; 6ª => 1 min, 7ª => 2 min, 8ª => 4 min ... até 30 min.
//
// A chave é o e-mail normalizado, EXISTINDO ou não a conta — assim o bloqueio
// não revela quais e-mails estão cadastrados.
type LoginLockoutPolicy struct {
	FreeAttempts int
	BaseLock     time.Duration
	MaxLock      time.Duration
	Window       time.Duration
}

// DefaultLoginLockoutPolicy devolve a política padrão.
func DefaultLoginLockoutPolicy() LoginLockoutPolicy {
	return LoginLockoutPolicy{FreeAttempts: 5, BaseLock: time.Minute, MaxLock: 30 * time.Minute, Window: 24 * time.Hour}
}

// LockDuration devolve o tempo de bloqueio após `failures` falhas acumuladas.
func (p LoginLockoutPolicy) LockDuration(failures int) time.Duration {
	if failures <= p.FreeAttempts {
		return 0
	}
	d := p.BaseLock
	for i := p.FreeAttempts + 1; i < failures; i++ {
		d *= 2
		if d >= p.MaxLock {
			return p.MaxLock
		}
	}
	if d > p.MaxLock {
		return p.MaxLock
	}
	return d
}

// LoginAttemptState é o estado de falhas de uma chave de login.
type LoginAttemptState struct {
	Failures    int
	LockedUntil time.Time
}

// LockKeyFromEmail normaliza o e-mail para uso como chave de bloqueio, mesmo
// que ele seja sintaticamente inválido (evita contornar com maiúsculas/espaços).
func LockKeyFromEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ---------------------------------------------------------------------------
// Verificação de telefone
// ---------------------------------------------------------------------------

// MetodoVerificacao define como o usuário prova a posse do número.
type MetodoVerificacao string

const (
	// VerificacaoCodigoEnviado: a API envia um código de 6 dígitos para o
	// WhatsApp do usuário, que o digita no app (exige sender habilitado).
	VerificacaoCodigoEnviado MetodoVerificacao = "codigo"
	// VerificacaoMensagem: o app exibe um código e o usuário o ENVIA do seu
	// WhatsApp ("verificar 123456"). Funciona sem envio ativo de mensagens.
	VerificacaoMensagem MetodoVerificacao = "mensagem"
)

// Valid informa se o método é conhecido.
func (m MetodoVerificacao) Valid() bool {
	return m == VerificacaoCodigoEnviado || m == VerificacaoMensagem
}

// PhoneVerificationPolicy parametriza os códigos.
type PhoneVerificationPolicy struct {
	TTL            time.Duration // validade do código
	MaxAttempts    int           // tentativas erradas antes de invalidar
	ResendCooldown time.Duration // intervalo mínimo entre códigos
}

// DefaultPhoneVerificationPolicy devolve a política padrão.
func DefaultPhoneVerificationPolicy() PhoneVerificationPolicy {
	return PhoneVerificationPolicy{TTL: 10 * time.Minute, MaxAttempts: 5, ResendCooldown: 60 * time.Second}
}

// PhoneVerification é um desafio pendente de verificação de telefone.
// O código NUNCA é persistido em claro: apenas CodeHash (HMAC com pepper).
type PhoneVerification struct {
	UserID    string
	Telefone  string
	Metodo    MetodoVerificacao
	CodeHash  string
	Attempts  int
	ExpiresAt time.Time
	CreatedAt time.Time
}

// IsExpired informa se o desafio expirou.
func (v *PhoneVerification) IsExpired(now time.Time) bool { return !now.Before(v.ExpiresAt) }

// ---------------------------------------------------------------------------
// Verificação de e-mail
// ---------------------------------------------------------------------------

// EmailVerificationPolicy parametriza os códigos enviados por e-mail.
type EmailVerificationPolicy struct {
	TTL            time.Duration // validade do código
	MaxAttempts    int           // tentativas erradas antes de invalidar
	ResendCooldown time.Duration // intervalo mínimo entre envios
}

// DefaultEmailVerificationPolicy devolve a política padrão (e-mail pode
// demorar a chegar: validade maior que a do telefone).
func DefaultEmailVerificationPolicy() EmailVerificationPolicy {
	return EmailVerificationPolicy{TTL: 30 * time.Minute, MaxAttempts: 5, ResendCooldown: 60 * time.Second}
}

// EmailVerification é um desafio pendente de confirmação de e-mail.
// O código NUNCA é persistido em claro: apenas CodeHash (HMAC com pepper).
type EmailVerification struct {
	UserID    string
	Email     string // e-mail para o qual o código foi enviado
	CodeHash  string
	Attempts  int
	ExpiresAt time.Time
	CreatedAt time.Time
}

// IsExpired informa se o desafio expirou.
func (v *EmailVerification) IsExpired(now time.Time) bool { return !now.Before(v.ExpiresAt) }

// EmailMessage é um e-mail transacional (texto puro + HTML opcional).
type EmailMessage struct {
	To      string
	Subject string
	Text    string
	HTML    string
}
