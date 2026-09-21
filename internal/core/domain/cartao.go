package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Cartao é um cartão de crédito do usuário.
//
// Ele existe para resolver um descompasso: a compra acontece num mês e o
// dinheiro sai da conta em outro. O app trabalha em regime de caixa — o saldo
// só muda quando o dinheiro se move —, então a compra no cartão NÃO mexe no
// saldo. Quem mexe é o pagamento da fatura.
type Cartao struct {
	ID            string
	UserID        string
	Nome          string
	Banco         string
	DiaFechamento int   // 1–31: fecha o ciclo de compras
	DiaVencimento int   // 1–31: prazo de pagamento
	Limite        Money // zero = não informado
	Ativo         bool
	CreatedAt     time.Time
}

// MaxDiaCiclo é o maior dia aceito para fechamento e vencimento. Meses mais
// curtos caem no último dia (ver AddMonthsClamped).
const MaxDiaCiclo = 31

// NewCartaoInput agrupa os dados brutos de cadastro.
type NewCartaoInput struct {
	UserID        string
	Nome          string
	Banco         string
	DiaFechamento int
	DiaVencimento int
	Limite        Money
}

// NewCartao valida e normaliza o cadastro.
func NewCartao(in NewCartaoInput) (*Cartao, error) {
	if strings.TrimSpace(in.UserID) == "" {
		return nil, NewValidationError("user_id", "obrigatório")
	}
	nome := strings.Join(strings.Fields(in.Nome), " ")
	if n := utf8.RuneCountInString(nome); n < 1 || n > 60 {
		return nil, NewValidationError("nome", "deve ter entre 1 e 60 caracteres")
	}
	banco := strings.Join(strings.Fields(in.Banco), " ")
	if utf8.RuneCountInString(banco) > 60 {
		return nil, NewValidationError("banco", "deve ter no máximo 60 caracteres")
	}
	if in.DiaFechamento < 1 || in.DiaFechamento > MaxDiaCiclo {
		return nil, NewValidationError("dia_fechamento", "deve estar entre 1 e 31")
	}
	if in.DiaVencimento < 1 || in.DiaVencimento > MaxDiaCiclo {
		return nil, NewValidationError("dia_vencimento", "deve estar entre 1 e 31")
	}
	if in.Limite < 0 {
		return nil, NewValidationError("limite", "não pode ser negativo")
	}
	if in.Limite > MaxMoney {
		return nil, NewValidationError("limite", "valor acima do limite permitido")
	}
	return &Cartao{
		UserID: in.UserID, Nome: nome, Banco: banco,
		DiaFechamento: in.DiaFechamento, DiaVencimento: in.DiaVencimento,
		Limite: in.Limite, Ativo: true,
	}, nil
}

// FechamentoDoMes devolve a data de fechamento no mês de ref (dia ajustado
// para o último dia em meses mais curtos: fechamento 31 em fevereiro cai no 28).
func (c *Cartao) FechamentoDoMes(ref time.Time) time.Time {
	return diaNoMes(ref, c.DiaFechamento)
}

// mesesAteVencer diz em quantos meses depois do fechamento a fatura vence:
// 0 quando o dia de vencimento vem depois do de fechamento (fecha 20, vence 21
// -> mesmo mês), 1 caso contrário (fecha 28, vence 5 -> mês seguinte).
//
// É decidido pelos dias CONFIGURADOS, uma vez, e não recalculado mês a mês.
// Essa é a diferença que importa: um deslocamento fixo garante que cada
// fechamento caia num vencimento só e vice-versa. A regra intuitiva ("a próxima
// ocorrência do dia de vencimento depois do fechamento") parece equivalente,
// mas quebra quando os dias grampeiam em meses curtos — num cartão que fecha 30
// e vence 31, fevereiro empurrava a fatura um mês inteiro e ela colidia com a
// de março, fundindo duas faturas numa.
func (c *Cartao) mesesAteVencer() int {
	if c.DiaVencimento > c.DiaFechamento {
		return 0
	}
	return 1
}

// VencimentoDoFechamento devolve quando vence a fatura que fechou nesta data.
func (c *Cartao) VencimentoDoFechamento(fechamento time.Time) time.Time {
	return diaNoMes(AddMonthsClamped(fechamento, c.mesesAteVencer()), c.DiaVencimento)
}

// FaturaDaCompra devolve o VENCIMENTO da fatura em que a compra cai.
//
// Compra feita DEPOIS do fechamento entra na fatura seguinte — é a regra que
// mais confunde na vida real: quem compra no dia 21 com fechamento no dia 20
// só paga aquilo no mês seguinte.
func (c *Cartao) FaturaDaCompra(dataCompra time.Time) time.Time {
	dataCompra = DateOnly(dataCompra)
	fechamento := c.FechamentoDoMes(dataCompra)
	if dataCompra.After(fechamento) {
		fechamento = c.FechamentoDoMes(AddMonthsClamped(dataCompra, 1))
	}
	return c.VencimentoDoFechamento(fechamento)
}

// MelhorDiaCompra é o dia seguinte ao fechamento: comprando nele você ganha o
// maior prazo possível até o pagamento.
func (c *Cartao) MelhorDiaCompra(ref time.Time) time.Time {
	return c.FechamentoDoMes(ref).AddDate(0, 0, 1)
}

// InicioDoCiclo devolve o primeiro dia coberto pela fatura que vence em
// vencimento — ou seja, o dia seguinte ao fechamento anterior.
func (c *Cartao) InicioDoCiclo(vencimento time.Time) time.Time {
	fim := c.FimDoCiclo(vencimento)
	return c.FechamentoDoMes(AddMonthsClamped(fim, -1)).AddDate(0, 0, 1)
}

// FimDoCiclo devolve o fechamento que originou a fatura que vence em vencimento.
// É o inverso exato de VencimentoDoFechamento — basta voltar os mesmos meses.
func (c *Cartao) FimDoCiclo(vencimento time.Time) time.Time {
	return c.FechamentoDoMes(AddMonthsClamped(DateOnly(vencimento), -c.mesesAteVencer()))
}

// MesmaData compara duas datas pelo calendário, ignorando fuso e hora.
//
// Existe porque as datas chegam de origens diferentes: o banco devolve DATE em
// UTC e o domínio calcula no fuso da aplicação. Nos dois casos é meia-noite,
// mas em instantes diferentes — time.Equal diria que 21/09 é diferente de
// 21/09.
func MesmaData(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// diaNoMes monta a data com o dia pedido no mês de ref, respeitando meses curtos.
func diaNoMes(ref time.Time, dia int) time.Time {
	primeiro := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location())
	return time.Date(ref.Year(), ref.Month(), min(dia, ultimoDia(primeiro)), 0, 0, 0, 0, ref.Location())
}

func ultimoDia(primeiroDoMes time.Time) int {
	return primeiroDoMes.AddDate(0, 1, -1).Day()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
