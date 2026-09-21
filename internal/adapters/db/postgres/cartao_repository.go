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

// CartaoRepository implementa ports.CartaoRepository.
type CartaoRepository struct {
	db *pgxpool.Pool
}

var _ ports.CartaoRepository = (*CartaoRepository)(nil)

// NewCartaoRepository cria o repositório.
func NewCartaoRepository(db *pgxpool.Pool) *CartaoRepository { return &CartaoRepository{db: db} }

const cartaoColumns = `id, user_id, nome, banco, dia_fechamento, dia_vencimento,
	limite_centavos, ativo, created_at`

func scanCartao(row pgx.Row) (*domain.Cartao, error) {
	var c domain.Cartao
	var limite int64
	if err := row.Scan(&c.ID, &c.UserID, &c.Nome, &c.Banco, &c.DiaFechamento,
		&c.DiaVencimento, &limite, &c.Ativo, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.Limite = domain.Money(limite)
	return &c, nil
}

// Create insere o cartão.
func (r *CartaoRepository) Create(ctx context.Context, userID string, c *domain.Cartao) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO cartoes (user_id, nome, banco, dia_fechamento, dia_vencimento, limite_centavos, ativo)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, created_at`,
			userID, c.Nome, c.Banco, c.DiaFechamento, c.DiaVencimento, int64(c.Limite), c.Ativo,
		).Scan(&c.ID, &c.CreatedAt)
	})
	if err != nil {
		return fmt.Errorf("cartoes.create: %w", err)
	}
	c.UserID = userID
	return nil
}

// FindByID busca um cartão do usuário.
func (r *CartaoRepository) FindByID(ctx context.Context, userID, id string) (*domain.Cartao, error) {
	var c *domain.Cartao
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var err error
		c, err = scanCartao(tx.QueryRow(ctx,
			`SELECT `+cartaoColumns+` FROM cartoes WHERE user_id = $1 AND id = $2`, userID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cartoes.find: %w", err)
	}
	return c, nil
}

// List devolve os cartões do usuário: ativos primeiro, mais recentes antes.
func (r *CartaoRepository) List(ctx context.Context, userID string) ([]*domain.Cartao, error) {
	out := []*domain.Cartao{}
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+cartaoColumns+` FROM cartoes
			WHERE user_id = $1 ORDER BY ativo DESC, created_at DESC LIMIT 100`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCartao(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("cartoes.list: %w", err)
	}
	return out, nil
}

// Update altera os dados do cartão.
func (r *CartaoRepository) Update(ctx context.Context, userID string, c *domain.Cartao) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE cartoes SET nome = $3, banco = $4, dia_fechamento = $5,
			       dia_vencimento = $6, limite_centavos = $7, ativo = $8
			WHERE user_id = $1 AND id = $2`,
			userID, c.ID, c.Nome, c.Banco, c.DiaFechamento, c.DiaVencimento, int64(c.Limite), c.Ativo)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("cartoes.update: %w", err)
	}
	return err
}

