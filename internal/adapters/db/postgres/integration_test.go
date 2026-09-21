//go:build integration

// Testes de integração do adaptador PostgreSQL (inclui Row-Level Security).
//
// Execução:
//
//	TEST_DATABASE_URL=postgres://owner@host/db      (dono das tabelas: roda migrations)
//	TEST_APP_DATABASE_URL=postgres://quantodeu_api@host/db   (membro de quantodeu_app)
//	go test -tags=integration ./internal/adapters/db/postgres/
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

func setupPools(t *testing.T) (owner, app *pgxpool.Pool) {
	t.Helper()
	ownerURL, appURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_APP_DATABASE_URL")
	if ownerURL == "" || appURL == "" {
		t.Skip("TEST_DATABASE_URL e TEST_APP_DATABASE_URL não definidos")
	}
	ctx := context.Background()
	owner, err := NewPool(ctx, PoolConfig{URL: ownerURL, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	if err := Migrate(ctx, owner, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	app, err = NewPool(ctx, PoolConfig{URL: appURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	info, err := InspectRole(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if !info.RLSEffective() {
		t.Fatalf("a role do app (%s) ignora RLS: %+v", info.Role, info)
	}
	return owner, app
}

func novoUsuario(t *testing.T, users *UserRepository, prefix string) *domain.User {
	t.Helper()
	suffix := fmt.Sprintf("%08d", time.Now().UnixNano()%100_000_000)
	u := &domain.User{Nome: prefix, Email: prefix + suffix + "@t.com", Telefone: "55119" + suffix, PasswordHash: "x"}
	if prefix == "b" {
		u.Telefone = "55219" + suffix
	}
	if err := users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRepositories_IsolamentoMultiTenant(t *testing.T) {
	_, app := setupPools(t)
	ctx := context.Background()

	users := NewUserRepository(app)
	txs := NewTransacaoRepository(app)
	parcs := NewParcelamentoRepository(app)
	sessions := NewSessionStore(app)

	a, b := novoUsuario(t, users, "a"), novoUsuario(t, users, "b")
	dup := *a
	if err := users.Create(ctx, &dup); !errors.Is(err, domain.ErrEmailAlreadyExists) {
		t.Fatalf("duplicado: %v", err)
	}
	if u, err := users.FindByEmail(ctx, a.Email); err != nil || u.ID != a.ID {
		t.Fatalf("FindByEmail via SECURITY DEFINER: %v", err)
	}
	if u, err := users.FindByPhones(ctx, []string{"000", b.Telefone}); err != nil || u.ID != b.ID {
		t.Fatalf("FindByPhones via SECURITY DEFINER: %v", err)
	}

	hoje := domain.DateOnly(time.Now().UTC())
	tx := &domain.Transacao{Tipo: domain.TipoSaida, Valor: 1000, Categoria: "teste", Data: hoje, Origem: domain.OrigemWhatsApp, ExternalID: "evt-1"}
	if err := txs.Create(ctx, a.ID, tx); err != nil {
		t.Fatal(err)
	}
	again := *tx
	if err := txs.Create(ctx, a.ID, &again); !errors.Is(err, domain.ErrDuplicateMessage) {
		t.Fatalf("idempotência: %v", err)
	}

	p := domain.PeriodoDe(hoje)
	if _, totalB, err := txs.List(ctx, b.ID, ports.TransacaoFilter{Inicio: p.Inicio, Fim: p.Fim, Limit: 10}); err != nil || totalB != 0 {
		t.Fatalf("usuário B enxergou dados de A: %d, err=%v", totalB, err)
	}
	if _, err := txs.FindByID(ctx, b.ID, tx.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B buscou transação de A: %v", err)
	}
	if err := txs.Delete(ctx, b.ID, tx.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B excluiu transação de A: %v", err)
	}
	if _, err := txs.FindByID(ctx, a.ID, "nao-e-uuid"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("id malformado deve virar NotFound: %v", err)
	}

	tx.Valor, tx.Categoria = 2500, "editada"
	if err := txs.Update(ctx, a.ID, tx); err != nil {
		t.Fatal(err)
	}
	if got, _ := txs.FindByID(ctx, a.ID, tx.ID); got.Valor != 2500 {
		t.Fatalf("update não persistiu: %+v", got)
	}

	// Parcelamento: criar, substituir, excluir preservando pagas.
	inicio := hoje.AddDate(0, -1, 0)
	parc, _ := domain.NewParcelamento(domain.NewParcelamentoInput{UserID: a.ID, Descricao: "TV", ValorTotal: 30000, TotalParcelas: 3, DataPrimeiraParcela: inicio})
	if err := parcs.CreateWithParcelas(ctx, a.ID, parc, parc.GerarParcelas()); err != nil {
		t.Fatal(err)
	}
	if list, _ := parcs.List(ctx, b.ID, time.Now()); len(list) != 0 {
		t.Fatal("usuário B enxergou parcelamentos de A")
	}
	if saldo, _ := txs.SaldoAte(ctx, a.ID, p.Fim.AddDate(0, 3, 0)); saldo != -32500 {
		t.Fatalf("saldo A = %d; want -32500", saldo)
	}

	novo, _ := domain.NewParcelamento(domain.NewParcelamentoInput{UserID: a.ID, Descricao: "TV 4K", ValorTotal: 40000, TotalParcelas: 4, DataPrimeiraParcela: inicio})
	novo.ID = parc.ID
	if err := parcs.ReplaceWithParcelas(ctx, a.ID, novo, novo.GerarParcelas()); err != nil {
		t.Fatal(err)
	}
	if ps, _ := parcs.ListParcelasDoParcelamento(ctx, a.ID, parc.ID); len(ps) != 4 {
		t.Fatalf("replace deveria gerar 4 parcelas, gerou %d", len(ps))
	}
	if err := parcs.ReplaceWithParcelas(ctx, b.ID, novo, novo.GerarParcelas()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B alterou parcelamento de A: %v", err)
	}
	if _, err := parcs.Delete(ctx, b.ID, parc.ID, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B excluiu parcelamento de A: %v", err)
	}

	// FK composta impede vincular parcela a um parcelamento de outro usuário.
	parcID, n := parc.ID, 9
	forged := &domain.Transacao{Tipo: domain.TipoSaida, Valor: 1, Categoria: "x", Data: hoje, Origem: domain.OrigemParcelamento, ParcelamentoID: &parcID, NumeroParcela: &n}
	if err := txs.Create(ctx, b.ID, forged); err == nil {
		t.Fatal("FK composta deveria impedir parcela cross-tenant")
	}

	removidas, err := parcs.Delete(ctx, a.ID, parc.ID, &hoje)
	if err != nil {
		t.Fatal(err)
	}
	if removidas != 2 { // parcelas 1 (mês passado) e 2 (este mês) preservadas
		t.Fatalf("removidas = %d; want 2", removidas)
	}
	if _, err := parcs.FindByID(ctx, a.ID, parc.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("parcelamento deveria ter sido removido")
	}

	// Sessões: expirada não retorna; revogação preserva a atual.
	now := time.Now()
	for i, exp := range []time.Time{now.Add(-time.Minute), now.Add(time.Hour), now.Add(time.Hour)} {
		_ = sessions.Create(ctx, &domain.Session{TokenHash: fmt.Sprintf("%064d", now.UnixNano()+int64(i)), UserID: a.ID, CreatedAt: now, ExpiresAt: exp, LastSeenAt: now})
	}
	if _, err := sessions.FindByTokenHash(ctx, fmt.Sprintf("%064d", now.UnixNano())); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatal("sessão expirada não pode ser retornada")
	}
	keep := fmt.Sprintf("%064d", now.UnixNano()+1)
	if n, err := sessions.DeleteAllForUser(ctx, a.ID, keep); err != nil || n != 2 {
		t.Fatalf("DeleteAllForUser = %d, %v", n, err)
	}
	if _, err := sessions.FindByTokenHash(ctx, keep); err != nil {
		t.Fatal("sessão preservada deveria existir")
	}
}

// TestRLS_BloqueiaAcessoDiretoSemTenant prova que, mesmo com SQL "esquecendo"
// o WHERE user_id, o banco não devolve dados de outros usuários.
func TestRLS_BloqueiaAcessoDiretoSemTenant(t *testing.T) {
	owner, app := setupPools(t)
	ctx := context.Background()
	users := NewUserRepository(app)
	txs := NewTransacaoRepository(app)
	a, b := novoUsuario(t, users, "a"), novoUsuario(t, users, "b")
	hoje := domain.DateOnly(time.Now().UTC())
	for _, u := range []*domain.User{a, b} {
		if err := txs.Create(ctx, u.ID, &domain.Transacao{Tipo: domain.TipoEntrada, Valor: 100, Categoria: "rls", Data: hoje, Origem: domain.OrigemManual}); err != nil {
			t.Fatal(err)
		}
	}

	count := func(pool *pgxpool.Pool, tenant string) int {
		var n int
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if tenant != "" {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, tenant); err != nil {
					return err
				}
			}
			// Query propositalmente SEM filtro por usuário.
			return tx.QueryRow(ctx, `SELECT count(*) FROM transacoes WHERE user_id IN ($1, $2)`, a.ID, b.ID).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := count(app, ""); n != 0 {
		t.Fatalf("sem app.user_id o RLS deveria negar tudo; viu %d linhas", n)
	}
	if n := count(app, a.ID); n != 1 {
		t.Fatalf("tenant A deveria ver só 1 linha; viu %d", n)
	}
	if n := count(owner, ""); n != 2 {
		t.Fatalf("o dono (migrator) vê tudo: esperado 2, viu %d", n)
	}

	// Inserir linha em nome de outro usuário viola o WITH CHECK.
	err := pgx.BeginFunc(ctx, app, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, a.ID)
		_, err := tx.Exec(ctx, `INSERT INTO transacoes (user_id, tipo, valor_centavos, categoria, data_transacao, origem)
			VALUES ($1, 'entrada', 1, 'x', current_date, 'manual')`, b.ID)
		return err
	})
	if err == nil {
		t.Fatal("RLS WITH CHECK deveria impedir inserir em nome de outro usuário")
	}

	// O tenant não vaza para a próxima transação da mesma conexão.
	conn, _ := app.Acquire(ctx)
	defer conn.Release()
	_ = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT set_config('app.user_id', $1, true)`, a.ID)
		return err
	})
	var leaked *string
	_ = conn.QueryRow(ctx, `SELECT NULLIF(current_setting('app.user_id', true), '')`).Scan(&leaked)
	if leaked != nil {
		t.Fatalf("app.user_id vazou para fora da transação: %s", *leaked)
	}
}

func TestLoginAttemptStore(t *testing.T) {
	_, app := setupPools(t)
	ctx := context.Background()
	s := NewLoginAttemptStore(app)
	key := fmt.Sprintf("x%d@t.com", time.Now().UnixNano())
	now := time.Now()
	for i := 1; i <= 3; i++ {
		if n, err := s.RegisterFailure(ctx, key, now, time.Hour); err != nil || n != i {
			t.Fatalf("falha %d = %d, %v", i, n, err)
		}
	}
	_ = s.Lock(ctx, key, now.Add(time.Minute))
	if st, _ := s.Get(ctx, key); st.Failures != 3 || st.LockedUntil.IsZero() {
		t.Fatalf("estado = %+v", st)
	}
	// Fora da janela, recomeça do 1 e remove o bloqueio.
	if n, _ := s.RegisterFailure(ctx, key, now.Add(2*time.Hour), time.Hour); n != 1 {
		t.Fatalf("janela expirada deveria reiniciar: %d", n)
	}
	_ = s.Reset(ctx, key)
	if st, _ := s.Get(ctx, key); st.Failures != 0 {
		t.Fatal("reset não apagou")
	}
}

func TestEmailVerification_StoreERepositorio(t *testing.T) {
	_, app := setupPools(t)
	ctx := context.Background()
	users := NewUserRepository(app)
	store := NewEmailVerificationStore(app)
	a, b := novoUsuario(t, users, "a"), novoUsuario(t, users, "b")
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := store.Get(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sem desafio: %v", err)
	}
	hash := fmt.Sprintf("%064d", 7)
	if err := store.Save(ctx, &domain.EmailVerification{UserID: a.ID, Email: a.Email, CodeHash: hash, ExpiresAt: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.IncrementAttempts(ctx, a.ID); err != nil || n != 1 {
		t.Fatalf("attempts: %d, %v", n, err)
	}
	v, err := store.Get(ctx, a.ID)
	if err != nil || v.Email != a.Email || v.CodeHash != hash || v.Attempts != 1 {
		t.Fatalf("get: %+v, %v", v, err)
	}
	// Outro tenant não enxerga o desafio de A (RLS).
	if _, err := store.Get(ctx, b.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B viu desafio: %v", err)
	}
	// Salvar de novo zera as tentativas.
	_ = store.Save(ctx, &domain.EmailVerification{UserID: a.ID, Email: a.Email, CodeHash: hash, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if v, _ := store.Get(ctx, a.ID); v.Attempts != 0 {
		t.Fatalf("attempts após save = %d", v.Attempts)
	}
	if err := store.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}

	if u, _ := users.FindByID(ctx, a.ID); u.EmailVerificado() {
		t.Fatal("usuário novo não deveria ter e-mail verificado")
	}
	if err := users.MarkEmailVerified(ctx, a.ID, now); err != nil {
		t.Fatal(err)
	}
	u, err := users.FindByEmail(ctx, a.Email)
	if err != nil || !u.EmailVerificado() || !u.EmailVerificadoEm.Equal(now) {
		t.Fatalf("find by email após verificar: %+v, %v", u, err)
	}
}

func TestParcelamentoRepository_ListComProgresso(t *testing.T) {
	_, app := setupPools(t)
	ctx := context.Background()
	users := NewUserRepository(app)
	parcs := NewParcelamentoRepository(app)
	u := novoUsuario(t, users, "a")
	hoje := domain.DateOnly(time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	p, err := domain.NewParcelamento(domain.NewParcelamentoInput{UserID: u.ID, Tipo: domain.ParcelamentoParcelado, Descricao: "TV",
		Categoria: "casa", ValorTotal: 30000, TotalParcelas: 3, DataPrimeiraParcela: hoje.AddDate(0, -1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := parcs.CreateWithParcelas(ctx, u.ID, p, p.GerarParcelas()); err != nil {
		t.Fatal(err)
	}
	list, err := parcs.List(ctx, u.ID, hoje)
	if err != nil || len(list) != 1 || list[0].Progresso == nil {
		t.Fatalf("list: %+v, %v", list, err)
	}
	pr := list[0].Progresso
	if pr.ParcelasPagas != 2 || pr.ParcelasRestantes != 1 || pr.ValorPago != 20000 || pr.ValorRestante != 10000 ||
		pr.ProximaParcela == nil || !pr.ProximaParcela.Equal(hoje.AddDate(0, 1, 0)) {
		t.Fatalf("progresso = %+v (próxima %v)", pr, pr.ProximaParcela)
	}
}

// Parcelamento cadastrado no meio do contrato: as parcelas antigas não são
// gravadas, mas continuam contando como pagas no progresso da listagem.
func TestParcelamentoRepository_ParcelasJaPagas(t *testing.T) {
	_, app := setupPools(t)
	ctx := context.Background()
	users := NewUserRepository(app)
	parcs := NewParcelamentoRepository(app)
	u := novoUsuario(t, users, "a")
	hoje := domain.DateOnly(time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))

	p, err := domain.NewParcelamento(domain.NewParcelamentoInput{UserID: u.ID, Tipo: domain.ParcelamentoParcelado,
		Descricao: "TV", Categoria: "casa", ValorTotal: 30000, TotalParcelas: 3,
		DataPrimeiraParcela: hoje.AddDate(0, -1, 0), ParcelasJaPagas: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := parcs.CreateWithParcelas(ctx, u.ID, p, p.GerarParcelas()); err != nil {
		t.Fatal(err)
	}

	// Só a parcela futura virou lançamento.
	parcelas, err := parcs.ListParcelasDoParcelamento(ctx, u.ID, p.ID)
	if err != nil || len(parcelas) != 1 {
		t.Fatalf("parcelas gravadas = %d, %v", len(parcelas), err)
	}

	achado, err := parcs.FindByID(ctx, u.ID, p.ID)
	if err != nil || achado.ParcelasJaPagas != 2 {
		t.Fatalf("parcelas_ja_pagas = %d, %v", achado.ParcelasJaPagas, err)
	}

	list, err := parcs.List(ctx, u.ID, hoje)
	if err != nil || len(list) != 1 || list[0].Progresso == nil {
		t.Fatalf("list: %+v, %v", list, err)
	}
	pr := list[0].Progresso
	if pr.ParcelasPagas != 2 || pr.ParcelasRestantes != 1 || pr.ValorPago != 20000 || pr.ValorRestante != 10000 {
		t.Fatalf("progresso = %+v", pr)
	}
}

// Cartão de crédito: isolamento entre contas e atomicidade do pagamento.
func TestCartaoRepository_IsolamentoEPagamento(t *testing.T) {
	_, app := setupPools(t)
	ctx := context.Background()
	users := NewUserRepository(app)
	cartoes := NewCartaoRepository(app)
	txs := NewTransacaoRepository(app)
	a := novoUsuario(t, users, "a")
	b := novoUsuario(t, users, "b")
	hoje := domain.DateOnly(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))

	cartao, err := domain.NewCartao(domain.NewCartaoInput{UserID: a.ID, Nome: "Nubank",
		DiaFechamento: 20, DiaVencimento: 21, Limite: 500000})
	if err != nil {
		t.Fatal(err)
	}
	if err := cartoes.Create(ctx, a.ID, cartao); err != nil {
		t.Fatal(err)
	}

	// B não enxerga, não busca e não apaga o cartão de A.
	if lista, _ := cartoes.List(ctx, b.ID); len(lista) != 0 {
		t.Fatal("usuário B enxergou cartões de A")
	}
	if _, err := cartoes.FindByID(ctx, b.ID, cartao.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B buscou cartão de A: %v", err)
	}
	if err := cartoes.Delete(ctx, b.ID, cartao.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("B excluiu cartão de A: %v", err)
	}

	compras, err := domain.NewCompraCartao(cartao, domain.NewCompraInput{
		UserID: a.ID, CartaoID: cartao.ID, Valor: 60000, Categoria: "mercado",
		Descricao: "Compras", DataCompra: hoje, TotalParcelas: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := cartoes.CreateCompras(ctx, a.ID, compras); err != nil {
		t.Fatal(err)
	}
	venc := compras[0].FaturaVencimento

	// B não vê as compras nem o total comprometido de A.
	if lista, _ := cartoes.ComprasDaFatura(ctx, b.ID, cartao.ID, venc); len(lista) != 0 {
		t.Fatal("B enxergou compras de A")
	}
	if total, _ := cartoes.TotalNaoPago(ctx, b.ID, cartao.ID); total != 0 {
		t.Fatalf("B viu o comprometido de A: %d", total)
	}
	if total, err := cartoes.TotalNaoPago(ctx, a.ID, cartao.ID); err != nil || total != 60000 {
		t.Fatalf("comprometido de A = %d, %v; queria 60000", total, err)
	}

	// Pagamento: a saída no saldo e o registro entram juntos.
	saldoAntes, _ := txs.SaldoAte(ctx, a.ID, hoje.AddDate(0, 0, 1))
	pagamento := &domain.PagamentoFatura{UserID: a.ID, CartaoID: cartao.ID,
		Vencimento: venc, Valor: 30000, PagoEm: hoje}
	saida, err := domain.NewTransacao(domain.NewTransacaoInput{UserID: a.ID,
		Tipo: domain.TipoSaida, Valor: 30000, Categoria: "cartão de crédito",
		Descricao: "Fatura Nubank", Data: hoje, Origem: domain.OrigemManual})
	if err != nil {
		t.Fatal(err)
	}
	if err := cartoes.RegistrarPagamento(ctx, a.ID, saida, pagamento); err != nil {
		t.Fatal(err)
	}
	saldoDepois, _ := txs.SaldoAte(ctx, a.ID, hoje.AddDate(0, 0, 1))
	if saldoAntes-saldoDepois != 30000 {
		t.Fatalf("o pagamento não saiu do saldo: antes %d, depois %d", saldoAntes, saldoDepois)
	}

	// Pagar de novo a mesma fatura é bloqueado pela chave primária.
	if err := cartoes.RegistrarPagamento(ctx, a.ID, saida, pagamento); !errors.Is(err, domain.ErrFaturaJaPaga) {
		t.Fatalf("pagamento duplicado: %v", err)
	}
	// B não desfaz o pagamento de A.
	if err := cartoes.RemoverPagamento(ctx, b.ID, cartao.ID, venc); !errors.Is(err, domain.ErrFaturaNaoPaga) {
		t.Fatalf("B removeu pagamento de A: %v", err)
	}

	// Desfazer devolve o saldo: a transação some junto.
	if err := cartoes.RemoverPagamento(ctx, a.ID, cartao.ID, venc); err != nil {
		t.Fatal(err)
	}
	if saldoFinal, _ := txs.SaldoAte(ctx, a.ID, hoje.AddDate(0, 0, 1)); saldoFinal != saldoAntes {
		t.Fatalf("saldo após desfazer = %d; queria %d", saldoFinal, saldoAntes)
	}
	if _, err := cartoes.PagamentoDaFatura(ctx, a.ID, cartao.ID, venc); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pagamento sobreviveu: %v", err)
	}

	// B não apaga as compras de A.
	if n, _ := cartoes.DeleteCompraGrupo(ctx, b.ID, compras[0].GrupoID); n != 0 {
		t.Fatalf("B apagou %d compras de A", n)
	}
	if n, err := cartoes.DeleteCompraGrupo(ctx, a.ID, compras[0].GrupoID); err != nil || n != 2 {
		t.Fatalf("apagar a compra inteira removeu %d parcelas, %v", n, err)
	}
}
