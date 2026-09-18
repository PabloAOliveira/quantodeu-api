package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// TipoParcelamento diferencia compras parceladas de despesas recorrentes.
type TipoParcelamento string

const (
	// ParcelamentoParcelado: um valor total dividido em N parcelas
	// (compra no cartão, financiamento).
	ParcelamentoParcelado TipoParcelamento = "parcelado"
	// ParcelamentoRecorrente: um valor fixo repetido por N meses
	// (aluguel, assinatura, mensalidade).
	ParcelamentoRecorrente TipoParcelamento = "recorrente"
)

// Valid informa se o tipo é conhecido.
func (t TipoParcelamento) Valid() bool {
	return t == ParcelamentoParcelado || t == ParcelamentoRecorrente
}

// MaxParcelas limita a quantidade de parcelas (35 anos de financiamento).
const MaxParcelas = 420

// Parcelamento é uma despesa de longo prazo que gera uma saída por mês.
type Parcelamento struct {
	ID                  string
	UserID              string
	Tipo                TipoParcelamento
	Descricao           string
	Categoria           string
	ValorTotal          Money
	ValorParcela        Money // valor das parcelas 2..N (a 1ª absorve centavos residuais)
	TotalParcelas       int
	DataPrimeiraParcela time.Time
	CreatedAt           time.Time
	// ParcelasJaPagas são as N primeiras parcelas quitadas ANTES do cadastro,
	// fora do app (quem registra um financiamento que começou meses atrás).
	// Elas seguem no cronograma — definem o valor total e contam no progresso
	// como pagas —, mas não viram lançamento: o saldo informado no cadastro já
	// está descontado delas, e gerá-las cobraria o mesmo dinheiro duas vezes.
	ParcelasJaPagas int
	// Financiamento, quando presente, projeta as parcelas por juros
	// (Price ou SAC) em vez de dividir o valor total em partes iguais.
	Financiamento *Financiamento
	// Progresso é calculado na leitura (parcelas com data até hoje contam
	// como pagas). Nil quando não calculado.
	Progresso *ProgressoParcelamento
}

// ProgressoParcelamento resume o andamento em relação a uma data de referência.
type ProgressoParcelamento struct {
	ParcelasPagas     int
	ParcelasRestantes int
	ValorPago         Money
	ValorRestante     Money
	ProximaParcela    *time.Time // nil quando quitado
}

// CalcularProgresso soma as parcelas reais (inclusive ajustes manuais) com
// data de competência até `hoje` como pagas.
func CalcularProgresso(parcelas []*Transacao, hoje time.Time) *ProgressoParcelamento {
	hoje = DateOnly(hoje)
	pr := &ProgressoParcelamento{}
	for _, t := range parcelas {
		d := DateOnly(t.Data)
		if !d.After(hoje) {
			pr.ParcelasPagas++
			pr.ValorPago += t.Valor
			continue
		}
		pr.ParcelasRestantes++
		pr.ValorRestante += t.Valor
		if pr.ProximaParcela == nil || d.Before(*pr.ProximaParcela) {
			dd := d
			pr.ProximaParcela = &dd
		}
	}
	return pr
}

// NewParcelamentoInput agrupa os dados brutos de cadastro.
//
// Para "parcelado" informe ValorTotal; para "recorrente" informe ValorParcela.
// Se ambos vierem, ValorTotal prevalece para "parcelado" e ValorParcela para
// "recorrente".
type NewParcelamentoInput struct {
	UserID              string
	Tipo                TipoParcelamento
	Descricao           string
	Categoria           string
	ValorTotal          Money
	ValorParcela        Money
	TotalParcelas       int
	DataPrimeiraParcela time.Time
	// ParcelasJaPagas não gera lançamento para as N primeiras parcelas
	// (já pagas fora do app). Ver Parcelamento.ParcelasJaPagas.
	ParcelasJaPagas int
	// Financiamento (opcional) substitui ValorTotal: o valor de cada parcela
	// é calculado a partir do contrato.
	Financiamento *Financiamento
}

