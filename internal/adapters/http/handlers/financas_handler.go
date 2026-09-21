package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/dto"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// FinancasHandler expõe transações, resumo e parcelamentos.
// O userID vem EXCLUSIVAMENTE do contexto preenchido pelo AuthRequired —
// nunca de parâmetros de rota, query ou corpo.
type FinancasHandler struct {
	transacoes    ports.TransacaoUseCase
	resumo        ports.ResumoUseCase
	parcelamentos ports.ParcelamentoUseCase
	log           *slog.Logger
}

// NewFinancasHandler cria o handler.
func NewFinancasHandler(t ports.TransacaoUseCase, r ports.ResumoUseCase, p ports.ParcelamentoUseCase, log *slog.Logger) *FinancasHandler {
	return &FinancasHandler{transacoes: t, resumo: r, parcelamentos: p, log: log}
}

// ---------------------------------------------------------------------------
// Transações
// ---------------------------------------------------------------------------

// ListTransacoes — GET /api/v1/transacoes
func (h *FinancasHandler) ListTransacoes(c *gin.Context) {
	var q dto.ListTransacoesQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		respondError(c, h.log, queryError(err))
		return
	}
	out, err := h.transacoes.List(c.Request.Context(), middlewares.UserID(c), ports.ListTransacoesInput{
		Ano: q.Ano, Mes: q.Mes, Categoria: q.Categoria, Tipo: domain.TipoTransacao(q.Tipo),
		Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	totalPages := (out.Total + int64(out.PageSize) - 1) / int64(out.PageSize)
	c.JSON(http.StatusOK, dto.ListTransacoesResponse{
		Referencia: out.Periodo.Inicio.Format("2006-01"),
		Items:      dto.NewTransacoesResponse(out.Items),
		Pagination: dto.Pagination{Page: out.Page, PageSize: out.PageSize, Total: out.Total, TotalPages: totalPages},
	})
}

// CreateTransacao — POST /api/v1/transacoes
func (h *FinancasHandler) CreateTransacao(c *gin.Context) {
	var req dto.CreateTransacaoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	if !req.Valor.Set {
		respondError(c, h.log, domain.NewValidationError("valor", "obrigatório"))
		return
	}
	t, err := h.transacoes.Create(c.Request.Context(), middlewares.UserID(c), ports.CreateTransacaoInput{
		Tipo: domain.TipoTransacao(req.Tipo), Valor: req.Valor.Value,
		Categoria: req.Categoria, Descricao: req.Descricao, Data: req.Data.Ptr(),
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusCreated, dto.NewTransacaoResponse(t))
}

// GetTransacao — GET /api/v1/transacoes/:id
func (h *FinancasHandler) GetTransacao(c *gin.Context) {
	t, err := h.transacoes.Get(c.Request.Context(), middlewares.UserID(c), c.Param("id"))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewTransacaoResponse(t))
}

// UpdateTransacao — PUT /api/v1/transacoes/:id
func (h *FinancasHandler) UpdateTransacao(c *gin.Context) {
	var req dto.UpdateTransacaoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	if !req.Valor.Set {
		respondError(c, h.log, domain.NewValidationError("valor", "obrigatório"))
		return
	}
	t, err := h.transacoes.Update(c.Request.Context(), middlewares.UserID(c), c.Param("id"), ports.UpdateTransacaoInput{
		Tipo: domain.TipoTransacao(req.Tipo), Valor: req.Valor.Value,
		Categoria: req.Categoria, Descricao: req.Descricao, Data: req.Data.Ptr(),
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewTransacaoResponse(t))
}

// DeleteTransacao — DELETE /api/v1/transacoes/:id
func (h *FinancasHandler) DeleteTransacao(c *gin.Context) {
	if err := h.transacoes.Delete(c.Request.Context(), middlewares.UserID(c), c.Param("id")); err != nil {
		respondError(c, h.log, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Resumo
// ---------------------------------------------------------------------------

// Resumo — GET /api/v1/resumo
func (h *FinancasHandler) Resumo(c *gin.Context) {
	var q dto.ResumoQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		respondError(c, h.log, queryError(err))
		return
	}
	r, err := h.resumo.GetResumo(c.Request.Context(), middlewares.UserID(c), q.Ano, q.Mes)
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewResumoResponse(r))
}

// Agenda — GET /api/v1/agenda
func (h *FinancasHandler) Agenda(c *gin.Context) {
	var q dto.AgendaQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		respondError(c, h.log, queryError(err))
		return
	}
	a, err := h.resumo.GetAgenda(c.Request.Context(), middlewares.UserID(c), q.Meses)
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewAgendaResponse(a))
}

// ---------------------------------------------------------------------------
// Parcelamentos
// ---------------------------------------------------------------------------

func parcelamentoInput(req dto.CreateParcelamentoRequest) ports.CreateParcelamentoInput {
	return ports.CreateParcelamentoInput{
		Tipo: domain.TipoParcelamento(req.Tipo), Descricao: req.Descricao, Categoria: req.Categoria,
		ValorTotal: req.ValorTotal.Value, ValorParcela: req.ValorParcela.Value,
		TotalParcelas: req.TotalParcelas, DataPrimeiraParcela: req.DataPrimeiraParcela.Ptr(),
		ParcelasJaPagas: req.ParcelasJaPagas,
		Financiamento:   financiamentoInput(req.Financiamento),
	}
}

func financiamentoInput(f *dto.FinanciamentoRequest) *domain.Financiamento {
	if f == nil {
		return nil
	}
	return f.Domain()
}

func detalhe(p *domain.Parcelamento, parcelas []*domain.Transacao) dto.ParcelamentoDetalheResponse {
	return dto.ParcelamentoDetalheResponse{
		Parcelamento: dto.NewParcelamentoResponse(p),
		Parcelas:     dto.NewTransacoesResponse(parcelas),
	}
}

// CreateParcelamento — POST /api/v1/parcelamentos
func (h *FinancasHandler) CreateParcelamento(c *gin.Context) {
	var req dto.CreateParcelamentoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	p, parcelas, err := h.parcelamentos.Create(c.Request.Context(), middlewares.UserID(c), parcelamentoInput(req))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusCreated, detalhe(p, parcelas))
}

