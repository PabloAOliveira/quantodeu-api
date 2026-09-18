package middlewares

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// MemoryRateLimiter é um token bucket em memória (por instância).
// Em múltiplas réplicas use o limitador Redis (RATE_LIMIT_STORE=redis).
type MemoryRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens por segundo
	burst   float64
	maxKeys int
}

var _ ports.RateLimiter = (*MemoryRateLimiter)(nil)

type bucket struct {
	tokens float64
	last   time.Time
}

// NewMemoryRateLimiter cria um limitador e inicia a limpeza até ctx acabar.
func NewMemoryRateLimiter(ctx context.Context, ratePerSecond float64, burst int) *MemoryRateLimiter {
	rl := &MemoryRateLimiter{
		buckets: make(map[string]*bucket),
		rate:    ratePerSecond,
		burst:   float64(burst),
		maxKeys: 100_000,
	}
	go rl.cleanup(ctx)
	return rl
}

// Allow implementa ports.RateLimiter.
func (rl *MemoryRateLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		if len(rl.buckets) >= rl.maxKeys { // proteção de memória sob ataque distribuído
			return false, time.Second, nil
		}
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens = math.Min(rl.burst, b.tokens+now.Sub(b.last).Seconds()*rl.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0, nil
	}
	return false, time.Duration((1 - b.tokens) / rl.rate * float64(time.Second)), nil
}

func (rl *MemoryRateLimiter) cleanup(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			idle := time.Duration(rl.burst/rl.rate*float64(time.Second)) + time.Minute
			rl.mu.Lock()
			for k, b := range rl.buckets {
				if now.Sub(b.last) > idle {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// RateLimitObserver recebe eventos de bloqueio (métricas).
type RateLimitObserver interface {
	RateLimited(scope string)
}

// RateLimit aplica o limitador usando o IP do cliente (+ escopo) como chave.
//
// Falhas do backend (ex.: Redis fora do ar) são FAIL-OPEN: a requisição segue
// e o erro é registrado. Derrubar a API inteira por falta do Redis seria pior
// que perder temporariamente o rate limit — o bloqueio progressivo de login
// continua ativo no PostgreSQL.
func RateLimit(rl ports.RateLimiter, scope string, obs RateLimitObserver, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, wait, err := rl.Allow(c.Request.Context(), scope+"|"+c.ClientIP())
		if err != nil {
			log.WarnContext(c.Request.Context(), "rate limit indisponível (fail-open)", slog.String("scope", scope), slog.Any("err", err))
			c.Next()
			return
		}
		if !ok {
			if obs != nil {
				obs.RateLimited(scope)
			}
			c.Header("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
				"code": "rate_limited", "message": "muitas requisições; tente novamente em instantes",
				"request_id": c.GetString(CtxRequestID),
			}})
			return
		}
		c.Next()
	}
}
