package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxParcelasCartao limita o parcelamento de uma compra no cartão.
const MaxParcelasCartao = 36

// CompraCartao é UMA PARCELA de uma compra no cartão.
//
// Uma compra em 3x vira três linhas, compartilhando o GrupoID — do mesmo jeito
// que um parcelamento materializa uma transação por mês. Guardar parcela a
// parcela deixa o total da fatura ser uma soma simples por período, em vez de
// expandir compras a cada consulta.
//
// Nada aqui mexe no saldo: a compra é uma dívida com o banco, não uma saída da
// conta. O dinheiro só se move quando a fatura é paga.
type CompraCartao struct {
	ID        string
	UserID    string
	CartaoID  string
	GrupoID   string // mesma compra, parcelas diferentes
	Valor     Money  // valor DESTA parcela
	Categoria string
	Descricao string

	// DataCompra é a data real da compra, igual em todas as parcelas: é ela
	// que diz em qual fatura a 1ª caiu, e é o que o usuário reconhece.
	DataCompra    time.Time
	NumeroParcela int
	TotalParcelas int

	// FaturaVencimento identifica a fatura desta parcela. Fica gravado porque
	// é um fato consumado: mudar o dia de fechamento do cartão amanhã não
	// remaneja compras que já caíram numa fatura fechada.
	FaturaVencimento time.Time
	CreatedAt        time.Time
}

// NewCompraInput agrupa os dados brutos de uma compra no cartão.
type NewCompraInput struct {
	UserID        string
	CartaoID      string
	Valor         Money // valor TOTAL da compra
	Categoria     string
	Descricao     string
	DataCompra    time.Time
	TotalParcelas int // 1 = à vista
}

// NewCompraCartao valida a compra e devolve uma linha por parcela.
//
// O resíduo de centavos vai na 1ª parcela, mesma regra do parcelamento: a soma
// das parcelas bate exatamente com o valor da compra.
func NewCompraCartao(c *Cartao, in NewCompraInput) ([]*CompraCartao, error) {
	if c == nil {
		return nil, NewValidationError("cartao_id", "cartão não encontrado")
	}
	if strings.TrimSpace(in.UserID) == "" {
		return nil, NewValidationError("user_id", "obrigatório")
	}
	if in.TotalParcelas == 0 {
		in.TotalParcelas = 1
	}
	if in.TotalParcelas < 1 || in.TotalParcelas > MaxParcelasCartao {
		return nil, NewValidationError("total_parcelas", "deve estar entre 1 e 36")
	}
	if in.Valor <= 0 {
		return nil, NewValidationError("valor", "deve ser maior que zero")
	}
	if in.Valor > MaxMoney {
		return nil, NewValidationError("valor", "valor acima do limite permitido")
	}
	if in.Valor < Money(in.TotalParcelas) {
		return nil, NewValidationError("valor", "cada parcela deve ser de ao menos R$ 0,01")
	}
	if in.DataCompra.IsZero() {
		return nil, NewValidationError("data", "obrigatória")
	}
	descricao := strings.Join(strings.Fields(in.Descricao), " ")
	if utf8.RuneCountInString(descricao) > 255 {
		return nil, NewValidationError("descricao", "máximo de 255 caracteres")
	}

	data := DateOnly(in.DataCompra)
	primeira := c.FaturaDaCompra(data)
	categoria := NormalizeCategoria(in.Categoria)
	n := in.TotalParcelas
	porParcela := in.Valor / Money(n)
	residuo := in.Valor - porParcela*Money(n)

	out := make([]*CompraCartao, 0, n)
	for i := 0; i < n; i++ {
		valor := porParcela
		if i == 0 {
			valor += residuo
		}
		// O "(1/3)" só entra quando há descrição: sem ela sobraria um título
		// que é só o sufixo. O número da parcela já viaja em campo próprio.
		desc := descricao
		if n > 1 && desc != "" {
			desc = desc + " (" + strconv.Itoa(i+1) + "/" + strconv.Itoa(n) + ")"
		}
		out = append(out, &CompraCartao{
			UserID: in.UserID, CartaoID: c.ID, Valor: valor,
			Categoria: categoria, Descricao: desc, DataCompra: data,
			NumeroParcela: i + 1, TotalParcelas: n,
			FaturaVencimento: AddMonthsClamped(primeira, i),
		})
	}
	return out, nil
}

// StatusFatura é o estágio da fatura em relação a hoje.
type StatusFatura string

const (
	// FaturaAberta ainda acumula compras (não chegou no fechamento).
	FaturaAberta StatusFatura = "aberta"
	// FaturaFechada passou do fechamento e espera pagamento.
	FaturaFechada StatusFatura = "fechada"
	// FaturaParcial recebeu pagamento, mas não o suficiente para quitá-la.
	FaturaParcial StatusFatura = "parcial"
	// FaturaPaga está quitada: os pagamentos cobrem o total.
	FaturaPaga StatusFatura = "paga"
)

// PagamentoFatura registra que uma fatura foi paga, ligando-a à transação de
// saída criada. É o único fato da fatura que precisa ser gravado — o resto é
// derivado das compras.
type PagamentoFatura struct {
	ID          string
	UserID      string
	CartaoID    string
	Vencimento  time.Time
	TransacaoID string
	Valor       Money
	PagoEm      time.Time
	CreatedAt   time.Time
}

