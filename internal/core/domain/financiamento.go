package domain

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Financiamento — projeção das parcelas a partir de juros
//
// Usado quando o usuário não sabe o valor da parcela, mas conhece o contrato:
// valor financiado, prazo, taxa de juros e sistema de amortização.
// ---------------------------------------------------------------------------

// SistemaAmortizacao define como o saldo devedor é amortizado.
type SistemaAmortizacao string

const (
	// SistemaPrice (Tabela Price / francês): parcela constante; a parte de
	// juros cai e a de amortização sobe ao longo do tempo.
	SistemaPrice SistemaAmortizacao = "price"
	// SistemaSAC: amortização constante; a parcela começa maior e vai
	// diminuindo. É o sistema mais usado em financiamento imobiliário no
	// Brasil (Caixa, Minha Casa Minha Vida).
	SistemaSAC SistemaAmortizacao = "sac"
)

// Valid informa se o sistema é conhecido.
func (s SistemaAmortizacao) Valid() bool { return s == SistemaPrice || s == SistemaSAC }

// TipoTaxa define como a taxa anual vira taxa mensal.
type TipoTaxa string

const (
	// TaxaEfetiva: juros compostos equivalentes — i_mensal = (1+i_anual)^(1/12)-1.
	// É como os bancos divulgam a "taxa efetiva a.a." dos contratos imobiliários.
	TaxaEfetiva TipoTaxa = "efetiva"
	// TaxaNominal: divisão simples — i_mensal = i_anual/12 (capitalização mensal).
	// É a "taxa nominal a.a." que aparece em muitos contratos.
	TaxaNominal TipoTaxa = "nominal"
)

// Valid informa se o tipo de taxa é conhecido.
func (t TipoTaxa) Valid() bool { return t == TaxaEfetiva || t == TaxaNominal }

// MaxTaxaJurosAnual limita a taxa anual aceita (%) — evita entradas absurdas.
const MaxTaxaJurosAnual = 100.0

// Financiamento são os dados do contrato usados para projetar as parcelas.
type Financiamento struct {
	Banco           string             // livre: "Caixa Econômica Federal", "Itaú"...
	Sistema         SistemaAmortizacao // price | sac
	ValorFinanciado Money              // valor do contrato (já descontada a entrada)
	TaxaAnual       float64            // em % ao ano (ex.: 8.66)
	TipoTaxa        TipoTaxa           // efetiva | nominal

	// ExtraMensal é a amortização extraordinária paga todo mês, somada à
	// parcela. Abate o saldo devedor mais rápido: o prazo encurta e o total de
	// juros cai. Zero significa "pago só a parcela do contrato".
	ExtraMensal Money

	// PrazoContratado é o número de meses do contrato. Com ExtraMensal > 0 o
	// financiamento quita antes, e o parcelamento passa a ter menos parcelas
	// que isto — mas o CRONOGRAMA precisa do prazo original, porque é ele que
	// define a parcela base (no SAC, valor_financiado ÷ prazo contratado).
	// Zero mantém o comportamento antigo: usa o prazo pedido na chamada.
	PrazoContratado int
}

// PrazoBase devolve o prazo que define a parcela do contrato.
func (f Financiamento) PrazoBase(fallback int) int {
	if f.PrazoContratado > 0 {
		return f.PrazoContratado
	}
	return fallback
}

// Validate valida os dados do contrato.
func (f *Financiamento) Validate() error {
	f.Banco = strings.Join(strings.Fields(f.Banco), " ")
	if utf8.RuneCountInString(f.Banco) > 120 {
		return NewValidationError("financiamento.banco", "deve ter no máximo 120 caracteres")
	}
	if f.Sistema == "" {
		f.Sistema = SistemaSAC
	}
	if !f.Sistema.Valid() {
		return NewValidationError("financiamento.sistema", "deve ser 'price' ou 'sac'")
	}
	if f.TipoTaxa == "" {
		f.TipoTaxa = TaxaEfetiva
	}
	if !f.TipoTaxa.Valid() {
		return NewValidationError("financiamento.tipo_taxa", "deve ser 'efetiva' ou 'nominal'")
	}
	if f.ValorFinanciado <= 0 {
		return NewValidationError("financiamento.valor_financiado", "obrigatório e maior que zero")
	}
	if f.ValorFinanciado > MaxMoney {
		return NewValidationError("financiamento.valor_financiado", "valor acima do limite permitido")
	}
	if f.TaxaAnual < 0 || f.TaxaAnual > MaxTaxaJurosAnual {
		return NewValidationError("financiamento.taxa_juros_anual", "deve estar entre 0 e 100 (% ao ano)")
	}
	if math.IsNaN(f.TaxaAnual) || math.IsInf(f.TaxaAnual, 0) {
		return NewValidationError("financiamento.taxa_juros_anual", "valor inválido")
	}
	if f.ExtraMensal < 0 {
		return NewValidationError("financiamento.pagamento_extra_mensal", "não pode ser negativo")
	}
	if f.ExtraMensal > MaxMoney {
		return NewValidationError("financiamento.pagamento_extra_mensal", "valor acima do limite permitido")
	}
	return nil
}