// NewParcelamento valida os dados e calcula valores.
func NewParcelamento(in NewParcelamentoInput) (*Parcelamento, error) {
	if strings.TrimSpace(in.UserID) == "" {
		return nil, NewValidationError("user_id", "obrigatório")
	}
	if in.Tipo == "" {
		in.Tipo = ParcelamentoParcelado
	}
	if !in.Tipo.Valid() {
		return nil, NewValidationError("tipo", "deve ser 'parcelado' ou 'recorrente'")
	}
	desc := strings.Join(strings.Fields(in.Descricao), " ")
	if n := utf8.RuneCountInString(desc); n < 1 || n > 255 {
		return nil, NewValidationError("descricao", "deve ter entre 1 e 255 caracteres")
	}
	if in.TotalParcelas < 1 || in.TotalParcelas > MaxParcelas {
		return nil, NewValidationError("total_parcelas", "deve estar entre 1 e 420")
	}
	if in.DataPrimeiraParcela.IsZero() {
		return nil, NewValidationError("data_primeira_parcela", "obrigatória")
	}
	if in.ParcelasJaPagas < 0 {
		return nil, NewValidationError("parcelas_ja_pagas", "não pode ser negativo")
	}
	if in.ParcelasJaPagas >= in.TotalParcelas {
		return nil, NewValidationError("parcelas_ja_pagas", "deve ser menor que o total de parcelas")
	}

	p := &Parcelamento{
		UserID:              in.UserID,
		Tipo:                in.Tipo,
		Descricao:           desc,
		Categoria:           NormalizeCategoria(in.Categoria),
		TotalParcelas:       in.TotalParcelas,
		DataPrimeiraParcela: DateOnly(in.DataPrimeiraParcela),
		ParcelasJaPagas:     in.ParcelasJaPagas,
	}

	// Financiamento: as parcelas vêm da tabela de amortização.
	if in.Financiamento != nil {
		if in.Tipo != ParcelamentoParcelado {
			return nil, NewValidationError("tipo", "financiamento só vale para o tipo 'parcelado'")
		}
		fin := *in.Financiamento
		if err := fin.Validate(); err != nil {
			return nil, err
		}
		fin.PrazoContratado = in.TotalParcelas
		cron, err := fin.Cronograma(in.TotalParcelas, p.DataPrimeiraParcela)
		if err != nil {
			return nil, err
		}
		total, _ := TotaisCronograma(cron)
		if total > MaxMoney {
			return nil, NewValidationError("financiamento.valor_financiado", "valor total acima do limite permitido")
		}
		p.Financiamento = &fin
		p.ValorTotal = total
		p.ValorParcela = cron[0].Valor
		// Com amortização extraordinária o contrato quita antes: o parcelamento
		// passa a ter as parcelas que realmente existirão, e o prazo do contrato
		// fica guardado em Financiamento.PrazoContratado.
		p.TotalParcelas = len(cron)
		// Com aporte o contrato quita antes: o limite passa a ser o prazo real.
		if p.ParcelasJaPagas >= p.TotalParcelas {
			return nil, NewValidationError("parcelas_ja_pagas", "deve ser menor que o total de parcelas")
		}
		return p, nil
	}

	n := Money(in.TotalParcelas)
	switch in.Tipo {
	case ParcelamentoParcelado:
		if in.ValorTotal <= 0 {
			return nil, NewValidationError("valor_total", "obrigatório e maior que zero para parcelado")
		}
		if in.ValorTotal > MaxMoney {
			return nil, NewValidationError("valor_total", "valor acima do limite permitido")
		}
		if in.ValorTotal < n {
			return nil, NewValidationError("valor_total", "cada parcela deve ser de ao menos R$ 0,01")
		}
		p.ValorTotal = in.ValorTotal
		p.ValorParcela = in.ValorTotal / n
	case ParcelamentoRecorrente:
		if in.ValorParcela <= 0 {
			return nil, NewValidationError("valor_parcela", "obrigatório e maior que zero para recorrente")
		}
		if in.ValorParcela > MaxMoney/n {
			return nil, NewValidationError("valor_parcela", "valor total acima do limite permitido")
		}
		p.ValorParcela = in.ValorParcela
		p.ValorTotal = in.ValorParcela * n
	}
	return p, nil
}

