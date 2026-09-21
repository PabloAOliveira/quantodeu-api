// Package domain contém as entidades e regras de negócio puras do QuantoDeu.
//
// REGRA DE OURO: este pacote só pode importar a biblioteca padrão do Go.
// Nada de Gin, pgx, GORM, Redis ou tags de ORM/JSON aqui.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Erros sentinela do domínio. Os adaptadores de entrada traduzem estes erros
// para códigos HTTP; os adaptadores de saída traduzem erros de infraestrutura
// para estes valores.
var (
	ErrNotFound           = errors.New("recurso não encontrado")
	ErrEmailAlreadyExists = errors.New("e-mail já cadastrado")
	ErrPhoneAlreadyExists = errors.New("telefone já cadastrado")
	ErrInvalidCredentials = errors.New("credenciais inválidas")
	ErrSessionNotFound    = errors.New("sessão inválida ou expirada")
	ErrDuplicateMessage   = errors.New("mensagem já processada")
	ErrUnparseableMessage = errors.New("mensagem não reconhecida como lançamento")

	// Transações / parcelamentos
	ErrParcelaGerenciada = errors.New("esta transação é uma parcela: altere ou exclua pelo parcelamento")

	// Verificação de telefone
	ErrPhoneNotVerified        = errors.New("telefone ainda não verificado")
	ErrPhoneAlreadyVerified    = errors.New("telefone já verificado")
	ErrVerificationInvalid     = errors.New("código de verificação inválido ou expirado")
	ErrVerificationUnavailable = errors.New("envio de código pelo WhatsApp não está habilitado; use o método 'mensagem'")
	ErrEmailNotVerified        = errors.New("confirme seu e-mail para continuar")
	ErrEmailAlreadyVerified    = errors.New("e-mail já verificado")
	ErrEmailDeliveryFailed     = errors.New("não foi possível enviar o e-mail agora; tente novamente em instantes")
	ErrPasswordReuse           = errors.New("a nova senha deve ser diferente da atual")
	ErrCartaoComHistorico      = errors.New("este cartão tem compras registradas; arquive-o em vez de excluir")
	ErrFaturaJaPaga            = errors.New("esta fatura já foi paga")
	ErrFaturaNaoPaga           = errors.New("esta fatura não tem pagamento registrado")
	ErrFaturaVazia             = errors.New("esta fatura não tem compras")
	ErrFaturaFechadaParaCompra = errors.New("esta fatura já foi paga; desfaça o pagamento antes de mexer nas compras dela")
)

// RetryAfterError indica que a operação foi temporariamente bloqueada
// (conta bloqueada por tentativas de login, reenvio de código muito cedo...).
type RetryAfterError struct {
	Code       string // identificador estável para o cliente
	Message    string
	RetryAfter time.Duration
}

func (e *RetryAfterError) Error() string { return e.Message }

// IsRetryAfter extrai um RetryAfterError da cadeia de erros.
func IsRetryAfter(err error) (*RetryAfterError, bool) {
	var re *RetryAfterError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

// ValidationError representa uma violação de regra de negócio em um campo.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// NewValidationError cria um erro de validação para o campo informado.
func NewValidationError(field, msg string) error {
	return &ValidationError{Field: field, Message: msg}
}

// IsValidationError informa se err (ou algum erro encadeado) é de validação.
func IsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}
