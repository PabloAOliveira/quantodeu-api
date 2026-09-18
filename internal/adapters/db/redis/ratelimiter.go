package redis

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// tokenBucketScript implementa um token bucket atômico no Redis.
// Usa o relógio do PRÓPRIO Redis (TIME), evitando divergência entre réplicas.
//
// KEYS[1] = chave do bucket
// ARGV[1] = taxa (tokens/segundo), ARGV[2] = burst
// Retorno: {permitido (0|1), espera_ms}
var tokenBucketScript = goredis.NewScript(`
local key   = KEYS[1]
local rate  = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local t     = redis.call('TIME')
local now   = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then
  tokens = burst
  ts = now
end

tokens = math.min(burst, tokens + math.max(0, now - ts) / 1000 * rate)
local allowed = 0
local wait = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  wait = math.ceil((1 - tokens) / rate * 1000)
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', key, math.ceil(burst / rate * 1000) + 1000)
return {allowed, wait}
`)

// RateLimiter implementa ports.RateLimiter compartilhado entre réplicas.
type RateLimiter struct {
	rdb    *goredis.Client
	prefix string
	rate   float64
	burst  int
}

var _ ports.RateLimiter = (*RateLimiter)(nil)

// NewRateLimiter cria um limitador com o prefixo de escopo informado.
func NewRateLimiter(rdb *goredis.Client, scope string, ratePerSecond float64, burst int) *RateLimiter {
	return &RateLimiter{rdb: rdb, prefix: "qd:rl:" + scope + ":", rate: ratePerSecond, burst: burst}
}

// Allow consome um token da chave.
func (l *RateLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	res, err := tokenBucketScript.Run(ctx, l.rdb, []string{l.prefix + key}, l.rate, l.burst).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("redis rate limit: %w", err)
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("redis rate limit: resposta inesperada %v", res)
	}
	return res[0] == 1, time.Duration(res[1]) * time.Millisecond, nil
}
