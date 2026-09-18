package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// Paginação.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// financasBase concentra dependências comuns aos serviços financeiros.
type financasBase struct {
	users         ports.UserRepository
	transacoes    ports.TransacaoRepository
	parcelamentos ports.ParcelamentoRepository
	clock         ports.Clock
	metrics       ports.Metrics
	loc           *time.Location
	log           *slog.Logger
}

// TransacaoService implementa ports.TransacaoUseCase.
type TransacaoService struct{ *financasBase }

// ResumoService implementa ports.ResumoUseCase e ports.ProfileUseCase.
type ResumoService struct{ *financasBase }

// ParcelamentoService implementa ports.ParcelamentoUseCase.
type ParcelamentoService struct{ *financasBase }

var (
	_ ports.TransacaoUseCase    = (*TransacaoService)(nil)
	_ ports.ResumoUseCase       = (*ResumoService)(nil)
	_ ports.ProfileUseCase      = (*ResumoService)(nil)
	_ ports.ParcelamentoUseCase = (*ParcelamentoService)(nil)
)

// FinancasServices agrupa os casos de uso financeiros.
type FinancasServices struct {
	Transacoes    *TransacaoService
	Resumo        *ResumoService
	Parcelamentos *ParcelamentoService
}

// NewFinancasServices cria os serviços. loc define o fuso usado para "hoje" e
// para as fronteiras de mês (ex.: America/Sao_Paulo).
func NewFinancasServices(
	users ports.UserRepository,
	transacoes ports.TransacaoRepository,
	parcelamentos ports.ParcelamentoRepository,
	clock ports.Clock,
	metrics ports.Metrics,
	loc *time.Location,
	log *slog.Logger,
) FinancasServices {
	if loc == nil {
		loc = time.UTC
	}
	if metrics == nil {
		metrics = NoopMetrics{}
	}
	b := &financasBase{users: users, transacoes: transacoes, parcelamentos: parcelamentos, clock: clock, metrics: metrics, loc: loc, log: log}
	return FinancasServices{
		Transacoes:    &TransacaoService{b},
		Resumo:        &ResumoService{b},
		Parcelamentos: &ParcelamentoService{b},
	}
}

func (s *financasBase) hoje() time.Time { return domain.DateOnly(s.clock.Now().In(s.loc)) }

// ---------------------------------------------------------------------------
// Transações
// ---------------------------------------------------------------------------

// Create registra um lançamento manual.
func (s *TransacaoService) Create(ctx context.Context, userID string, in ports.CreateTransacaoInput) (*domain.Transacao, error) {
	data := s.hoje()
	if in.Data != nil {
		y, m, d := in.Data.Date()
		data = time.Date(y, m, d, 0, 0, 0, 0, s.loc)
	}
	t, err := domain.NewTransacao(domain.NewTransacaoInput{
		UserID: userID, Tipo: in.Tipo, Valor: in.Valor, Categoria: in.Categoria,
		Descricao: in.Descricao, Data: data, Origem: domain.OrigemManual,
	})
	if err != nil {
		return nil, err
	}
	if err := s.transacoes.Create(ctx, userID, t); err != nil {
		return nil, fmt.Errorf("criar transação: %w", err)
	}
	s.metrics.TransacaoRegistrada(string(t.Origem), string(t.Tipo))
	return t, nil
}

// Get devolve um lançamento do usuário (ErrNotFound se for de outro usuário).
func (s *TransacaoService) Get(ctx context.Context, userID, id string) (*domain.Transacao, error) {
	return s.transacoes.FindByID(ctx, userID, id)
}

// Update substitui os campos editáveis de um lançamento.
func (s *TransacaoService) Update(ctx context.Context, userID, id string, in ports.UpdateTransacaoInput) (*domain.Transacao, error) {
	t, err := s.transacoes.FindByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	data := t.Data
	if in.Data != nil {
		y, m, d := in.Data.Date()
		data = time.Date(y, m, d, 0, 0, 0, 0, s.loc)
	}
	if err := t.Aplicar(domain.UpdateTransacaoInput{
		Tipo: in.Tipo, Valor: in.Valor, Categoria: in.Categoria, Descricao: in.Descricao, Data: data,
	}); err != nil {
		return nil, err
	}
	if err := s.transacoes.Update(ctx, userID, t); err != nil {
		return nil, fmt.Errorf("atualizar transação: %w", err)
	}
	return t, nil
}

// Delete remove um lançamento avulso. Parcelas são geridas pelo parcelamento.
func (s *TransacaoService) Delete(ctx context.Context, userID, id string) error {
	t, err := s.transacoes.FindByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if t.ParcelamentoID != nil {
		return domain.ErrParcelaGerenciada
	}
	return s.transacoes.Delete(ctx, userID, id)
}

