package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// ---------------------------------------------------------------------------
// Tentativas de login
// ---------------------------------------------------------------------------

// LoginAttemptStore implementa ports.LoginAttemptStore no PostgreSQL.
type LoginAttemptStore struct {
	db *pgxpool.Pool
}

var _ ports.LoginAttemptStore = (*LoginAttemptStore)(nil)

// NewLoginAttemptStore cria o store.
func NewLoginAttemptStore(db *pgxpool.Pool) *LoginAttemptStore { return &LoginAttemptStore{db: db} }

func attemptKey(key string) string {
	sum := sha256.Sum256([]byte("login:" + key))
	return hex.EncodeToString(sum[:])
}

// Get devolve o estado atual (zero se não houver registro).
func (s *LoginAttemptStore) Get(ctx context.Context, key string) (domain.LoginAttemptState, error) {
	var st domain.LoginAttemptState
	var locked *time.Time
	err := s.db.QueryRow(ctx, `SELECT failures, locked_until FROM login_attempts WHERE key_hash = $1`, attemptKey(key)).
		Scan(&st.Failures, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	if locked != nil {
		st.LockedUntil = *locked
	}
	return st, err
}

// RegisterFailure incrementa o contador atomicamente (reinicia fora da janela).
func (s *LoginAttemptStore) RegisterFailure(ctx context.Context, key string, now time.Time, window time.Duration) (int, error) {
	var failures int
	err := s.db.QueryRow(ctx, `
		INSERT INTO login_attempts (key_hash, failures, first_failure_at, updated_at)
		VALUES ($1, 1, $2, $2)
		ON CONFLICT (key_hash) DO UPDATE SET
			failures         = CASE WHEN login_attempts.first_failure_at < $3 THEN 1 ELSE login_attempts.failures + 1 END,
			locked_until     = CASE WHEN login_attempts.first_failure_at < $3 THEN NULL ELSE login_attempts.locked_until END,
			first_failure_at = CASE WHEN login_attempts.first_failure_at < $3 THEN $2 ELSE login_attempts.first_failure_at END,
			updated_at       = $2
		RETURNING failures`,
		attemptKey(key), now, now.Add(-window)).Scan(&failures)
	if err != nil {
		return 0, fmt.Errorf("login_attempts.failure: %w", err)
	}
	return failures, nil
}

// Lock bloqueia a chave até `until`.
func (s *LoginAttemptStore) Lock(ctx context.Context, key string, until time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE login_attempts SET locked_until = $2, updated_at = now() WHERE key_hash = $1`, attemptKey(key), until)
	return err
}

// Reset apaga o histórico da chave (login bem-sucedido).
func (s *LoginAttemptStore) Reset(ctx context.Context, key string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM login_attempts WHERE key_hash = $1`, attemptKey(key))
	return err
}

// DeleteStale remove registros sem atividade desde `before` (job de limpeza).
func (s *LoginAttemptStore) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM login_attempts WHERE updated_at < $1 AND (locked_until IS NULL OR locked_until < now())`, before)
	return tag.RowsAffected(), err
}

// ---------------------------------------------------------------------------
// Verificação de telefone
// ---------------------------------------------------------------------------

// PhoneVerificationStore implementa ports.PhoneVerificationStore (com RLS).
type PhoneVerificationStore struct {
	db *pgxpool.Pool
}

var _ ports.PhoneVerificationStore = (*PhoneVerificationStore)(nil)

// NewPhoneVerificationStore cria o store.
func NewPhoneVerificationStore(db *pgxpool.Pool) *PhoneVerificationStore {
	return &PhoneVerificationStore{db: db}
}

// Save cria ou substitui o desafio do usuário (zera tentativas).
func (s *PhoneVerificationStore) Save(ctx context.Context, v *domain.PhoneVerification) error {
	return withTenant(ctx, s.db, v.UserID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO phone_verifications (user_id, telefone, metodo, code_hash, attempts, expires_at, created_at)
			VALUES ($1, $2, $3, $4, 0, $5, $6)
			ON CONFLICT (user_id) DO UPDATE SET
				telefone = EXCLUDED.telefone, metodo = EXCLUDED.metodo, code_hash = EXCLUDED.code_hash,
				attempts = 0, expires_at = EXCLUDED.expires_at, created_at = EXCLUDED.created_at`,
			v.UserID, v.Telefone, string(v.Metodo), v.CodeHash, v.ExpiresAt, v.CreatedAt)
		return err
	})
}