// TaxaMensal devolve a taxa mensal em fração (0.0069 = 0,69% a.m.).
func (f Financiamento) TaxaMensal() float64 {
	a := f.TaxaAnual / 100
	if a <= 0 {
		return 0
	}
	if f.TipoTaxa == TaxaNominal {
		return a / 12
	}
	return math.Pow(1+a, 1.0/12) - 1
}

// ParcelaProjetada é uma linha da tabela de amortização.
type ParcelaProjetada struct {
	Numero       int
	Data         time.Time
	Valor        Money // juros + amortização
	Juros        Money
	Amortizacao  Money
	SaldoDevedor Money // saldo APÓS pagar esta parcela
}

// Cronograma monta a tabela de amortização completa.
//
// Todo o cálculo é feito em centavos (int64): os juros de cada mês são
// arredondados para o centavo e a ÚLTIMA parcela absorve a diferença, de modo
// que o saldo devedor termine exatamente em zero.
//
// Com ExtraMensal > 0, o valor é somado à amortização de cada parcela e o
// contrato quita ANTES de totalParcelas — a fatia devolvida tem menos linhas
// que o prazo contratado — PrazoQuitacao devolve os dois números.
func (f Financiamento) Cronograma(totalParcelas int, primeira time.Time) ([]ParcelaProjetada, error) {
	if totalParcelas < 1 || totalParcelas > MaxParcelas {
		return nil, NewValidationError("total_parcelas", "deve estar entre 1 e 420")
	}
	if f.ValorFinanciado < Money(totalParcelas) {
		return nil, NewValidationError("financiamento.valor_financiado", "cada parcela deve ser de ao menos R$ 0,01")
	}
	primeira = DateOnly(primeira)
	i := f.TaxaMensal()
	n := totalParcelas
	saldo := f.ValorFinanciado

	extra := f.ExtraMensal
	if extra < 0 {
		extra = 0
	}

	out := make([]ParcelaProjetada, 0, n)
	add := func(k int, juros, amort Money) {
		saldo -= amort
		if saldo < 0 {
			saldo = 0
		}
		out = append(out, ParcelaProjetada{
			Numero: k + 1, Data: AddMonthsClamped(primeira, k),
			Valor: juros + amort, Juros: juros, Amortizacao: amort, SaldoDevedor: saldo,
		})
	}

	// aplicaExtra soma o aporte e trava a amortização no saldo restante, para
	// a última parcela nunca cobrar mais do que se deve.
	aplicaExtra := func(amort Money) Money {
		amort += extra
		if amort > saldo {
			amort = saldo
		}
		return amort
	}

	switch {
	case i == 0: // sem juros: divide o valor, sobra vai na primeira parcela
		base := f.ValorFinanciado / Money(n)
		resto := f.ValorFinanciado - base*Money(n)
		for k := 0; k < n && saldo > 0; k++ {
			amort := base
			if k == 0 {
				amort += resto
			}
			if k == n-1 {
				amort = saldo
			}
			add(k, 0, aplicaExtra(amort))
		}

	case f.Sistema == SistemaSAC:
		base := f.ValorFinanciado / Money(n)
		resto := f.ValorFinanciado - base*Money(n)
		for k := 0; k < n && saldo > 0; k++ {
			juros := arredondaCentavos(float64(saldo) * i)
			amort := base
			if k == 0 {
				amort += resto
			}
			if k == n-1 || amort > saldo {
				amort = saldo // última parcela quita o saldo
			}
			add(k, juros, aplicaExtra(amort))
		}

	default: // Price: parcela constante
		pv := float64(f.ValorFinanciado)
		fator := math.Pow(1+i, float64(n))
		pmt := arredondaCentavos(pv * i * fator / (fator - 1))
		if pmt <= 0 {
			return nil, NewValidationError("financiamento.taxa_juros_anual", "não foi possível calcular a parcela com esses valores")
		}
		for k := 0; k < n && saldo > 0; k++ {
			juros := arredondaCentavos(float64(saldo) * i)
			amort := pmt - juros
			if amort < 0 {
				// Só ocorreria com prazo longuíssimo e taxa altíssima.
				return nil, NewValidationError("financiamento.taxa_juros_anual", "os juros do mês superam a parcela; revise taxa e prazo")
			}
			if k == n-1 || amort > saldo {
				amort = saldo // última parcela quita o saldo
			}
			add(k, juros, aplicaExtra(amort))
		}
	}
	return out, nil
}

// PrazoQuitacao devolve em quantas parcelas o contrato quita com o aporte
// extra e quantas teria sem ele. Serve para o app mostrar a economia.
func (f Financiamento) PrazoQuitacao(totalParcelas int, primeira time.Time) (comExtra, contratado int, err error) {
	parcelas, err := f.Cronograma(totalParcelas, primeira)
	if err != nil {
		return 0, 0, err
	}
	return len(parcelas), totalParcelas, nil
}

// TotaisCronograma soma o total pago e o total de juros de uma projeção.
func TotaisCronograma(parcelas []ParcelaProjetada) (total, juros Money) {
	for _, p := range parcelas {
		total += p.Valor
		juros += p.Juros
	}
	return total, juros
}

// arredondaCentavos arredonda meio para cima (padrão financeiro).
func arredondaCentavos(v float64) Money {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return Money(math.Round(v))
}
