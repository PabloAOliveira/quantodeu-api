package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errTenantRequired protege contra chamadas sem usuário (nunca deve ocorrer).
var errTenantRequired = errors.New("postgres: operação de tenant sem user_id")

// withTenant executa fn numa transação com app.user_id = userID.
//
// set_config(..., true) é LOCAL À TRANSAÇÃO: ao fazer COMMIT/ROLLBACK o valor
// some, então a conexão devolvida ao pool não carrega o tenant anterior.
// As políticas RLS da migration 0002 usam esse valor.
func withTenant(ctx context.Context, pool *pgxpool.Pool, userID string, fn func(tx pgx.Tx) error) error {
	if userID == "" {
		return errTenantRequired
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, userID); err != nil {
			return fmt.Errorf("definir tenant: %w", err)
		}
		return fn(tx)
	})
}

// newUUID gera um UUID v4 (usado no cadastro, antes de existir tenant).
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// isInvalidInput detecta IDs malformados (ex.: UUID inválido na URL).
func isInvalidInput(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "22P02" || pgErr.Code == "22007" || pgErr.Code == "22008")
}

// RoleInfo descreve os privilégios da role conectada.
type RoleInfo struct {
	Role       string
	Superuser  bool
	BypassRLS  bool
	OwnsTables bool
}

// RLSEffective informa se o RLS será de fato aplicado a esta conexão.
func (r RoleInfo) RLSEffective() bool { return !r.Superuser && !r.BypassRLS && !r.OwnsTables }

// InspectRole verifica se a role da aplicação está sujeita ao RLS.
func InspectRole(ctx context.Context, pool *pgxpool.Pool) (RoleInfo, error) {
	var info RoleInfo
	err := pool.QueryRow(ctx, `
		SELECT current_user, r.rolsuper, r.rolbypassrls,
		       EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'public' AND tablename = 'transacoes'
		               AND pg_has_role(current_user, tableowner, 'MEMBER'))
		FROM pg_roles r WHERE r.rolname = current_user`).
		Scan(&info.Role, &info.Superuser, &info.BypassRLS, &info.OwnsTables)
	return info, err
}