// Get devolve o desafio pendente.
func (s *PhoneVerificationStore) Get(ctx context.Context, userID string) (*domain.PhoneVerification, error) {
	v := &domain.PhoneVerification{UserID: userID}
	err := withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		var metodo string
		err := tx.QueryRow(ctx, `
			SELECT telefone, metodo, code_hash, attempts, expires_at, created_at
			FROM phone_verifications WHERE user_id = $1`, userID).
			Scan(&v.Telefone, &metodo, &v.CodeHash, &v.Attempts, &v.ExpiresAt, &v.CreatedAt)
		v.Metodo = domain.MetodoVerificacao(metodo)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("phone_verifications.get: %w", err)
	}
	return v, nil
}

// IncrementAttempts soma uma tentativa errada e devolve o total.
func (s *PhoneVerificationStore) IncrementAttempts(ctx context.Context, userID string) (int, error) {
	var n int
	err := withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE phone_verifications SET attempts = attempts + 1 WHERE user_id = $1 RETURNING attempts`, userID).Scan(&n)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	return n, err
}

// Delete remove o desafio.
func (s *PhoneVerificationStore) Delete(ctx context.Context, userID string) error {
	return withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM phone_verifications WHERE user_id = $1`, userID)
		return err
	})
}

// EmailVerificationStore implementa ports.EmailVerificationStore (com RLS).
type EmailVerificationStore struct {
	db *pgxpool.Pool
}

var _ ports.EmailVerificationStore = (*EmailVerificationStore)(nil)

// NewEmailVerificationStore cria o store.
func NewEmailVerificationStore(db *pgxpool.Pool) *EmailVerificationStore {
	return &EmailVerificationStore{db: db}
}

// Save cria ou substitui o desafio do usuário (zera tentativas).
func (s *EmailVerificationStore) Save(ctx context.Context, v *domain.EmailVerification) error {
	return withTenant(ctx, s.db, v.UserID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO email_verifications (user_id, email, code_hash, attempts, expires_at, created_at)
			VALUES ($1, $2, $3, 0, $4, $5)
			ON CONFLICT (user_id) DO UPDATE SET
				email = EXCLUDED.email, code_hash = EXCLUDED.code_hash,
				attempts = 0, expires_at = EXCLUDED.expires_at, created_at = EXCLUDED.created_at`,
			v.UserID, v.Email, v.CodeHash, v.ExpiresAt, v.CreatedAt)
		return err
	})
}

// Get devolve o desafio pendente.
func (s *EmailVerificationStore) Get(ctx context.Context, userID string) (*domain.EmailVerification, error) {
	v := &domain.EmailVerification{UserID: userID}
	err := withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT email, code_hash, attempts, expires_at, created_at
			FROM email_verifications WHERE user_id = $1`, userID).
			Scan(&v.Email, &v.CodeHash, &v.Attempts, &v.ExpiresAt, &v.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("email_verifications.get: %w", err)
	}
	return v, nil
}

// IncrementAttempts soma uma tentativa errada e devolve o total.
func (s *EmailVerificationStore) IncrementAttempts(ctx context.Context, userID string) (int, error) {
	var n int
	err := withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE email_verifications SET attempts = attempts + 1 WHERE user_id = $1 RETURNING attempts`, userID).Scan(&n)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	return n, err
}

// Delete remove o desafio.
func (s *EmailVerificationStore) Delete(ctx context.Context, userID string) error {
	return withTenant(ctx, s.db, userID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM email_verifications WHERE user_id = $1`, userID)
		return err
	})
}
