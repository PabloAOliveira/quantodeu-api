package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/dto"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// CartaoHandler expõe cartões de crédito, compras e faturas.
// Como nos demais, o userID vem EXCLUSIVAMENTE do contexto do AuthRequired.
type CartaoHandler struct {
	cartoes ports.CartaoUseCase
	log     *slog.Logger
}

// NewCartaoHandler cria o handler.
func NewCartaoHandler(c ports.CartaoUseCase, log *slog.Logger) *CartaoHandler {
	return &CartaoHandler{cartoes: c, log: log}
}

func cartaoInput(req dto.CartaoRequest) ports.CreateCartaoInput {
	return ports.CreateCartaoInput{
		Nome: req.Nome, Banco: req.Banco,
		DiaFechamento: req.DiaFechamento, DiaVencimento: req.DiaVencimento,
		Limite: req.Limite.Value, Ativo: req.Ativo,
	}
}

// CreateCartao — POST /api/v1/cartoes
func (h *CartaoHandler) CreateCartao(c *gin.Context) {
	var req dto.CartaoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	userID := middlewares.UserID(c)
	cartao, err := h.cartoes.Create(c.Request.Context(), userID, cartaoInput(req))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	// Devolve o mesmo formato do GET (com melhor dia de compra e faturas), para
	// o cliente não precisar de uma segunda chamada logo após criar.
	c.JSON(http.StatusCreated, dto.NewCartaoResponse(h.resumoOuCru(c, userID, cartao)))
}

// resumoOuCru devolve o resumo do cartão; se a montagem falhar, devolve ao
// menos o cartão cru — gravar deu certo, e negar a resposta por causa de um
// extra seria pior do que entregá-la incompleta.
func (h *CartaoHandler) resumoOuCru(c *gin.Context, userID string, cartao *domain.Cartao) *domain.ResumoCartao {
	if r, err := h.cartoes.Get(c.Request.Context(), userID, cartao.ID); err == nil {
		return r
	}
	return &domain.ResumoCartao{Cartao: cartao}
}

// ListCartoes — GET /api/v1/cartoes
func (h *CartaoHandler) ListCartoes(c *gin.Context) {
	items, err := h.cartoes.List(c.Request.Context(), middlewares.UserID(c))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	out := make([]dto.CartaoResponse, 0, len(items))
	for _, r := range items {
		out = append(out, dto.NewCartaoResponse(r))
	}
	c.JSON(http.StatusOK, dto.ListCartoesResponse{Items: out})
}

// GetCartao — GET /api/v1/cartoes/:id
func (h *CartaoHandler) GetCartao(c *gin.Context) {
	r, err := h.cartoes.Get(c.Request.Context(), middlewares.UserID(c), c.Param("id"))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewCartaoResponse(r))
}

// UpdateCartao — PUT /api/v1/cartoes/:id
func (h *CartaoHandler) UpdateCartao(c *gin.Context) {
	var req dto.CartaoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	userID := middlewares.UserID(c)
	cartao, err := h.cartoes.Update(c.Request.Context(), userID, c.Param("id"), cartaoInput(req))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewCartaoResponse(h.resumoOuCru(c, userID, cartao)))
}

// DeleteCartao — DELETE /api/v1/cartoes/:id
func (h *CartaoHandler) DeleteCartao(c *gin.Context) {
	if err := h.cartoes.Delete(c.Request.Context(), middlewares.UserID(c), c.Param("id")); err != nil {
		respondError(c, h.log, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListFaturas — GET /api/v1/cartoes/:id/faturas
func (h *CartaoHandler) ListFaturas(c *gin.Context) {
	faturas, err := h.cartoes.Faturas(c.Request.Context(), middlewares.UserID(c), c.Param("id"))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	out := make([]dto.FaturaResponse, 0, len(faturas))
	for _, f := range faturas {
		out = append(out, dto.NewFaturaResponse(f))
	}
	c.JSON(http.StatusOK, dto.ListFaturasResponse{Items: out})
}

// GetFatura — GET /api/v1/cartoes/:id/faturas/:competencia
func (h *CartaoHandler) GetFatura(c *gin.Context) {
	f, err := h.cartoes.Fatura(c.Request.Context(), middlewares.UserID(c), c.Param("id"), c.Param("competencia"))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewFaturaResponse(f))
}

// PagarFatura — POST /api/v1/cartoes/:id/faturas/:competencia/pagar
func (h *CartaoHandler) PagarFatura(c *gin.Context) {
	var req dto.PagarFaturaRequest
	if err := bindJSONOpcional(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	f, err := h.cartoes.PagarFatura(c.Request.Context(), middlewares.UserID(c),
		c.Param("id"), c.Param("competencia"),
		ports.PagarFaturaInput{Data: req.Data.Ptr(), Valor: req.Valor.Value})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewFaturaResponse(f))
}

// DesfazerPagamento — DELETE /api/v1/cartoes/:id/faturas/:competencia/pagar
func (h *CartaoHandler) DesfazerPagamento(c *gin.Context) {
	err := h.cartoes.DesfazerPagamento(c.Request.Context(), middlewares.UserID(c),
		c.Param("id"), c.Param("competencia"))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// CreateCompra — POST /api/v1/cartoes/:id/compras
func (h *CartaoHandler) CreateCompra(c *gin.Context) {
	var req dto.CompraCartaoRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	if !req.Valor.Set {
		respondError(c, h.log, domain.NewValidationError("valor", "obrigatório"))
		return
	}
	compras, err := h.cartoes.RegistrarCompra(c.Request.Context(), middlewares.UserID(c), c.Param("id"),
		ports.CreateCompraInput{
			Valor: req.Valor.Value, Categoria: req.Categoria, Descricao: req.Descricao,
			Data: req.Data.Ptr(), TotalParcelas: req.TotalParcelas,
		})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	out := dto.CompraCriadaResponse{}
	if len(compras) > 0 {
		out.GrupoID = compras[0].GrupoID
	}
	for _, x := range compras {
		out.Parcelas = append(out.Parcelas, dto.CompraCartaoResponse{
			ID: x.ID, GrupoID: x.GrupoID, Valor: dto.NewMoney(x.Valor),
			Categoria: x.Categoria, Descricao: x.Descricao,
			Data:          x.DataCompra.Format("2006-01-02"),
			NumeroParcela: x.NumeroParcela, TotalParcelas: x.TotalParcelas,
		})
	}
	c.JSON(http.StatusCreated, out)
}

// DeleteCompra — DELETE /api/v1/compras/:grupo
func (h *CartaoHandler) DeleteCompra(c *gin.Context) {
	if _, err := h.cartoes.ExcluirCompra(c.Request.Context(), middlewares.UserID(c), c.Param("grupo")); err != nil {
		respondError(c, h.log, err)
		return
	}
	c.Status(http.StatusNoContent)
}
