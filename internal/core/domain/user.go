package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// User é a entidade raiz do tenant: todo dado financeiro pertence a um User.
type User struct {
	ID           string
	Nome         string
	Email        string
	Telefone     string // normalizado: 55 + DDD + número
	PasswordHash string
	SaldoInicial Money
	// TelefoneVerificadoEm é preenchido quando o usuário prova a posse do
	// número. Somente telefones verificados podem lançar via WhatsApp.
	TelefoneVerificadoEm *time.Time
	// EmailVerificadoEm é preenchido quando o usuário confirma o código
	// enviado por e-mail.
	EmailVerificadoEm *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TelefoneVerificado informa se o telefone já foi verificado.
func (u *User) TelefoneVerificado() bool { return u.TelefoneVerificadoEm != nil }

// EmailVerificado informa se o e-mail já foi confirmado.
func (u *User) EmailVerificado() bool { return u.EmailVerificadoEm != nil }

// Regras de senha.
const (
	PasswordMinLen = 8
	PasswordMaxLen = 128 // limita custo de hash e ataques de DoS
)

// NewUserInput agrupa os dados brutos de cadastro.
type NewUserInput struct {
	Nome         string
	Email        string
	Telefone     string
	Senha        string
	SaldoInicial Money
}

// NewUser valida e normaliza os dados de cadastro. O hash da senha é
// preenchido pelo serviço (via porta PasswordHasher), nunca aqui.
func NewUser(in NewUserInput) (*User, error) {
	nome := strings.Join(strings.Fields(in.Nome), " ")
	if n := utf8.RuneCountInString(nome); n < 2 || n > 120 {
		return nil, NewValidationError("nome", "deve ter entre 2 e 120 caracteres")
	}

	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}

	tel, err := NormalizePhoneBR(in.Telefone)
	if err != nil {
		return nil, err
	}

	if err := ValidatePassword(in.Senha); err != nil {
		return nil, err
	}

	if in.SaldoInicial > MaxMoney || in.SaldoInicial < -MaxMoney {
		return nil, NewValidationError("saldo_inicial", "valor fora do limite permitido")
	}

	return &User{
		Nome:         nome,
		Email:        email,
		Telefone:     tel,
		SaldoInicial: in.SaldoInicial,
	}, nil
}

// NormalizeEmail valida e coloca o e-mail em minúsculas.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) > 254 {
		return "", NewValidationError("email", "e-mail muito longo")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return "", NewValidationError("email", "e-mail inválido")
	}
	return email, nil
}

// ValidatePassword aplica a política mínima de senha: 8..128 caracteres com
// ao menos uma letra e um número.
func ValidatePassword(p string) error {
	n := utf8.RuneCountInString(p)
	if n < PasswordMinLen || n > PasswordMaxLen {
		return NewValidationError("senha", "deve ter entre 8 e 128 caracteres")
	}
	var hasLetter, hasDigit bool
	for _, r := range p {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return NewValidationError("senha", "deve conter ao menos uma letra e um número")
	}
	return nil
}