// FaturaResumo é o total de uma fatura e o pagamento dela, SEM carregar as
// compras. É o que a lista de faturas e a Home precisam — e cabe numa consulta
// agregada só, em vez de duas por fatura.
type FaturaResumo struct {
	Vencimento time.Time
	Total      Money
	// Pago é a soma dos pagamentos: a fatura aceita quantos precisar, porque
	// pagar metade agora e metade depois é comum.
	Pago Money
	// UltimoPagamentoEm é zero quando não houve pagamento nenhum.
	UltimoPagamentoEm time.Time
	// UltimoPagamentoValor é quanto foi o pagamento mais recente. Desfazer
	// mexe só nele, então a tela precisa saber o valor sem carregar a lista.
	UltimoPagamentoValor Money
}

// Fatura é a visão de um ciclo do cartão. Derivada, nunca armazenada: assim
// não tem como ficar dessincronizada das compras.
type Fatura struct {
	CartaoID    string
	Competencia string // "2026-10", o mês do vencimento
	Vencimento  time.Time
	InicioCiclo time.Time
	FimCiclo    time.Time
	Total       Money
	// Pago é a soma dos pagamentos já registrados.
	Pago    Money
	Compras []*CompraCartao
	Status  StatusFatura
	// Pagamentos vem vazio no resumo (a Home não precisa da lista).
	Pagamentos []*PagamentoFatura
	// UltimoPagamentoEm é zero quando a fatura não tem pagamento.
	UltimoPagamentoEm time.Time
	// UltimoPagamentoValor é o valor do pagamento mais recente.
	UltimoPagamentoValor Money
}

// Restante é o que ainda falta pagar. Nunca negativo: o banco pode cobrar mais
// que o total da fatura, e isso não vira crédito aqui.
func (f *Fatura) Restante() Money {
	if falta := f.Total - f.Pago; falta > 0 {
		return falta
	}
	return 0
}

// Quitada diz se os pagamentos cobrem o total.
func (f *Fatura) Quitada() bool { return f.Pago > 0 && f.Pago >= f.Total }

// CompetenciaDe formata o mês do vencimento: é como a fatura é referenciada
// na API (`/faturas/2026-10`).
func CompetenciaDe(vencimento time.Time) string { return vencimento.Format("2006-01") }

// ParseCompetencia lê "2026-10" e devolve o primeiro dia do mês.
func ParseCompetencia(ref string) (time.Time, error) {
	t, err := time.Parse("2006-01", strings.TrimSpace(ref))
	if err != nil {
		return time.Time{}, NewValidationError("competencia", "use o formato AAAA-MM")
	}
	return t, nil
}

// MontarDoResumo monta a fatura a partir do agregado, sem a lista de compras.
func MontarDoResumo(c *Cartao, r FaturaResumo, hoje time.Time) *Fatura {
	f := MontarFatura(c, r.Vencimento, nil, nil, hoje)
	f.Total = r.Total
	f.Pago = r.Pago
	f.UltimoPagamentoEm = r.UltimoPagamentoEm
	f.UltimoPagamentoValor = r.UltimoPagamentoValor
	f.definirStatus(hoje)
	return f
}

// MontarFatura junta cartão, compras e pagamentos numa fatura.
func MontarFatura(c *Cartao, vencimento time.Time, compras []*CompraCartao, pagamentos []*PagamentoFatura, hoje time.Time) *Fatura {
	vencimento = DateOnly(vencimento)
	f := &Fatura{
		CartaoID:    c.ID,
		Competencia: CompetenciaDe(vencimento),
		Vencimento:  vencimento,
		InicioCiclo: c.InicioDoCiclo(vencimento),
		FimCiclo:    c.FimDoCiclo(vencimento),
		Compras:     compras,
		Pagamentos:  pagamentos,
	}
	for _, compra := range compras {
		f.Total += compra.Valor
	}
	for _, pag := range pagamentos {
		f.Pago += pag.Valor
		// A lista vem em ordem de pagamento: o último é o que "desfazer" tira.
		if !pag.PagoEm.Before(f.UltimoPagamentoEm) {
			f.UltimoPagamentoEm = pag.PagoEm
			f.UltimoPagamentoValor = pag.Valor
		}
	}
	f.definirStatus(hoje)
	return f
}

// AtualizarStatus recalcula o estágio depois de mexer nos pagamentos.
func (f *Fatura) AtualizarStatus(hoje time.Time) { f.definirStatus(hoje) }

// definirStatus decide o estágio da fatura. Pagamento que não cobre o total
// deixa a fatura em "parcial": ela continua sendo uma conta a pagar, só que
// menor.
func (f *Fatura) definirStatus(hoje time.Time) {
	switch {
	case f.Quitada():
		f.Status = FaturaPaga
	case f.Pago > 0:
		f.Status = FaturaParcial
	case !DateOnly(hoje).After(f.FimCiclo):
		f.Status = FaturaAberta
	default:
		f.Status = FaturaFechada
	}
}

// ResumoCartao é o que a Home e a lista de cartões mostram: as duas faturas
// que convivem em qualquer mês, mais o limite que sobrou.
type ResumoCartao struct {
	Cartao *Cartao
	// APagar é a fatura fechada mais antiga ainda não paga (nil se não há).
	APagar *Fatura
	// EmAberto é a fatura que ainda está acumulando compras.
	EmAberto *Fatura
	// LimiteDisponivel = limite - tudo que ainda não foi pago. Zero quando o
	// cartão não tem limite informado.
	LimiteDisponivel Money
	MelhorDiaCompra  time.Time
}

// CalcularLimiteDisponivel desconta do limite tudo que ainda não foi pago.
// Nunca devolve negativo: estourou o limite, mostra zero.
func CalcularLimiteDisponivel(limite, comprometido Money) Money {
	if limite <= 0 {
		return 0
	}
	if disponivel := limite - comprometido; disponivel > 0 {
		return disponivel
	}
	return 0
}
