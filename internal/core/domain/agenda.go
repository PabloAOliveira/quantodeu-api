package domain

import "time"

// TipoCompromisso separa o que vence: uma parcela de parcelamento/financiamento
// ou a fatura de um cartão.
type TipoCompromisso string

const (
	CompromissoParcela TipoCompromisso = "parcela"
	CompromissoFatura  TipoCompromisso = "fatura"
)

// Compromisso é uma conta com data marcada para os próximos meses.
//
// Existe para o app agendar o aviso no aparelho: são notificações locais, e
// sem isto ele teria que pedir o resumo de cada mês e as faturas de cada
// cartão só para descobrir as datas.
type Compromisso struct {
	Tipo  TipoCompromisso
	Data  time.Time
	Valor Money
	// Titulo é o que aparece na notificação ("Academia", "Nubank").
	Titulo    string
	Categoria string

	// Parcela: de onde veio e em que ponto da série está.
	ParcelamentoID string
	NumeroParcela  int
	TotalParcelas  int

	// Fatura: o cartão e a competência ("2026-10").
	CartaoID    string
	Competencia string
}

// Agenda é a lista de compromissos ordenada por data.
type Agenda struct {
	Inicio time.Time
	Fim    time.Time
	Itens  []Compromisso
}

// MesesAgendaPadrao é o horizonte usado quando o cliente não pede outro. Seis
// meses cobrem folgado o intervalo entre duas aberturas do app sem encher o
// limite de alarmes do Android.
const MesesAgendaPadrao = 6

// MesesAgendaMax limita o horizonte: agendar dois anos de parcelas de um
// financiamento de 360 meses estouraria o limite de alarmes do sistema.
const MesesAgendaMax = 12