// Delete remove o cartão (o serviço só chama sem histórico).
func (r *CartaoRepository) Delete(ctx context.Context, userID, id string) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM cartoes WHERE user_id = $1 AND id = $2`, userID, id)
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
		return fmt.Errorf("cartoes.delete: %w", err)
	}
	return err
}

// ---------------------------------------------------------------------------
// Compras
// ---------------------------------------------------------------------------

const compraColumns = `id, user_id, cartao_id, grupo_id, valor_centavos, categoria,
	descricao, data_compra, numero_parcela, total_parcelas, fatura_vencimento, created_at`

func scanCompra(rows pgx.Rows) (*domain.CompraCartao, error) {
	var c domain.CompraCartao
	var valor int64
	if err := rows.Scan(&c.ID, &c.UserID, &c.CartaoID, &c.GrupoID, &valor, &c.Categoria,
		&c.Descricao, &c.DataCompra, &c.NumeroParcela, &c.TotalParcelas,
		&c.FaturaVencimento, &c.CreatedAt); err != nil {
		return nil, err
	}
	c.Valor = domain.Money(valor)
	return &c, nil
}

// CreateCompras grava todas as parcelas numa transação só: ou a compra inteira
// entra, ou nada entra.
func (r *CartaoRepository) CreateCompras(ctx context.Context, userID string, compras []*domain.CompraCartao) error {
	if len(compras) == 0 {
		return nil
	}
	grupo, err := newUUID()
	if err != nil {
		return fmt.Errorf("compras.create: uuid: %w", err)
	}
	err = withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		for _, c := range compras {
			c.GrupoID = grupo
			if err := tx.QueryRow(ctx, `
				INSERT INTO compras_cartao
					(user_id, cartao_id, grupo_id, valor_centavos, categoria, descricao,
					 data_compra, numero_parcela, total_parcelas, fatura_vencimento)
				VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8, $9, $10::date)
				RETURNING id, created_at`,
				userID, c.CartaoID, grupo, int64(c.Valor), c.Categoria, c.Descricao,
				dateParam(c.DataCompra), c.NumeroParcela, c.TotalParcelas,
				dateParam(c.FaturaVencimento),
			).Scan(&c.ID, &c.CreatedAt); err != nil {
				return err
			}
			c.UserID = userID
		}
		return nil
	})
	if isInvalidInput(err) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("compras.create: %w", err)
	}
	return nil
}

// ComprasDaFatura devolve as parcelas de um vencimento, na ordem da compra.
func (r *CartaoRepository) ComprasDaFatura(ctx context.Context, userID, cartaoID string, vencimento time.Time) ([]*domain.CompraCartao, error) {
	out := []*domain.CompraCartao{}
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+compraColumns+` FROM compras_cartao
			WHERE user_id = $1 AND cartao_id = $2 AND fatura_vencimento = $3::date
			ORDER BY data_compra, created_at`, userID, cartaoID, dateParam(vencimento))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCompra(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("compras.fatura: %w", err)
	}
	return out, nil
}

// ResumoDasFaturas traz total e pagamento de cada fatura numa consulta só.
//
// Antes isto era um laço chamando duas consultas por fatura — com um ano de
// cartão, ~24 idas ao banco a cada abertura da Home.
func (r *CartaoRepository) ResumoDasFaturas(ctx context.Context, userID, cartaoID string) ([]domain.FaturaResumo, error) {
	out := []domain.FaturaResumo{}
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.fatura_vencimento, sum(c.valor_centavos),
			       p.transacao_id, p.valor_centavos, p.pago_em
			FROM compras_cartao c
			LEFT JOIN pagamentos_fatura p
			  ON p.user_id = c.user_id AND p.cartao_id = c.cartao_id
			 AND p.vencimento = c.fatura_vencimento
			WHERE c.user_id = $1 AND c.cartao_id = $2
			GROUP BY c.fatura_vencimento, p.transacao_id, p.valor_centavos, p.pago_em
			ORDER BY c.fatura_vencimento DESC
			LIMIT 120`, userID, cartaoID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f domain.FaturaResumo
			var total int64
			var transacaoID *string
			var valorPago *int64
			var pagoEm *time.Time
			if err := rows.Scan(&f.Vencimento, &total, &transacaoID, &valorPago, &pagoEm); err != nil {
				return err
			}
			f.Total = domain.Money(total)
			if transacaoID != nil && pagoEm != nil {
				f.Pagamento = &domain.PagamentoFatura{
					UserID: userID, CartaoID: cartaoID, Vencimento: f.Vencimento,
					TransacaoID: *transacaoID, PagoEm: *pagoEm,
				}
				if valorPago != nil {
					f.Pagamento.Valor = domain.Money(*valorPago)
				}
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("compras.resumo_faturas: %w", err)
	}
	return out, nil
}

// TotalNaoPago soma as compras cujas faturas ainda não foram pagas — é o que
// está comprometido do limite.
func (r *CartaoRepository) TotalNaoPago(ctx context.Context, userID, cartaoID string) (domain.Money, error) {
	var total int64
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE(sum(c.valor_centavos), 0)
			FROM compras_cartao c
			WHERE c.user_id = $1 AND c.cartao_id = $2
			  AND NOT EXISTS (
			      SELECT 1 FROM pagamentos_fatura p
			      WHERE p.user_id = c.user_id AND p.cartao_id = c.cartao_id
			        AND p.vencimento = c.fatura_vencimento)`,
			userID, cartaoID).Scan(&total)
	})
	if isInvalidInput(err) {
		return 0, domain.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("compras.nao_pago: %w", err)
	}
	return domain.Money(total), nil
}

// GrupoTemFaturaPaga verifica se a compra toca alguma fatura já paga.
func (r *CartaoRepository) GrupoTemFaturaPaga(ctx context.Context, userID, grupoID string) (bool, error) {
	var existe bool
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM compras_cartao c
				JOIN pagamentos_fatura p
				  ON p.user_id = c.user_id AND p.cartao_id = c.cartao_id
				 AND p.vencimento = c.fatura_vencimento
				WHERE c.user_id = $1 AND c.grupo_id = $2)`,
			userID, grupoID).Scan(&existe)
	})
	if isInvalidInput(err) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("compras.grupo_pago: %w", err)
	}
	return existe, nil
}

