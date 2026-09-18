//go:build integration

package redis

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

func client(t *testing.T) *goredis.Client {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR não definido")
	}
	rdb := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestRateLimiter_CompartilhadoEntreInstancias(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	scope := fmt.Sprintf("test%d", time.Now().UnixNano())
	// Duas "réplicas" compartilham o mesmo bucket: burst 5, 1 token/s.
	r1, r2 := NewRateLimiter(rdb, scope, 1, 5), NewRateLimiter(rdb, scope, 1, 5)

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := r1
			if i%2 == 0 {
				l = r2
			}
			ok, _, err := l.Allow(ctx, "1.2.3.4")
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("20 requisições concorrentes em 2 réplicas deveriam liberar exatamente 5; liberou %d", allowed)
	}
	ok, wait, _ := r1.Allow(ctx, "1.2.3.4")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("deveria negar com espera ~1s: ok=%v wait=%s", ok, wait)
	}
	if ok, _, _ := r1.Allow(ctx, "5.6.7.8"); !ok {
		t.Fatal("outra chave não pode ser afetada")
	}
}

func TestLoginAttemptStore_Redis(t *testing.T) {
	rdb := client(t)
	ctx := context.Background()
	s := NewLoginAttemptStore(rdb)
	key := fmt.Sprintf("x%d@t.com", time.Now().UnixNano())
	for i := 1; i <= 3; i++ {
		if n, err := s.RegisterFailure(ctx, key, time.Now(), time.Hour); err != nil || n != i {
			t.Fatalf("falha %d = %d, %v", i, n, err)
		}
	}
	until := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	_ = s.Lock(ctx, key, until)
	st, _ := s.Get(ctx, key)
	if st.Failures != 3 || !st.LockedUntil.Equal(until) {
		t.Fatalf("estado = %+v", st)
	}
	_ = s.Reset(ctx, key)
	if st, _ := s.Get(ctx, key); st != (domain.LoginAttemptState{}) {
		t.Fatalf("reset: %+v", st)
	}
}
