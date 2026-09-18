// Package middlewares contém os middlewares Gin do adaptador HTTP.
package middlewares

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/session"
	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// Chaves de contexto.
const (
	CtxUserID    = "userID"
	CtxSession   = "session"
	CtxRequestID = "requestID"
	CtxBearer    = "authBearer"
)

// AuthRequired extrai o cookie de sessão, valida a assinatura HMAC, consulta
// o SessionStore (via AuthUseCase) e injeta o userID no contexto.
func AuthRequired(auth ports.AuthUseCase, cookies *session.CookieManager, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, bearer, ok := cookies.ReadRequest(c.Request)
		if !ok {
			abortUnauthorized(c)
			return
		}

		sess, err := auth.Authenticate(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, domain.ErrSessionNotFound) {
				if !bearer {
					cookies.Clear(c.Writer)
				}
				abortUnauthorized(c)
				return
			}
			log.ErrorContext(c.Request.Context(), "auth middleware: falha ao validar sessão",
				slog.Any("err", err), slog.String("request_id", c.GetString(CtxRequestID)))
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"code": "session_unavailable", "message": "não foi possível validar a sessão",
			}})
			return
		}

		c.Set(CtxUserID, sess.UserID)
		c.Set(CtxSession, sess)
		c.Set(CtxBearer, bearer)
		c.Next()
	}
}

// EmailVerifiedRequired bloqueia (403 email_nao_verificado) usuários que ainda
// não confirmaram o e-mail. Deve vir DEPOIS de AuthRequired.
func EmailVerifiedRequired(v ports.EmailVerificationUseCase, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, err := v.IsVerified(c.Request.Context(), UserID(c))
		if err != nil {
			log.ErrorContext(c.Request.Context(), "verificação de e-mail: falha ao consultar usuário",
				slog.Any("err", err), slog.String("request_id", c.GetString(CtxRequestID)))
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
				"code": "session_unavailable", "message": "não foi possível validar a conta",
			}})
			return
		}
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"code": "email_nao_verificado", "message": domain.ErrEmailNotVerified.Error(),
			}})
			return
		}
		c.Next()
	}
}

// UserID devolve o ID do usuário autenticado. Handlers protegidos por
// AuthRequired sempre possuem esse valor.
func UserID(c *gin.Context) string { return c.GetString(CtxUserID) }

// UsingBearer informa se a requisição autenticou por "Authorization: Bearer".
func UsingBearer(c *gin.Context) bool { return c.GetBool(CtxBearer) }

func abortUnauthorized(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{
		"code": "unauthorized", "message": "autenticação necessária",
	}})
}