// List lista lançamentos do mês com filtros e paginação.
func (s *TransacaoService) List(ctx context.Context, userID string, in ports.ListTransacoesInput) (*ports.ListTransacoesOutput, error) {
	hoje := s.hoje()
	if in.Ano == 0 {
		in.Ano = hoje.Year()
	}
	if in.Mes == 0 {
		in.Mes = int(hoje.Month())
	}
	periodo, err := domain.NewPeriodo(in.Ano, in.Mes, s.loc)
	if err != nil {
		return nil, err
	}
	if in.Tipo != "" && !in.Tipo.Valid() {
		return nil, domain.NewValidationError("tipo", "deve ser 'entrada' ou 'saida'")
	}
	if in.Page < 1 {
		in.Page = 1
	}
	if in.PageSize < 1 {
		in.PageSize = DefaultPageSize
	}
	if in.PageSize > MaxPageSize {
		in.PageSize = MaxPageSize
	}
	categoria := ""
	if strings.TrimSpace(in.Categoria) != "" {
		categoria = domain.NormalizeCategoria(in.Categoria)
	}

	items, total, err := s.transacoes.List(ctx, userID, ports.TransacaoFilter{
		Inicio: periodo.Inicio, Fim: periodo.Fim, Categoria: categoria, Tipo: in.Tipo,
		Limit: in.PageSize, Offset: (in.Page - 1) * in.PageSize,
	})
	if err != nil {
		return nil, fmt.Errorf("listar transações: %w", err)
	}
	return &ports.ListTransacoesOutput{Items: items, Total: total, Page: in.Page, PageSize: in.PageSize, Periodo: periodo}, nil
}

// ---------------------------------------------------------------------------
// Resumo e Perfil
// ---------------------------------------------------------------------------

// GetResumo consolida o mês solicitado (padrão: mês corrente).
func (s *ResumoService) GetResumo(ctx context.Context, userID string, ano, mes int) (*domain.Resumo, error) {
	hoje := s.hoje()
	if ano == 0 {
		ano = hoje.Year()
	}
	if mes == 0 {
		mes = int(hoje.Month())
	}
	periodo, err := domain.NewPeriodo(ano, mes, s.loc)
	if err != nil {
		return nil, err
	}

	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	totais, err := s.transacoes.Totais(ctx, userID, periodo.Inicio, periodo.Fim)
	if err != nil {
		return nil, fmt.Errorf("resumo: totais: %w", err)
	}
	porCategoria, err := s.transacoes.TotaisPorCategoria(ctx, userID, periodo.Inicio, periodo.Fim)
	if err != nil {
		return nil, fmt.Errorf("resumo: categorias: %w", err)
	}
	parcelas, err := s.transacoes.ListParcelas(ctx, userID, periodo.Inicio, periodo.Fim)
	if err != nil {
		return nil, fmt.Errorf("resumo: parcelas: %w", err)
	}
	saldoFimMes, err := s.transacoes.SaldoAte(ctx, userID, periodo.Fim)
	if err != nil {
		return nil, fmt.Errorf("resumo: saldo geral: %w", err)
	}
	saldoHoje, err := s.transacoes.SaldoAte(ctx, userID, hoje.AddDate(0, 0, 1))
	if err != nil {
		return nil, fmt.Errorf("resumo: saldo atual: %w", err)
	}

	return &domain.Resumo{
		Periodo:          periodo,
		Receitas:         totais.Entradas,
		Despesas:         totais.Saidas,
		ResultadoMes:     totais.Entradas - totais.Saidas,
		SaldoGeral:       user.SaldoInicial + saldoFimMes,
		SaldoAtual:       user.SaldoInicial + saldoHoje,
		CustosParcelados: totais.SaidasParceladas,
		Parcelas:         parcelas,
		PorCategoria:     porCategoria,
	}, nil
}

// GetProfile monta os dados de /me.
func (s *ResumoService) GetProfile(ctx context.Context, userID string) (*domain.Perfil, error) {
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	hoje := s.hoje()
	periodo := domain.PeriodoDe(hoje)

	totais, err := s.transacoes.Totais(ctx, userID, periodo.Inicio, periodo.Fim)
	if err != nil {
		return nil, fmt.Errorf("perfil: totais: %w", err)
	}
	saldo, err := s.transacoes.SaldoAte(ctx, userID, hoje.AddDate(0, 0, 1))
	if err != nil {
		return nil, fmt.Errorf("perfil: saldo: %w", err)
	}
	return &domain.Perfil{
		User:        user,
		SaldoAtual:  user.SaldoInicial + saldo,
		Periodo:     periodo,
		EntradasMes: totais.Entradas,
		SaidasMes:   totais.Saidas,
		Rendimentos: totais.Rendimentos,
	}, nil
}

// ---------------------------------------------------------------------------
// Parcelamentos
// ---------------------------------------------------------------------------

func (s *ParcelamentoService) build(userID string, in ports.CreateParcelamentoInput) (*domain.Parcelamento, error) {
	data := s.hoje()
	if in.DataPrimeiraParcela != nil {
		y, m, d := in.DataPrimeiraParcela.Date()
		data = time.Date(y, m, d, 0, 0, 0, 0, s.loc)
	}
	return domain.NewParcelamento(domain.NewParcelamentoInput{
		UserID: userID, Tipo: in.Tipo, Descricao: in.Descricao, Categoria: in.Categoria,
		ValorTotal: in.ValorTotal, ValorParcela: in.ValorParcela,
		TotalParcelas: in.TotalParcelas, DataPrimeiraParcela: data,
		ParcelasJaPagas: in.ParcelasJaPagas,
		Financiamento:   in.Financiamento,
	})
}

