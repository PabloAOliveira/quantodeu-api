package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// SessionStore implementa ports.SessionStore no PostgreSQL.
// Operações por token (usuário ainda desconhecido) usam funções SECURITY DEFINER.
type SessionStore struct {
	db *pgxpool.Pool
}

var _ ports.SessionStore = (*SessionStore)(nil)

// NewSessionStore cria o store.
func NewSessionStore(db *pgxpool.Pool) *SessionStore { return &SessionStore{db: db} }

// Create persiste a sessão (dentro do tenant do usuário).
func (s *SessionStore) Create(ctx context.Context, sess *domain.Session) error {
	err := withTenant(ctx, s.db, sess.UserID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at, user_agent, ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			sess.TokenHash, sess.UserID, sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt, sess.UserAgent, sess.IP)
		return err
	})
	if err != nil {
		return fmt.Errorf("sessions.create: %w", err)
	}
	return nil
}

// FindByTokenHash busca uma sessão ainda não expirada.
func (s *SessionStore) FindByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	var sess domain.Session
	err := s.db.QueryRow(ctx, `
		SELECT token_hash, user_id, created_at, expires_at, last_seen_at, user_agent, ip
		FROM qd_find_session($1)`, tokenHash,
	).Scan(&sess.TokenHash, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt, &sess.UserAgent, &sess.IP)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sessions.find: %w", err)
	}
	return &sess, nil
}

// Touch atualiza o último acesso.
func (s *SessionStore) Touch(ctx context.Context, tokenHash string, lastSeen time.Time) error {
	_, err := s.db.Exec(ctx, `SELECT qd_touch_session($1, $2)`, tokenHash, lastSeen)
	return err
}

// Delete remove uma sessão.
func (s *SessionStore) Delete(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx, `SELECT qd_delete_session($1)`, tokenHash)
	return err
}

// DeleteAllForUser encerra as sessões do usuário, exceto exceptTokenHash.
func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID, exceptTokenHash string) (int64, error) {
	var n int64
	err := withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, exceptTokenHash)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("sessions.delete_all: %w", err)
	}
	return n, nil
}

// DeleteExpired remove sessões expiradas.
func (s *SessionStore) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT qd_delete_expired_sessions($1)`, now).Scan(&n)
	return n, err
}
