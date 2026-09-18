package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// LoginAttemptStore implementa ports.LoginAttemptStore no Redis.
// Cada chave é um hash {failures, locked_until_ms} com TTL igual à janela.
type LoginAttemptStore struct {
	rdb *goredis.Client
}

var _ ports.LoginAttemptStore = (*LoginAttemptStore)(nil)

// NewLoginAttemptStore cria o store.
func NewLoginAttemptStore(rdb *goredis.Client) *LoginAttemptStore {
	return &LoginAttemptStore{rdb: rdb}
}

func attemptKey(key string) string {
	sum := sha256.Sum256([]byte("login:" + key))
	return "qd:login:" + hex.EncodeToString(sum[:])
}

// Get devolve o estado atual.
func (s *LoginAttemptStore) Get(ctx context.Context, key string) (domain.LoginAttemptState, error) {
	var st domain.LoginAttemptState
	vals, err := s.rdb.HMGet(ctx, attemptKey(key), "failures", "locked_until").Result()
	if err != nil {
		return st, err
	}
	if v, ok := vals[0].(string); ok {
		st.Failures, _ = strconv.Atoi(v)
	}
	if v, ok := vals[1].(string); ok {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			st.LockedUntil = time.UnixMilli(ms)
		}
	}
	return st, nil
}

// RegisterFailure incrementa atomicamente; o TTL da chave É a janela.
func (s *LoginAttemptStore) RegisterFailure(ctx context.Context, key string, _ time.Time, window time.Duration) (int, error) {
	k := attemptKey(key)
	var incr *goredis.IntCmd
	_, err := s.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		incr = p.HIncrBy(ctx, k, "failures", 1)
		p.ExpireNX(ctx, k, window) // a janela conta a partir da 1ª falha
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(incr.Val()), nil
}

// Lock grava o fim do bloqueio e garante que a chave viva ao menos até lá.
func (s *LoginAttemptStore) Lock(ctx context.Context, key string, until time.Time) error {
	k := attemptKey(key)
	_, err := s.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.HSet(ctx, k, "locked_until", until.UnixMilli())
		p.ExpireGT(ctx, k, time.Until(until)+time.Minute)
		return nil
	})
	if errors.Is(err, goredis.Nil) {
		return nil
	}
	return err
}

// Reset remove o histórico.
func (s *LoginAttemptStore) Reset(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, attemptKey(key)).Err()
}
