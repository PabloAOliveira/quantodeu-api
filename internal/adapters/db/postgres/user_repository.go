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

// UserRepository implementa ports.UserRepository.
type UserRepository struct {
	db *pgxpool.Pool
}

var _ ports.UserRepository = (*UserRepository)(nil)

// NewUserRepository cria o repositório.
func NewUserRepository(db *pgxpool.Pool) *UserRepository { return &UserRepository{db: db} }

const userColumns = `id, nome, email, telefone, password_hash, saldo_inicial_centavos,
	telefone_verificado_em, email_verificado_em, created_at, updated_at`

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var saldo int64
	err := row.Scan(&u.ID, &u.Nome, &u.Email, &u.Telefone, &u.PasswordHash, &saldo,
		&u.TelefoneVerificadoEm, &u.EmailVerificadoEm, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		if isInvalidInput(err) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	u.SaldoInicial = domain.Money(saldo)
	return &u, nil
}

// Create insere um usuário. O UUID é gerado aqui para que a própria inserção
// já aconteça dentro do tenant (a política RLS exige id = app.user_id).
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	id, err := newUUID()
	if err != nil {
		return fmt.Errorf("users.create: uuid: %w", err)
	}
	err = withTenant(ctx, r.db, id, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO users (id, nome, email, telefone, password_hash, saldo_inicial_centavos)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING created_at, updated_at`,
			id, u.Nome, u.Email, u.Telefone, u.PasswordHash, int64(u.SaldoInicial),
		).Scan(&u.CreatedAt, &u.UpdatedAt)
	})
	if c, ok := uniqueViolation(err); ok {
		switch c {
		case "users_email_uq":
			return domain.ErrEmailAlreadyExists
		case "users_telefone_uq":
			return domain.ErrPhoneAlreadyExists
		}
	}
	if err != nil {
		return fmt.Errorf("users.create: %w", err)
	}
	u.ID = id
	return nil
}

// FindByID busca o próprio usuário (tenant = ele mesmo).
func (r *UserRepository) FindByID(ctx context.Context, userID string) (*domain.User, error) {
	var u *domain.User
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, userID))
		return err
	})
	return u, err
}

// FindByEmail usa a função SECURITY DEFINER (login: tenant ainda desconhecido).
func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	return scanUser(r.db.QueryRow(ctx, `SELECT `+userColumns+` FROM qd_find_user_by_email($1)`, email))
}

// FindByPhones usa a função SECURITY DEFINER (webhook: tenant desconhecido).
func (r *UserRepository) FindByPhones(ctx context.Context, phones []string) (*domain.User, error) {
	if len(phones) == 0 {
		return nil, domain.ErrNotFound
	}
	return scanUser(r.db.QueryRow(ctx, `SELECT `+userColumns+` FROM qd_find_user_by_phones($1)`, phones))
}

// UpdatePasswordHash atualiza o hash da senha.
func (r *UserRepository) UpdatePasswordHash(ctx context.Context, userID, hash string) error {
	return r.execOne(ctx, userID, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, hash)
}

// MarkPhoneVerified registra a verificação do telefone.
func (r *UserRepository) MarkPhoneVerified(ctx context.Context, userID string, at time.Time) error {
	return r.execOne(ctx, userID, `UPDATE users SET telefone_verificado_em = $2, updated_at = now() WHERE id = $1`, at)
}

// MarkEmailVerified registra a confirmação do e-mail.
func (r *UserRepository) MarkEmailVerified(ctx context.Context, userID string, at time.Time) error {
	return r.execOne(ctx, userID, `UPDATE users SET email_verificado_em = $2, updated_at = now() WHERE id = $1`, at)
}

func (r *UserRepository) execOne(ctx context.Context, userID, sql string, arg any) error {
	return withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, userID, arg)
		if err != nil {
			return fmt.Errorf("users.update: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}
