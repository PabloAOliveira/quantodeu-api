package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// TipoTransacao indica se o lançamento soma ou subtrai do saldo.
type TipoTransacao string

const (
	TipoEntrada TipoTransacao = "entrada"
	TipoSaida   TipoTransacao = "saida"
)

// Valid informa se o tipo é conhecido.
func (t TipoTransacao) Valid() bool { return t == TipoEntrada || t == TipoSaida }

// OrigemTransacao indica por qual canal o lançamento foi criado.
type OrigemTransacao string

const (
	OrigemManual       OrigemTransacao = "manual"
	OrigemWhatsApp     OrigemTransacao = "whatsapp"
	OrigemParcelamento OrigemTransacao = "parcelamento"
)

// CategoriaRendimentos é a categoria usada para compor "Rendimentos" no perfil.
const CategoriaRendimentos = "rendimentos"

// CategoriaPadrao é usada quando o texto não traz categoria.
const CategoriaPadrao = "outros"

// Transacao é um lançamento financeiro de um usuário.
type Transacao struct {
	ID             string
	UserID         string
	Tipo           TipoTransacao
	Valor          Money // sempre positivo; o sinal é dado por Tipo
	Categoria      string
	Descricao      string
	Data           time.Time // data de competência (sem hora relevante)
	Origem         OrigemTransacao
	ExternalID     string  // id da mensagem no provedor (idempotência do webhook)
	ParcelamentoID *string // preenchido quando gerada por um parcelamento
	NumeroParcela  *int
	CreatedAt      time.Time
}

// NewTransacaoInput agrupa os dados brutos de um lançamento.
type NewTransacaoInput struct {
	UserID     string
	Tipo       TipoTransacao
	Valor      Money
	Categoria  string
	Descricao  string
	Data       time.Time
	Origem     OrigemTransacao
	ExternalID string
}

// NewTransacao valida e normaliza um lançamento.
func NewTransacao(in NewTransacaoInput) (*Transacao, error) {
	if strings.TrimSpace(in.UserID) == "" {
		return nil, NewValidationError("user_id", "obrigatório")
	}
	if !in.Tipo.Valid() {
		return nil, NewValidationError("tipo", "deve ser 'entrada' ou 'saida'")
	}
	if in.Valor <= 0 {
		return nil, NewValidationError("valor", "deve ser maior que zero")
	}
	if in.Valor > MaxMoney {
		return nil, NewValidationError("valor", "valor acima do limite permitido")
	}
	if in.Data.IsZero() {
		return nil, NewValidationError("data", "obrigatória")
	}
	if in.Origem == "" {
		in.Origem = OrigemManual
	}

	categoria := NormalizeCategoria(in.Categoria)
	descricao := strings.Join(strings.Fields(in.Descricao), " ")
	if utf8.RuneCountInString(descricao) > 255 {
		return nil, NewValidationError("descricao", "máximo de 255 caracteres")
	}
	if utf8.RuneCountInString(in.ExternalID) > 128 {
		return nil, NewValidationError("external_id", "máximo de 128 caracteres")
	}

	return &Transacao{
		UserID:     in.UserID,
		Tipo:       in.Tipo,
		Valor:      in.Valor,
		Categoria:  categoria,
		Descricao:  descricao,
		Data:       DateOnly(in.Data),
		Origem:     in.Origem,
		ExternalID: in.ExternalID,
	}, nil
}

// ValorAssinado devolve o valor com sinal: positivo para entrada, negativo para saída.
func (t *Transacao) ValorAssinado() Money {
	if t.Tipo == TipoSaida {
		return -t.Valor
	}
	return t.Valor
}

// NormalizeCategoria deixa a categoria em minúsculas, sem espaços duplicados e
// limitada a 60 caracteres. Vazia vira CategoriaPadrao.
func NormalizeCategoria(c string) string {
	c = strings.ToLower(strings.Join(strings.Fields(c), " "))
	if c == "" {
		return CategoriaPadrao
	}
	if utf8.RuneCountInString(c) > 60 {
		c = string([]rune(c)[:60])
	}
	return c
}

// DateOnly zera hora/minuto/segundo mantendo o fuso da data.
func DateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// UpdateTransacaoInput substitui os campos editáveis de um lançamento (PUT).
type UpdateTransacaoInput struct {
	Tipo      TipoTransacao
	Valor     Money
	Categoria string
	Descricao string
	Data      time.Time
}

// Aplicar valida e aplica a edição. Origem, external_id e o vínculo com o
// parcelamento são imutáveis; parcelas continuam sendo sempre saídas.
func (t *Transacao) Aplicar(in UpdateTransacaoInput) error {
	if t.ParcelamentoID != nil && in.Tipo != TipoSaida {
		return NewValidationError("tipo", "parcelas de um parcelamento são sempre 'saida'")
	}
	novo, err := NewTransacao(NewTransacaoInput{
		UserID: t.UserID, Tipo: in.Tipo, Valor: in.Valor, Categoria: in.Categoria,
		Descricao: in.Descricao, Data: in.Data, Origem: t.Origem, ExternalID: t.ExternalID,
	})
	if err != nil {
		return err
	}
	t.Tipo, t.Valor, t.Categoria, t.Descricao, t.Data = novo.Tipo, novo.Valor, novo.Categoria, novo.Descricao, novo.Data
	return nil
}
