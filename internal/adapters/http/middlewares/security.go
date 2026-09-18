package middlewares

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
)

var reRequestID = regexp.MustCompile(`^[A-Za-z0-9\-_.]{8,64}$`)

// RequestID propaga X-Request-ID (se válido) ou gera um novo.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !reRequestID.MatchString(id) {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		c.Set(CtxRequestID, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// Logger registra cada requisição em JSON estruturado (sem corpo, sem cookies).
func Logger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		attrs := []slog.Attr{
			slog.String("request_id", c.GetString(CtxRequestID)),
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("ip", c.ClientIP()),
			slog.String("user_id", c.GetString(CtxUserID)),
			slog.Int("bytes", c.Writer.Size()),
		}
		if sc := trace.SpanContextFromContext(c.Request.Context()); sc.HasTraceID() {
			attrs = append(attrs, slog.String("trace_id", sc.TraceID().String()))
		}
		log.LogAttrs(c.Request.Context(), level, "http_request", attrs...)
	}
}

// HTTPRecorder recebe as métricas de cada requisição (Prometheus).
type HTTPRecorder interface {
	ObserveHTTP(method, route string, status int, duration time.Duration)
	InFlight(delta float64)
}

// Metrics mede volume, latência e requisições em andamento. Usa a ROTA
// (c.FullPath, ex.: /api/v1/transacoes/:id) e não a URL, evitando explosão de
// cardinalidade com IDs.
func Metrics(rec HTTPRecorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		rec.InFlight(1)
		defer rec.InFlight(-1)
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		rec.ObserveHTTP(c.Request.Method, route, c.Writer.Status(), time.Since(start))
	}
}

// Recovery converte panics em 500 sem vazar detalhes ao cliente.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(c.Request.Context(), "panic recuperado",
					slog.Any("panic", rec),
					slog.String("request_id", c.GetString(CtxRequestID)),
					slog.String("stack", string(debug.Stack())))
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{
					"code": "internal_error", "message": "erro interno", "request_id": c.GetString(CtxRequestID),
				}})
			}
		}()
		c.Next()
	}
}

// SecurityHeaders aplica cabeçalhos defensivos para uma API JSON.
func SecurityHeaders(production bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		if production {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		}
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// BodyLimit limita o tamanho do corpo da requisição.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{
				"code": "payload_too_large", "message": "corpo da requisição excede o limite",
			}})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// CORS permite apenas origens da allowlist, com credenciais (cookies).
// Nunca devolve "*" junto de Access-Control-Allow-Credentials.
func CORS(allowed []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		c.Writer.Header().Add("Vary", "Origin")
		if origin != "" && slices.Contains(allowed, origin) {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
			if c.Request.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				h.Set("Access-Control-Max-Age", "600")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// CSRFProtection é uma defesa em profundidade além do SameSite=Strict:
// em métodos que alteram estado, rejeita Origin fora da allowlist e
// requisições marcadas pelo navegador como cross-site, e exige JSON quando
// há corpo (impede ataques via <form> HTML).
func CSRFProtection(allowed []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" && !slices.Contains(allowed, origin) && !sameOrigin(origin, c.Request.Host) {
			abortForbidden(c, "origem não permitida")
			return
		}
		if c.GetHeader("Sec-Fetch-Site") == "cross-site" {
			abortForbidden(c, "requisição cross-site bloqueada")
			return
		}
		if c.Request.ContentLength != 0 {
			ct := strings.ToLower(c.ContentType())
			if ct != "application/json" {
				c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{"error": gin.H{
					"code": "unsupported_media_type", "message": "Content-Type deve ser application/json",
				}})
				return
			}
		}
		c.Next()
	}
}

// sameOrigin permite chamadas da própria API (ex.: Swagger UI em /docs).
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

func abortForbidden(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "forbidden", "message": msg}})
}
