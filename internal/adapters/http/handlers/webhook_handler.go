package handlers

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/whatsapp"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// WebhookHandler recebe webhooks do provedor de WhatsApp configurado.
type WebhookHandler struct {
	converter whatsapp.Converter
	useCase   ports.WebhookUseCase
	log       *slog.Logger
}

// NewWebhookHandler cria o handler.
func NewWebhookHandler(conv whatsapp.Converter, uc ports.WebhookUseCase, log *slog.Logger) *WebhookHandler {
	return &WebhookHandler{converter: conv, useCase: uc, log: log}
}

// WhatsApp godoc
// POST /api/v1/webhook/whatsapp
//
// Fluxo: autentica (token/assinatura) -> converte payload -> processa cada
// mensagem. Responde 200 para eventos válidos mesmo quando a mensagem é
// ignorada, evitando reenvios infinitos do provedor. Responde 5xx apenas em
// falhas de infraestrutura (o provedor reenvia e a idempotência por
// external_id impede duplicidade).
func (h *WebhookHandler) WhatsApp(c *gin.Context) {
	ctx := c.Request.Context()
	logger := h.log.With(slog.String("provider", h.converter.Name()), slog.String("request_id", c.GetString(middlewares.CtxRequestID)))

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if err := h.converter.Authenticate(c.Request, body); err != nil {
		logger.WarnContext(ctx, "webhook rejeitado: autenticação inválida", slog.String("ip", c.ClientIP()))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	msgs, err := h.converter.Convert(c.Request, body)
	if err != nil {
		logger.WarnContext(ctx, "webhook com payload inválido", slog.Any("err", err))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	for _, m := range msgs {
		res, err := h.useCase.ProcessMessage(ctx, m)
		if err != nil {
			logger.ErrorContext(ctx, "falha ao processar mensagem", slog.Any("err", err), slog.String("message_id", m.MessageID))
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		logger.DebugContext(ctx, "mensagem processada", slog.String("status", string(res.Status)), slog.String("message_id", m.MessageID))
	}

	ct, ack := h.converter.Ack()
	c.Data(http.StatusOK, ct, ack)
}
