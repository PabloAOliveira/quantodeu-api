// Package handlers contém os adaptadores primários HTTP (Gin). Handlers só
// fazem: decodificar/validar entrada -> chamar a porta primária -> mapear
// saída/erro para JSON. Nenhuma regra de negócio vive aqui.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/dto"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

// bindJSON decodifica estritamente (rejeita campos desconhecidos e lixo após
// o objeto) e aplica as validações das tags `binding`.
// bindJSONOpcional aceita requisição SEM corpo, para rotas em que todo campo é
// opcional — obrigar o cliente a mandar um "{}" só para dizer "use o padrão"
// seria cerimônia à toa.
func bindJSONOpcional(c *gin.Context, dst any) error {
	if c.Request.Body == nil || c.Request.ContentLength == 0 {
		return nil
	}
	return bindJSON(c, dst)
}

func bindJSON(c *gin.Context, dst any) error {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errPayloadTooLarge
		}
		if errors.Is(err, io.EOF) {
			return domain.NewValidationError("body", "corpo JSON obrigatório")
		}
		if ve, ok := domain.IsValidationError(err); ok {
			return ve
		}
		return domain.NewValidationError("body", "JSON inválido: "+sanitizeJSONError(err))
	}
	if dec.More() {
		return domain.NewValidationError("body", "JSON deve conter um único objeto")
	}
	return validate(dst)
}

func validate(dst any) error {
	if err := binding.Validator.ValidateStruct(dst); err != nil {
		var verrs validator.ValidationErrors
		if errors.As(err, &verrs) && len(verrs) > 0 {
			fe := verrs[0]
			return domain.NewValidationError(toSnake(fe.Field()), validationMessage(fe))
		}
		return domain.NewValidationError("body", "dados inválidos")
	}
	return nil
}

var errPayloadTooLarge = errors.New("payload too large")

func sanitizeJSONError(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return fmt.Sprintf("campo %q com tipo inválido", ute.Field)
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "json: unknown field ") {
		return "campo desconhecido " + strings.TrimPrefix(msg, "json: unknown field ")
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}

func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "obrigatório"
	case "max":
		return "excede o tamanho/valor máximo (" + fe.Param() + ")"
	case "min":
		return "abaixo do mínimo (" + fe.Param() + ")"
	case "oneof":
		return "deve ser um de: " + fe.Param()
	case "len":
		return "deve ter exatamente " + fe.Param() + " caracteres"
	case "numeric":
		return "deve conter apenas dígitos"
	default:
		return "inválido"
	}
}

func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// respondError traduz erros de domínio para HTTP de forma consistente.
func respondError(c *gin.Context, log *slog.Logger, err error) {
	reqID := c.GetString(middlewares.CtxRequestID)
	write := func(status int, code, msg, field string) {
		c.AbortWithStatusJSON(status, dto.ErrorBody{Error: dto.ErrorDetail{Code: code, Message: msg, Field: field, RequestID: reqID}})
	}

	if ve, ok := domain.IsValidationError(err); ok {
		write(http.StatusUnprocessableEntity, "validation_error", ve.Message, ve.Field)
		return
	}
	if re, ok := domain.IsRetryAfter(err); ok {
		secs := int(math.Ceil(re.RetryAfter.Seconds()))
		c.Header("Retry-After", strconv.Itoa(secs))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, dto.ErrorBody{Error: dto.ErrorDetail{
			Code: re.Code, Message: re.Message, RetryAfter: secs, RequestID: reqID,
		}})
		return
	}
	switch {
	case errors.Is(err, errPayloadTooLarge):
		write(http.StatusRequestEntityTooLarge, "payload_too_large", "corpo da requisição excede o limite", "")
	case errors.Is(err, domain.ErrEmailAlreadyExists):
		write(http.StatusConflict, "email_already_exists", err.Error(), "email")
	case errors.Is(err, domain.ErrPhoneAlreadyExists):
		write(http.StatusConflict, "phone_already_exists", err.Error(), "telefone")
	case errors.Is(err, domain.ErrInvalidCredentials):
		write(http.StatusUnauthorized, "invalid_credentials", "e-mail ou senha inválidos", "")
	case errors.Is(err, domain.ErrSessionNotFound):
		write(http.StatusUnauthorized, "unauthorized", "autenticação necessária", "")
	case errors.Is(err, domain.ErrNotFound):
		write(http.StatusNotFound, "not_found", "recurso não encontrado", "")
	case errors.Is(err, domain.ErrParcelaGerenciada):
		write(http.StatusConflict, "installment_managed_by_parent", err.Error(), "")
	case errors.Is(err, domain.ErrPhoneAlreadyVerified):
		write(http.StatusConflict, "phone_already_verified", err.Error(), "")
	case errors.Is(err, domain.ErrVerificationInvalid):
		write(http.StatusUnprocessableEntity, "verification_invalid", err.Error(), "codigo")
	case errors.Is(err, domain.ErrVerificationUnavailable):
		write(http.StatusUnprocessableEntity, "verification_method_unavailable", err.Error(), "metodo")
	case errors.Is(err, domain.ErrEmailNotVerified):
		write(http.StatusForbidden, "email_nao_verificado", err.Error(), "")
	case errors.Is(err, domain.ErrCartaoComHistorico):
		write(http.StatusConflict, "cartao_com_historico", err.Error(), "")
	case errors.Is(err, domain.ErrFaturaJaPaga):
		write(http.StatusConflict, "fatura_ja_paga", err.Error(), "")
	case errors.Is(err, domain.ErrFaturaNaoPaga):
		write(http.StatusConflict, "fatura_nao_paga", err.Error(), "")
	case errors.Is(err, domain.ErrFaturaFechadaParaCompra):
		write(http.StatusConflict, "fatura_fechada_para_compra", err.Error(), "")
	case errors.Is(err, domain.ErrFaturaVazia):
		write(http.StatusUnprocessableEntity, "fatura_vazia", err.Error(), "")
	case errors.Is(err, domain.ErrEmailAlreadyVerified):
		write(http.StatusConflict, "email_already_verified", err.Error(), "")
	case errors.Is(err, domain.ErrEmailDeliveryFailed):
		write(http.StatusServiceUnavailable, "email_delivery_failed", err.Error(), "")
	case errors.Is(err, domain.ErrPasswordReuse):
		write(http.StatusUnprocessableEntity, "password_reuse", err.Error(), "nova_senha")
	default:
		log.ErrorContext(c.Request.Context(), "erro não tratado",
			slog.Any("err", err), slog.String("request_id", reqID), slog.String("route", c.FullPath()))
		write(http.StatusInternalServerError, "internal_error", "erro interno", "")
	}
}