// DeleteCompraGrupo remove todas as parcelas de uma compra.
func (r *CartaoRepository) DeleteCompraGrupo(ctx context.Context, userID, grupoID string) (int64, error) {
	var n int64
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM compras_cartao WHERE user_id = $1 AND grupo_id = $2`,
			userID, grupoID)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return nil
	})
	if isInvalidInput(err) {
		return 0, domain.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("compras.delete: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Pagamento da fatura
// ---------------------------------------------------------------------------

const pagamentoColumns = `user_id, cartao_id, vencimento, transacao_id, valor_centavos, pago_em, created_at`

func scanPagamento(row pgx.Row) (*domain.PagamentoFatura, error) {
	var p domain.PagamentoFatura
	var valor int64
	if err := row.Scan(&p.UserID, &p.CartaoID, &p.Vencimento, &p.TransacaoID,
		&valor, &p.PagoEm, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.Valor = domain.Money(valor)
	return &p, nil
}

// PagamentoDaFatura devolve ErrNotFound quando a fatura não foi paga.
func (r *CartaoRepository) PagamentoDaFatura(ctx context.Context, userID, cartaoID string, vencimento time.Time) (*domain.PagamentoFatura, error) {
	var p *domain.PagamentoFatura
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var err error
		p, err = scanPagamento(tx.QueryRow(ctx, `SELECT `+pagamentoColumns+` FROM pagamentos_fatura
			WHERE user_id = $1 AND cartao_id = $2 AND vencimento = $3::date`,
			userID, cartaoID, dateParam(vencimento)))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pagamentos.find: %w", err)
	}
	return p, nil
}

// RegistrarPagamento grava a saída no saldo e o pagamento juntos: ou os dois
// entram, ou nenhum — senão sobraria uma fatura paga sem dinheiro saindo.
func (r *CartaoRepository) RegistrarPagamento(ctx context.Context, userID string, t *domain.Transacao, pag *domain.PagamentoFatura) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		if err := insertTransacao(ctx, tx, userID, t); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO pagamentos_fatura
				(user_id, cartao_id, vencimento, transacao_id, valor_centavos, pago_em)
			VALUES ($1, $2, $3::date, $4, $5, $6::date)`,
			userID, pag.CartaoID, dateParam(pag.Vencimento), t.ID, int64(pag.Valor), dateParam(pag.PagoEm))
		if c, ok := uniqueViolation(err); ok && c == "pagamentos_fatura_pkey" {
			return domain.ErrFaturaJaPaga
		}
		return err
	})
	if isInvalidInput(err) {
		return domain.ErrNotFound
	}
	if err != nil && !errors.Is(err, domain.ErrFaturaJaPaga) {
		return fmt.Errorf("pagamentos.registrar: %w", err)
	}
	return err
}

// RemoverPagamento apaga o pagamento e a saída correspondente.
func (r *CartaoRepository) RemoverPagamento(ctx context.Context, userID, cartaoID string, vencimento time.Time) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var transacaoID string
		if err := tx.QueryRow(ctx, `
			DELETE FROM pagamentos_fatura
			WHERE user_id = $1 AND cartao_id = $2 AND vencimento = $3::date
			RETURNING transacao_id`,
			userID, cartaoID, dateParam(vencimento)).Scan(&transacaoID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM transacoes WHERE user_id = $1 AND id = $2`, userID, transacaoID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || isInvalidInput(err) {
		return domain.ErrFaturaNaoPaga
	}
	if err != nil {
		return fmt.Errorf("pagamentos.remover: %w", err)
	}
	return nil
}