// ListParcelamentos — GET /api/v1/parcelamentos
func (h *FinancasHandler) ListParcelamentos(c *gin.Context) {
	items, err := h.parcelamentos.List(c.Request.Context(), middlewares.UserID(c))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	out := make([]dto.ParcelamentoResponse, 0, len(items))
	for _, p := range items {
		out = append(out, dto.NewParcelamentoResponse(p))
	}
	c.JSON(http.StatusOK, dto.ListParcelamentosResponse{Items: out})
}

// GetParcelamento — GET /api/v1/parcelamentos/:id
func (h *FinancasHandler) GetParcelamento(c *gin.Context) {
	p, parcelas, err := h.parcelamentos.Get(c.Request.Context(), middlewares.UserID(c), c.Param("id"))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, detalhe(p, parcelas))
}

// UpdateParcelamento — PUT /api/v1/parcelamentos/:id
func (h *FinancasHandler) UpdateParcelamento(c *gin.Context) {
	var req dto.CreateParcelamentoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	p, parcelas, err := h.parcelamentos.Update(c.Request.Context(), middlewares.UserID(c), c.Param("id"), parcelamentoInput(req))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, detalhe(p, parcelas))
}

// DeleteParcelamento — DELETE /api/v1/parcelamentos/:id?manter_pagas=true
func (h *FinancasHandler) DeleteParcelamento(c *gin.Context) {
	var q dto.DeleteParcelamentoQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		respondError(c, h.log, queryError(err))
		return
	}
	n, err := h.parcelamentos.Delete(c.Request.Context(), middlewares.UserID(c), c.Param("id"), q.ManterPagas)
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.DeleteParcelamentoResponse{ParcelasRemovidas: n, ManteveParcelasPagas: q.ManterPagas})
}

// queryError converte erros de binding de query string em ValidationError.
func queryError(err error) error {
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) && len(verrs) > 0 {
		return domain.NewValidationError(toSnake(verrs[0].Field()), validationMessage(verrs[0]))
	}
	return domain.NewValidationError("query", "parâmetros de consulta inválidos (verifique números e booleanos)")
}

// SimularFinanciamento — POST /api/v1/parcelamentos/simular
//
// Projeta a tabela de amortização sem gravar nada (tela de simulação do app).
func (h *FinancasHandler) SimularFinanciamento(c *gin.Context) {
	var req dto.SimularFinanciamentoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	out, err := h.parcelamentos.Simular(c.Request.Context(), ports.SimulacaoFinanciamentoInput{
		Financiamento: domain.Financiamento{
			Banco: req.Banco, Sistema: domain.SistemaAmortizacao(req.Sistema),
			ValorFinanciado: req.ValorFinanciado.Value, TaxaAnual: req.TaxaJurosAnual,
			TipoTaxa: domain.TipoTaxa(req.TipoTaxa), ExtraMensal: req.PagamentoExtra.Value,
		},
		TotalParcelas:       req.TotalParcelas,
		DataPrimeiraParcela: req.DataPrimeiraParcela.Ptr(),
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewSimulacaoResponse(out))
}
