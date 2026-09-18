package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// TransacaoRepository implementa ports.TransacaoRepository.
//
// Defesa em profundidade: toda query roda dentro de withTenant (RLS) E ainda
// filtra explicitamente por `user_id = $1`.
type TransacaoRepository struct {
	db *pgxpool.Pool
}

var _ ports.TransacaoRepository = (*TransacaoRepository)(nil)

// NewTransacaoRepository cria o repositório.
func NewTransacaoRepository(db *pgxpool.Pool) *TransacaoRepository {
	return &TransacaoRepository{db: db}
}

const transacaoColumns = `id, user_id, tipo, valor_centavos, categoria, descricao, data_transacao,
	origem, COALESCE(external_id, ''), parcelamento_id, numero_parcela, created_at`

func scanTransacao(row pgx.Row) (*domain.Transacao, error) {
	var t domain.Transacao
	var tipo, origem string
	var valor int64
	var parcID *string
	var numParc *int32
	if err := row.Scan(&t.ID, &t.UserID, &tipo, &valor, &t.Categoria, &t.Descricao, &t.Data,
		&origem, &t.ExternalID, &parcID, &numParc, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.Tipo = domain.TipoTransacao(tipo)
	t.Origem = domain.OrigemTransacao(origem)
	t.Valor = domain.Money(valor)
	t.ParcelamentoID = parcID
	if numParc != nil {
		n := int(*numParc)
		t.NumeroParcela = &n
	}
	return &t, nil
}

func scanTransacoes(rows pgx.Rows) ([]*domain.Transacao, error) {
	defer rows.Close()
	out := []*domain.Transacao{}
	for rows.Next() {
		t, err := scanTransacao(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func insertTransacao(ctx context.Context, tx pgx.Tx, userID string, t *domain.Transacao) error {
	var externalID *string
	if t.ExternalID != "" {
		externalID = &t.ExternalID
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO transacoes
			(user_id, tipo, valor_centavos, categoria, descricao, data_transacao, origem,
			 external_id, parcelamento_id, numero_parcela)
		VALUES ($1, $2, $3, $4, $5, $6::date, $7, $8, $9, $10)
		RETURNING id, created_at`,
		userID, string(t.Tipo), int64(t.Valor), t.Categoria, t.Descricao, dateParam(t.Data),
		string(t.Origem), externalID, t.ParcelamentoID, t.NumeroParcela,
	).Scan(&t.ID, &t.CreatedAt)
	if c, ok := uniqueViolation(err); ok && c == "transacoes_external_id_uq" {
		return domain.ErrDuplicateMessage
	}
	if err != nil {
		return err
	}
	t.UserID = userID
	return nil
}

// Create insere um lançamento para o usuário informado.
func (r *TransacaoRepository) Create(ctx context.Context, userID string, t *domain.Transacao) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error { return insertTransacao(ctx, tx, userID, t) })
	if err != nil && !errors.Is(err, domain.ErrDuplicateMessage) {
		return fmt.Errorf("transacoes.create: %w", err)
	}
	return err
}

// FindByID busca um lançamento do usuário.
func (r *TransacaoRepository) FindByID(ctx context.Context, userID, id string) (*domain.Transacao, error) {
	var t *domain.Transacao
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var err error
		t, err = scanTransacao(tx.QueryRow(ctx, `SELECT `+transacaoColumns+` FROM transacoes WHERE user_id = $1 AND id = $2`, userID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("transacoes.find: %w", err)
	}
	return t, nil
}

// Update grava os campos editáveis.
func (r *TransacaoRepository) Update(ctx context.Context, userID string, t *domain.Transacao) error {
	return withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE transacoes
			SET tipo = $3, valor_centavos = $4, categoria = $5, descricao = $6, data_transacao = $7::date
			WHERE user_id = $1 AND id = $2`,
			userID, t.ID, string(t.Tipo), int64(t.Valor), t.Categoria, t.Descricao, dateParam(t.Data))
		if err != nil {
			return fmt.Errorf("transacoes.update: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// Delete remove um lançamento.
func (r *TransacaoRepository) Delete(ctx context.Context, userID, id string) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM transacoes WHERE user_id = $1 AND id = $2`, userID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if isInvalidInput(err) {
		return domain.ErrNotFound
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("transacoes.delete: %w", err)
	}
	return err
}

// List pagina os lançamentos do usuário com filtros.
func (r *TransacaoRepository) List(ctx context.Context, userID string, f ports.TransacaoFilter) ([]*domain.Transacao, int64, error) {
	where := []string{"user_id = $1", "data_transacao >= $2::date", "data_transacao < $3::date"}
	args := []any{userID, dateParam(f.Inicio), dateParam(f.Fim)}
	if f.Categoria != "" {
		args = append(args, f.Categoria)
		where = append(where, "categoria = $"+strconv.Itoa(len(args)))
	}
	if f.Tipo != "" {
		args = append(args, string(f.Tipo))
		where = append(where, "tipo = $"+strconv.Itoa(len(args)))
	}
	cond := strings.Join(where, " AND ")

	var items []*domain.Transacao
	var total int64
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM transacoes WHERE `+cond, args...).Scan(&total); err != nil {
			return err
		}
		if total == 0 {
			items = []*domain.Transacao{}
			return nil
		}
		pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)
		rows, err := tx.Query(ctx, `
			SELECT `+transacaoColumns+`
			FROM transacoes
			WHERE `+cond+`
			ORDER BY data_transacao DESC, created_at DESC
			LIMIT $`+strconv.Itoa(len(pageArgs)-1)+` OFFSET $`+strconv.Itoa(len(pageArgs)), pageArgs...)
		if err != nil {
			return err
		}
		items, err = scanTransacoes(rows)
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("transacoes.list: %w", err)
	}
	return items, total, nil
}

// Totais agrega o intervalo [inicio, fim).
func (r *TransacaoRepository) Totais(ctx context.Context, userID string, inicio, fim time.Time) (domain.TotaisPeriodo, error) {
	var e, s, rend, parc, qtd int64
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(valor_centavos) FILTER (WHERE tipo = 'entrada'), 0),
				COALESCE(SUM(valor_centavos) FILTER (WHERE tipo = 'saida'), 0),
				COALESCE(SUM(valor_centavos) FILTER (WHERE tipo = 'entrada' AND categoria = $4), 0),
				COALESCE(SUM(valor_centavos) FILTER (WHERE tipo = 'saida' AND parcelamento_id IS NOT NULL), 0),
				count(*)
			FROM transacoes
			WHERE user_id = $1 AND data_transacao >= $2::date AND data_transacao < $3::date`,
			userID, dateParam(inicio), dateParam(fim), domain.CategoriaRendimentos,
		).Scan(&e, &s, &rend, &parc, &qtd)
	})
	if err != nil {
		return domain.TotaisPeriodo{}, fmt.Errorf("transacoes.totais: %w", err)
	}
	return domain.TotaisPeriodo{
		Entradas: domain.Money(e), Saidas: domain.Money(s), Rendimentos: domain.Money(rend),
		SaidasParceladas: domain.Money(parc), QtdTransacoes: qtd,
	}, nil
}

// SaldoAte soma (entradas - saídas) com data < ate.
func (r *TransacaoRepository) SaldoAte(ctx context.Context, userID string, ate time.Time) (domain.Money, error) {
	var saldo int64
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(CASE WHEN tipo = 'entrada' THEN valor_centavos ELSE -valor_centavos END), 0)
			FROM transacoes
			WHERE user_id = $1 AND data_transacao < $2::date`,
			userID, dateParam(ate),
		).Scan(&saldo)
	})
	if err != nil {
		return 0, fmt.Errorf("transacoes.saldo: %w", err)
	}
	return domain.Money(saldo), nil
}