// Simular projeta a tabela de amortização sem gravar nada.
func (s *ParcelamentoService) Simular(_ context.Context, in ports.SimulacaoFinanciamentoInput) (*ports.SimulacaoFinanciamentoOutput, error) {
	data := s.hoje()
	if in.DataPrimeiraParcela != nil {
		y, m, d := in.DataPrimeiraParcela.Date()
		data = time.Date(y, m, d, 0, 0, 0, 0, s.loc)
	}
	fin := in.Financiamento
	if err := fin.Validate(); err != nil {
		return nil, err
	}
	fin.PrazoContratado = in.TotalParcelas
	parcelas, err := fin.Cronograma(in.TotalParcelas, data)
	if err != nil {
		return nil, err
	}
	total, juros := domain.TotaisCronograma(parcelas)
	out := &ports.SimulacaoFinanciamentoOutput{
		Financiamento: fin, TaxaMensal: fin.TaxaMensal(),
		Parcelas: parcelas, TotalAPagar: total, TotalJuros: juros,
		PrazoContratado: in.TotalParcelas,
	}

	// Com aporte, projeta também o cenário sem ele: é a comparação que mostra
	// ao usuário quanto a amortização extraordinária vale.
	if fin.ExtraMensal > 0 {
		base := fin
		base.ExtraMensal = 0
		if cron, err := base.Cronograma(in.TotalParcelas, data); err == nil {
			totalBase, jurosBase := domain.TotaisCronograma(cron)
			out.SemAporte = &ports.ComparativoFinanciamento{
				TotalParcelas: len(cron), TotalAPagar: totalBase, TotalJuros: jurosBase,
			}
		}
	}
	return out, nil
}

// Create registra a despesa e materializa as parcelas mensais.
func (s *ParcelamentoService) Create(ctx context.Context, userID string, in ports.CreateParcelamentoInput) (*domain.Parcelamento, []*domain.Transacao, error) {
	p, err := s.build(userID, in)
	if err != nil {
		return nil, nil, err
	}
	parcelas := p.GerarParcelas()
	if err := s.parcelamentos.CreateWithParcelas(ctx, userID, p, parcelas); err != nil {
		return nil, nil, fmt.Errorf("criar parcelamento: %w", err)
	}
	s.metrics.TransacaoRegistrada(string(domain.OrigemParcelamento), string(domain.TipoSaida))
	p.Progresso = p.ProgressoEm(parcelas, s.hoje())
	return p, parcelas, nil
}

// Get devolve o parcelamento e suas parcelas.
func (s *ParcelamentoService) Get(ctx context.Context, userID, id string) (*domain.Parcelamento, []*domain.Transacao, error) {
	p, err := s.parcelamentos.FindByID(ctx, userID, id)
	if err != nil {
		return nil, nil, err
	}
	parcelas, err := s.parcelamentos.ListParcelasDoParcelamento(ctx, userID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("listar parcelas: %w", err)
	}
	p.Progresso = p.ProgressoEm(parcelas, s.hoje())
	return p, parcelas, nil
}

// Update recalcula o parcelamento e regera todas as parcelas.
func (s *ParcelamentoService) Update(ctx context.Context, userID, id string, in ports.CreateParcelamentoInput) (*domain.Parcelamento, []*domain.Transacao, error) {
	atual, err := s.parcelamentos.FindByID(ctx, userID, id)
	if err != nil {
		return nil, nil, err
	}
	if in.DataPrimeiraParcela == nil {
		d := atual.DataPrimeiraParcela
		in.DataPrimeiraParcela = &d
	}
	p, err := s.build(userID, in)
	if err != nil {
		return nil, nil, err
	}
	p.ID, p.CreatedAt = atual.ID, atual.CreatedAt
	parcelas := p.GerarParcelas()
	if err := s.parcelamentos.ReplaceWithParcelas(ctx, userID, p, parcelas); err != nil {
		return nil, nil, fmt.Errorf("atualizar parcelamento: %w", err)
	}
	p.Progresso = p.ProgressoEm(parcelas, s.hoje())
	return p, parcelas, nil
}

// Delete remove o parcelamento; com manterPagas preserva parcelas até hoje.
func (s *ParcelamentoService) Delete(ctx context.Context, userID, id string, manterPagas bool) (int64, error) {
	var ate *time.Time
	if manterPagas {
		h := s.hoje()
		ate = &h
	}
	return s.parcelamentos.Delete(ctx, userID, id, ate)
}

// List lista os parcelamentos do usuário.
func (s *ParcelamentoService) List(ctx context.Context, userID string) ([]*domain.Parcelamento, error) {
	return s.parcelamentos.List(ctx, userID, s.hoje())
}
