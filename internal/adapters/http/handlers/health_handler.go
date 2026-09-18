package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/dto"
)

// Pinger verifica uma dependência.
type Pinger func(ctx context.Context) error

// HealthCheck descreve uma dependência. Critical=false significa que a API
// funciona degradada sem ela (ex.: Redis usado só para rate limit, que é
// fail-open) e não deve tirar a instância do balanceador.
type HealthCheck struct {
	Ping     Pinger
	Critical bool
}

// HealthHandler expõe liveness e readiness.
type HealthHandler struct {
	checks map[string]HealthCheck
}

// NewHealthHandler cria o handler.
func NewHealthHandler(checks map[string]HealthCheck) *HealthHandler {
	return &HealthHandler{checks: checks}
}

// Live responde 200 enquanto o processo está de pé.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, dto.HealthResponse{Status: "ok"})
}

// Ready verifica dependências: 503 se alguma crítica falhar; "degradado"
// (200) se só as não críticas falharem.
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	resp := dto.HealthResponse{Status: "ok", Checks: map[string]string{}}
	status := http.StatusOK
	for name, chk := range h.checks {
		if err := chk.Ping(ctx); err != nil {
			resp.Checks[name] = "indisponivel"
			if chk.Critical {
				status, resp.Status = http.StatusServiceUnavailable, "indisponivel"
			} else if resp.Status == "ok" {
				resp.Status = "degradado"
			}
			continue
		}
		resp.Checks[name] = "ok"
	}
	c.JSON(status, resp)
}
