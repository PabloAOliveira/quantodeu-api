package domain

// TotaisPeriodo agrega valores de um intervalo (calculados no repositório).
type TotaisPeriodo struct {
	Entradas         Money
	Saidas           Money
	Rendimentos      Money // entradas com categoria "rendimentos"
	SaidasParceladas Money // saídas geradas por parcelamentos
	QtdTransacoes    int64
}

// TotalCategoria agrega valores por categoria/tipo.
type TotalCategoria struct {
	Categoria string
	Tipo      TipoTransacao
	Total     Money
	Qtd       int64
}

// Resumo é a visão consolidada de um mês.
type Resumo struct {
	Periodo          Periodo
	Receitas         Money
	Despesas         Money
	ResultadoMes     Money // Receitas - Despesas
	SaldoGeral       Money // saldo inicial + tudo lançado até o fim do mês
	SaldoAtual       Money // saldo inicial + tudo lançado até hoje
	CustosParcelados Money
	Parcelas         []*Transacao
	PorCategoria     []TotalCategoria
}

// Perfil é a visão do usuário logado exibida em /me.
type Perfil struct {
	User        *User
	SaldoAtual  Money
	Periodo     Periodo
	EntradasMes Money
	SaidasMes   Money
	Rendimentos Money
}