// TotaisPorCategoria agrega por categoria e tipo.
func (r *TransacaoRepository) TotaisPorCategoria(ctx context.Context, userID string, inicio, fim time.Time) ([]domain.TotalCategoria, error) {
	out := []domain.TotalCategoria{}
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT categoria, tipo, SUM(valor_centavos), count(*)
			FROM transacoes
			WHERE user_id = $1 AND data_transacao >= $2::date AND data_transacao < $3::date
			GROUP BY categoria, tipo
			ORDER BY tipo, SUM(valor_centavos) DESC`,
			userID, dateParam(inicio), dateParam(fim))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.TotalCategoria
			var tipo string
			var total int64
			if err := rows.Scan(&c.Categoria, &tipo, &total, &c.Qtd); err != nil {
				return err
			}
			c.Tipo, c.Total = domain.TipoTransacao(tipo), domain.Money(total)
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("transacoes.por_categoria: %w", err)
	}
	return out, nil
}

// ListParcelas lista as parcelas (de parcelamentos) no intervalo.
func (r *TransacaoRepository) ListParcelas(ctx context.Context, userID string, inicio, fim time.Time) ([]*domain.Transacao, error) {
	var out []*domain.Transacao
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+transacaoColumns+`
			FROM transacoes
			WHERE user_id = $1 AND parcelamento_id IS NOT NULL
			  AND data_transacao >= $2::date AND data_transacao < $3::date
			ORDER BY data_transacao, descricao`,
			userID, dateParam(inicio), dateParam(fim))
		if err != nil {
			return err
		}
		out, err = scanTransacoes(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("transacoes.parcelas: %w", err)
	}
	return out, nil
}
