// Package redis implementa ports.SessionStore sobre Redis (TTL nativo).
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

const (
	sessionPrefix = "qd:session:"
	userSetPrefix = "qd:user_sessions:"
)

// SessionStore implementa ports.SessionStore.
type SessionStore struct {
	rdb *goredis.Client
}

var _ ports.SessionStore = (*SessionStore)(nil)

// NewSessionStore cria o store.
func NewSessionStore(rdb *goredis.Client) *SessionStore { return &SessionStore{rdb: rdb} }

// record é a representação serializada (JSON) — mantém tags fora do domínio.
type record struct {
	UserID     string    `json:"uid"`
	CreatedAt  time.Time `json:"c"`
	ExpiresAt  time.Time `json:"e"`
	LastSeenAt time.Time `json:"l"`
	UserAgent  string    `json:"ua"`
	IP         string    `json:"ip"`
}

// Create grava a sessão com TTL até ExpiresAt e indexa por usuário.
func (s *SessionStore) Create(ctx context.Context, sess *domain.Session) error {
	ttl := time.Until(sess.ExpiresAt)
	if ttl <= 0 {
		return errors.New("redis session: sessão já expirada")
	}
	b, err := json.Marshal(record{sess.UserID, sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt, sess.UserAgent, sess.IP})
	if err != nil {
		return err
	}
	_, err = s.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.Set(ctx, sessionPrefix+sess.TokenHash, b, ttl)
		p.SAdd(ctx, userSetPrefix+sess.UserID, sess.TokenHash)
		p.ExpireGT(ctx, userSetPrefix+sess.UserID, ttl)
		p.ExpireNX(ctx, userSetPrefix+sess.UserID, ttl)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis session create: %w", err)
	}
	return nil
}

// FindByTokenHash busca a sessão.
func (s *SessionStore) FindByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	b, err := s.rdb.Get(ctx, sessionPrefix+tokenHash).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, domain.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("redis session get: %w", err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("redis session decode: %w", err)
	}
	return &domain.Session{
		TokenHash: tokenHash, UserID: r.UserID, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
		LastSeenAt: r.LastSeenAt, UserAgent: r.UserAgent, IP: r.IP,
	}, nil
}

// Touch atualiza last_seen preservando o TTL restante.
func (s *SessionStore) Touch(ctx context.Context, tokenHash string, lastSeen time.Time) error {
	sess, err := s.FindByTokenHash(ctx, tokenHash)
	if err != nil {
		return err
	}
	sess.LastSeenAt = lastSeen
	b, err := json.Marshal(record{sess.UserID, sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt, sess.UserAgent, sess.IP})
	if err != nil {
		return err
	}
	return s.rdb.SetArgs(ctx, sessionPrefix+tokenHash, b, goredis.SetArgs{KeepTTL: true, Mode: "XX"}).Err()
}

// Delete remove a sessão.
func (s *SessionStore) Delete(ctx context.Context, tokenHash string) error {
	sess, err := s.FindByTokenHash(ctx, tokenHash)
	if errors.Is(err, domain.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		p.Del(ctx, sessionPrefix+tokenHash)
		p.SRem(ctx, userSetPrefix+sess.UserID, tokenHash)
		return nil
	})
	return err
}

// DeleteAllForUser encerra as sessões do usuário, exceto exceptTokenHash.
func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID, exceptTokenHash string) (int64, error) {
	hashes, err := s.rdb.SMembers(ctx, userSetPrefix+userID).Result()
	if err != nil {
		return 0, err
	}
	var keys []string
	var removed []any
	for _, h := range hashes {
		if h == exceptTokenHash {
			continue
		}
		keys = append(keys, sessionPrefix+h)
		removed = append(removed, h)
	}
	if len(keys) == 0 {
		return 0, nil
	}
	var delCmd *goredis.IntCmd
	_, err = s.rdb.TxPipelined(ctx, func(p goredis.Pipeliner) error {
		delCmd = p.Del(ctx, keys...)
		p.SRem(ctx, userSetPrefix+userID, removed...)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return delCmd.Val(), nil
}

// DeleteExpired é no-op: o Redis expira as chaves sozinho.
func (s *SessionStore) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }
