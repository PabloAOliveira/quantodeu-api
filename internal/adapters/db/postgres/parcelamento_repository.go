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

// ParcelamentoRepository implementa ports.ParcelamentoRepository.
type ParcelamentoRepository struct {
	db *pgxpool.Pool
}

var _ ports.ParcelamentoRepository = (*ParcelamentoRepository)(nil)

// NewParcelamentoRepository cria o repositório.
func NewParcelamentoRepository(db *pgxpool.Pool) *ParcelamentoRepository {
	return &ParcelamentoRepository{db: db}
}

const parcelamentoColumns = `id, user_id, tipo, descricao, categoria, valor_total_centavos, valor_parcela_centavos,
	total_parcelas, data_primeira_parcela, parcelas_ja_pagas, created_at`

func scanParcelamento(row pgx.Row) (*domain.Parcelamento, error) {
	var p domain.Parcelamento
	var tipo string
	var total, parcela int64
	if err := row.Scan(&p.ID, &p.UserID, &tipo, &p.Descricao, &p.Categoria, &total, &parcela,
		&p.TotalParcelas, &p.DataPrimeiraParcela, &p.ParcelasJaPagas, &p.CreatedAt); err != nil {
		return nil, err
	}
	p.Tipo, p.ValorTotal, p.ValorParcela = domain.TipoParcelamento(tipo), domain.Money(total), domain.Money(parcela)
	return &p, nil
}

// upsertFinanciamento grava (ou remove) o contrato do parcelamento.
func upsertFinanciamento(ctx context.Context, tx pgx.Tx, userID, parcelamentoID string, f *domain.Financiamento) error {
	if f == nil {
		_, err := tx.Exec(ctx, `DELETE FROM financiamentos WHERE user_id = $1 AND parcelamento_id = $2`, userID, parcelamentoID)
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO financiamentos (parcelamento_id, user_id, banco, sistema, valor_financiado_centavos, taxa_juros_anual, tipo_taxa, pagamento_extra_centavos, prazo_contratado)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (parcelamento_id) DO UPDATE SET
			banco = EXCLUDED.banco, sistema = EXCLUDED.sistema,
			valor_financiado_centavos = EXCLUDED.valor_financiado_centavos,
			taxa_juros_anual = EXCLUDED.taxa_juros_anual, tipo_taxa = EXCLUDED.tipo_taxa,
			pagamento_extra_centavos = EXCLUDED.pagamento_extra_centavos,
			prazo_contratado = EXCLUDED.prazo_contratado`,
		parcelamentoID, userID, f.Banco, string(f.Sistema), int64(f.ValorFinanciado), f.TaxaAnual, string(f.TipoTaxa), int64(f.ExtraMensal), f.PrazoContratado)
	return err
}

// loadFinanciamento devolve nil quando o parcelamento não é financiamento.
func loadFinanciamento(ctx context.Context, tx pgx.Tx, userID, parcelamentoID string) (*domain.Financiamento, error) {
	var f domain.Financiamento
	var sistema, tipoTaxa string
	var valor, extra int64
	err := tx.QueryRow(ctx, `
		SELECT banco, sistema, valor_financiado_centavos, taxa_juros_anual, tipo_taxa, pagamento_extra_centavos, prazo_contratado
		FROM financiamentos WHERE user_id = $1 AND parcelamento_id = $2`, userID, parcelamentoID).
		Scan(&f.Banco, &sistema, &valor, &f.TaxaAnual, &tipoTaxa, &extra, &f.PrazoContratado)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.Sistema, f.TipoTaxa = domain.SistemaAmortizacao(sistema), domain.TipoTaxa(tipoTaxa)
	f.ValorFinanciado, f.ExtraMensal = domain.Money(valor), domain.Money(extra)
	return &f, nil
}

func insertParcelas(ctx context.Context, tx pgx.Tx, userID, parcelamentoID string, parcelas []*domain.Transacao) error {
	// Até 420 linhas numa única transação; precisamos dos IDs gerados.
	for _, t := range parcelas {
		id := parcelamentoID
		t.ParcelamentoID = &id
		if err := insertTransacao(ctx, tx, userID, t); err != nil {
			return fmt.Errorf("inserir parcela %d: %w", derefInt(t.NumeroParcela), err)
		}
	}
	return nil
}

// CreateWithParcelas grava o parcelamento e as parcelas atomicamente.
func (r *ParcelamentoRepository) CreateWithParcelas(ctx context.Context, userID string, p *domain.Parcelamento, parcelas []*domain.Transacao) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO parcelamentos
				(user_id, tipo, descricao, categoria, valor_total_centavos, valor_parcela_centavos,
				 total_parcelas, data_primeira_parcela, parcelas_ja_pagas)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, $9)
			RETURNING id, created_at`,
			userID, string(p.Tipo), p.Descricao, p.Categoria, int64(p.ValorTotal), int64(p.ValorParcela),
			p.TotalParcelas, dateParam(p.DataPrimeiraParcela), p.ParcelasJaPagas,
		).Scan(&p.ID, &p.CreatedAt); err != nil {
			return fmt.Errorf("inserir parcelamento: %w", err)
		}
		p.UserID = userID
		if err := upsertFinanciamento(ctx, tx, userID, p.ID, p.Financiamento); err != nil {
			return fmt.Errorf("inserir financiamento: %w", err)
		}
		return insertParcelas(ctx, tx, userID, p.ID, parcelas)
	})
	if err != nil {
		return fmt.Errorf("parcelamentos.create: %w", err)
	}
	return nil
}

// FindByID busca um parcelamento do usuário.
func (r *ParcelamentoRepository) FindByID(ctx context.Context, userID, id string) (*domain.Parcelamento, error) {
	var p *domain.Parcelamento
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var err error
		p, err = scanParcelamento(tx.QueryRow(ctx, `SELECT `+parcelamentoColumns+` FROM parcelamentos WHERE user_id = $1 AND id = $2`, userID, id))
		if err != nil {
			return err
		}
		p.Financiamento, err = loadFinanciamento(ctx, tx, userID, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("parcelamentos.find: %w", err)
	}
	return p, nil
}

// ListParcelasDoParcelamento devolve as parcelas em ordem.
func (r *ParcelamentoRepository) ListParcelasDoParcelamento(ctx context.Context, userID, id string) ([]*domain.Transacao, error) {
	var out []*domain.Transacao
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+transacaoColumns+`
			FROM transacoes
			WHERE user_id = $1 AND parcelamento_id = $2
			ORDER BY numero_parcela`, userID, id)
		if err != nil {
			return err
		}
		out, err = scanTransacoes(rows)
		return err
	})
	if isInvalidInput(err) {
		return nil, domain.ErrNotFound
	}
	return out, err
}

// ReplaceWithParcelas atualiza o parcelamento e regera todas as parcelas.
func (r *ParcelamentoRepository) ReplaceWithParcelas(ctx context.Context, userID string, p *domain.Parcelamento, parcelas []*domain.Transacao) error {
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE parcelamentos
			SET tipo = $3, descricao = $4, categoria = $5, valor_total_centavos = $6,
			    valor_parcela_centavos = $7, total_parcelas = $8, data_primeira_parcela = $9::date,
			    parcelas_ja_pagas = $10
			WHERE user_id = $1 AND id = $2`,
			userID, p.ID, string(p.Tipo), p.Descricao, p.Categoria, int64(p.ValorTotal), int64(p.ValorParcela),
			p.TotalParcelas, dateParam(p.DataPrimeiraParcela), p.ParcelasJaPagas)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM transacoes WHERE user_id = $1 AND parcelamento_id = $2`, userID, p.ID); err != nil {
			return err
		}
		p.UserID = userID
		if err := upsertFinanciamento(ctx, tx, userID, p.ID, p.Financiamento); err != nil {
			return fmt.Errorf("gravar financiamento: %w", err)
		}
		return insertParcelas(ctx, tx, userID, p.ID, parcelas)
	})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("parcelamentos.replace: %w", err)
	}
	return err
}

// Delete remove o parcelamento; parcelas até preservarAte viram lançamentos avulsos.
func (r *ParcelamentoRepository) Delete(ctx context.Context, userID, id string, preservarAte *time.Time) (int64, error) {
	var removidas int64
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM parcelamentos WHERE user_id = $1 AND id = $2)`, userID, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound
		}
		if preservarAte != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE transacoes SET parcelamento_id = NULL, numero_parcela = NULL
				WHERE user_id = $1 AND parcelamento_id = $2 AND data_transacao <= $3::date`,
				userID, id, dateParam(*preservarAte)); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM transacoes WHERE user_id = $1 AND parcelamento_id = $2`, userID, id)
		if err != nil {
			return err
		}
		removidas = tag.RowsAffected()
		_, err = tx.Exec(ctx, `DELETE FROM parcelamentos WHERE user_id = $1 AND id = $2`, userID, id)
		return err
	})
	if isInvalidInput(err) {
		return 0, domain.ErrNotFound
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return 0, fmt.Errorf("parcelamentos.delete: %w", err)
	}
	return removidas, err
}

// List devolve os parcelamentos do usuário, mais recentes primeiro.
func (r *ParcelamentoRepository) List(ctx context.Context, userID string, hoje time.Time) ([]*domain.Parcelamento, error) {
	out := []*domain.Parcelamento{}
	err := withTenant(ctx, r.db, userID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT p.id, p.user_id, p.tipo, p.descricao, p.categoria, p.valor_total_centavos, p.valor_parcela_centavos,
			       p.total_parcelas, p.data_primeira_parcela, p.parcelas_ja_pagas, p.created_at,
			       COALESCE(a.pagas, 0), COALESCE(a.restantes, 0), COALESCE(a.valor_pago, 0), COALESCE(a.valor_restante, 0), a.proxima
			FROM parcelamentos p
			LEFT JOIN LATERAL (
				SELECT count(*) FILTER (WHERE t.data_transacao <= $2::date)                        AS pagas,
				       count(*) FILTER (WHERE t.data_transacao >  $2::date)                        AS restantes,
				       COALESCE(sum(t.valor_centavos) FILTER (WHERE t.data_transacao <= $2::date), 0) AS valor_pago,
				       COALESCE(sum(t.valor_centavos) FILTER (WHERE t.data_transacao >  $2::date), 0) AS valor_restante,
				       min(t.data_transacao) FILTER (WHERE t.data_transacao > $2::date)            AS proxima
				FROM transacoes t
				WHERE t.user_id = p.user_id AND t.parcelamento_id = p.id
			) a ON true
			WHERE p.user_id = $1
			ORDER BY p.created_at DESC
			LIMIT 500`, userID, dateParam(hoje))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p domain.Parcelamento
			var tipo string
			var total, parcela, pago, restante int64
			var pr domain.ProgressoParcelamento
			if err := rows.Scan(&p.ID, &p.UserID, &tipo, &p.Descricao, &p.Categoria, &total, &parcela,
				&p.TotalParcelas, &p.DataPrimeiraParcela, &p.ParcelasJaPagas, &p.CreatedAt,
				&pr.ParcelasPagas, &pr.ParcelasRestantes, &pago, &restante, &pr.ProximaParcela); err != nil {
				return err
			}
			p.Tipo, p.ValorTotal, p.ValorParcela = domain.TipoParcelamento(tipo), domain.Money(total), domain.Money(parcela)
			pr.ValorPago, pr.ValorRestante = domain.Money(pago), domain.Money(restante)
			p.Progresso = &pr
			out = append(out, &p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := carregarFinanciamentos(ctx, tx, userID, out); err != nil {
			return err
		}
		// Depois dos contratos: o valor das parcelas pagas antes do cadastro
		// sai do cronograma, que no financiamento depende do contrato.
		for _, p := range out {
			p.AplicarJaPagas(p.Progresso)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("parcelamentos.list: %w", err)
	}
	return out, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// carregarFinanciamentos anexa os contratos aos parcelamentos da lista.
func carregarFinanciamentos(ctx context.Context, tx pgx.Tx, userID string, ps []*domain.Parcelamento) error {
	if len(ps) == 0 {
		return nil
	}
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT parcelamento_id, banco, sistema, valor_financiado_centavos, taxa_juros_anual, tipo_taxa, pagamento_extra_centavos, prazo_contratado
		FROM financiamentos WHERE user_id = $1 AND parcelamento_id = ANY ($2)`, userID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	porID := map[string]*domain.Financiamento{}
	for rows.Next() {
		var id, sistema, tipoTaxa string
		var f domain.Financiamento
		var valor, extra int64
		if err := rows.Scan(&id, &f.Banco, &sistema, &valor, &f.TaxaAnual, &tipoTaxa, &extra, &f.PrazoContratado); err != nil {
			return err
		}
		f.Sistema, f.TipoTaxa = domain.SistemaAmortizacao(sistema), domain.TipoTaxa(tipoTaxa)
		f.ValorFinanciado, f.ExtraMensal = domain.Money(valor), domain.Money(extra)
		porID[id] = &f
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range ps {
		p.Financiamento = porID[p.ID]
	}
	return nil
}