// GerarParcelas materializa as parcelas que ainda vão sair da conta: o
// cronograma inteiro, menos as ParcelasJaPagas quitadas antes do cadastro.
//
// A numeração é a do contrato ("7/360"), não a da lista: quem cadastrou um
// financiamento no meio continua vendo o número real da parcela.
func (p *Parcelamento) GerarParcelas() []*Transacao {
	cron := p.cronograma()
	if p.ParcelasJaPagas <= 0 {
		return cron
	}
	if p.ParcelasJaPagas >= len(cron) {
		return nil
	}
	return cron[p.ParcelasJaPagas:]
}

// ValorJaPago soma as parcelas quitadas antes do cadastro. Elas não existem
// como lançamento, então o valor sai do cronograma.
func (p *Parcelamento) ValorJaPago() Money {
	if p == nil || p.ParcelasJaPagas <= 0 {
		return 0
	}
	cron := p.cronograma()
	var total Money
	for i := 0; i < p.ParcelasJaPagas && i < len(cron); i++ {
		total += cron[i].Valor
	}
	return total
}

// AplicarJaPagas acrescenta ao progresso as parcelas pagas antes do cadastro.
func (p *Parcelamento) AplicarJaPagas(pr *ProgressoParcelamento) {
	if p == nil || pr == nil || p.ParcelasJaPagas <= 0 {
		return
	}
	pr.ParcelasPagas += p.ParcelasJaPagas
	pr.ValorPago += p.ValorJaPago()
}

// ProgressoEm calcula o progresso das parcelas reais e soma as que já estavam
// pagas quando o parcelamento foi cadastrado.
func (p *Parcelamento) ProgressoEm(parcelas []*Transacao, hoje time.Time) *ProgressoParcelamento {
	pr := CalcularProgresso(parcelas, hoje)
	p.AplicarJaPagas(pr)
	return pr
}

// cronograma devolve TODAS as parcelas do contrato, inclusive as já pagas
// antes do cadastro. A primeira recebe os centavos residuais da divisão para
// que a soma bata exatamente com ValorTotal.
func (p *Parcelamento) cronograma() []*Transacao {
	n := p.TotalParcelas
	if p.Financiamento != nil {
		// O cronograma é montado com o prazo CONTRATADO (é ele que define a
		// parcela base); o número de parcelas geradas pode ser menor.
		cron, err := p.Financiamento.Cronograma(p.Financiamento.PrazoBase(n), p.DataPrimeiraParcela)
		if err != nil {
			return nil
		}
		n = len(cron)
		out := make([]*Transacao, 0, n)
		for _, c := range cron {
			num := c.Numero
			out = append(out, &Transacao{
				UserID:        p.UserID,
				Tipo:          TipoSaida,
				Valor:         c.Valor,
				Categoria:     p.Categoria,
				Descricao:     p.Descricao + " (" + strconv.Itoa(num) + "/" + strconv.Itoa(n) + ")",
				Data:          c.Data,
				Origem:        OrigemParcelamento,
				NumeroParcela: &num,
			})
		}
		return out
	}
	residuo := p.ValorTotal - p.ValorParcela*Money(n)
	out := make([]*Transacao, 0, n)
	for i := 0; i < n; i++ {
		valor := p.ValorParcela
		if i == 0 {
			valor += residuo
		}
		num := i + 1
		out = append(out, &Transacao{
			UserID:        p.UserID,
			Tipo:          TipoSaida,
			Valor:         valor,
			Categoria:     p.Categoria,
			Descricao:     p.Descricao + " (" + strconv.Itoa(num) + "/" + strconv.Itoa(n) + ")",
			Data:          AddMonthsClamped(p.DataPrimeiraParcela, i),
			Origem:        OrigemParcelamento,
			NumeroParcela: &num,
		})
	}
	return out
}
