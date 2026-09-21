package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// fakeCartoesUseCase entrega uma lista pronta de cartões; só List é usado pela
// agenda, o resto existe para satisfazer a porta.
type fakeCartoesUseCase struct {
	lista []*domain.ResumoCartao
	err   error
}

func (f *fakeCartoesUseCase) List(context.Context, string) ([]*domain.ResumoCartao, error) {
	return f.lista, f.err
}
func (f *fakeCartoesUseCase) Create(context.Context, string, ports.CreateCartaoInput) (*domain.Cartao, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCartoesUseCase) Get(context.Context, string, string) (*domain.ResumoCartao, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCartoesUseCase) Update(context.Context, string, string, ports.CreateCartaoInput) (*domain.Cartao, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCartoesUseCase) Delete(context.Context, string, string) error { return domain.ErrNotFound }
func (f *fakeCartoesUseCase) Faturas(context.Context, string, string) ([]*domain.Fatura, error) {
	return nil, nil
}
func (f *fakeCartoesUseCase) Fatura(context.Context, string, string, string) (*domain.Fatura, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCartoesUseCase) PagarFatura(context.Context, string, string, string, ports.PagarFaturaInput) (*domain.Fatura, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCartoesUseCase) DesfazerPagamento(context.Context, string, string, string) error {
	return domain.ErrNotFound
}
func (f *fakeCartoesUseCase) RegistrarCompra(context.Context, string, string, ports.CreateCompraInput) ([]*domain.CompraCartao, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCartoesUseCase) ExcluirCompra(context.Context, string, string) (int64, error) {
	return 0, domain.ErrNotFound
}

var _ ports.CartaoUseCase = (*fakeCartoesUseCase)(nil)

func dataEm(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// parcelaEm cria um lançamento de parcela já com dono e parcelamento.
func parcelaEm(userID, descricao, data string, valor domain.Money, numero int) *domain.Transacao {
	pid := "parc-1"
	return &domain.Transacao{
		UserID: userID, Tipo: domain.TipoSaida, Valor: valor, Categoria: "saude",
		Descricao: descricao, Data: dataEm(data), Origem: domain.OrigemParcelamento,
		ParcelamentoID: &pid, NumeroParcela: &numero,
	}
}

func agendaService(t *testing.T, hoje string, txs *fakeTransacoes, cartoes ports.CartaoUseCase) *ResumoService {
	t.Helper()
	clk := &fakeClock{now: dataEm(hoje).Add(12 * time.Hour)}
	fin := NewFinancasServices(newFakeUsers(), txs, nil, clk, nil, time.UTC, discardLogger())
	if cartoes != nil {
		fin.Resumo.ComCartoes(cartoes)
	}
	return fin.Resumo
}

func TestAgenda_ParcelasEFaturasOrdenadas(t *testing.T) {
	ctx := context.Background()
	txs := &fakeTransacoes{}
	txs.items = []*domain.Transacao{
		parcelaEm("u1", "Academia", "2026-10-09", 9990, 2),
		parcelaEm("u1", "Academia", "2026-11-09", 9990, 3),
		// Fora do horizonte de 6 meses.
		parcelaEm("u1", "Academia", "2027-08-09", 9990, 12),
		// Já passou: a agenda é do que ainda vai vencer.
		parcelaEm("u1", "Academia", "2026-08-09", 9990, 1),
		// De outro usuário.
		parcelaEm("u2", "Faculdade", "2026-10-10", 50000, 1),
	}
	cartao := &domain.Cartao{ID: "c1", Nome: "Nubank", Ativo: true, DiaFechamento: 20, DiaVencimento: 27}
	cartoes := &fakeCartoesUseCase{lista: []*domain.ResumoCartao{{
		Cartao: cartao,
		APagar: &domain.Fatura{Competencia: "2026-09", Vencimento: dataEm("2026-09-27"), Total: 50000, Pago: 30000},
		EmAberto: &domain.Fatura{
			Competencia: "2026-10", Vencimento: dataEm("2026-10-27"), Total: 10334,
		},
	}}}

	agenda, err := agendaService(t, "2026-09-21", txs, cartoes).GetAgenda(ctx, "u1", 0)
	if err != nil {
		t.Fatal(err)
	}
	quer := []struct {
		data  string
		tipo  domain.TipoCompromisso
		valor domain.Money
	}{
		{"2026-09-27", domain.CompromissoFatura, 20000}, // só o que falta da parcial
		{"2026-10-09", domain.CompromissoParcela, 9990},
		{"2026-10-27", domain.CompromissoFatura, 10334},
		{"2026-11-09", domain.CompromissoParcela, 9990},
	}
	if len(agenda.Itens) != len(quer) {
		t.Fatalf("itens = %d; queria %d (%+v)", len(agenda.Itens), len(quer), agenda.Itens)
	}
	for i, q := range quer {
		got := agenda.Itens[i]
		if !got.Data.Equal(dataEm(q.data)) || got.Tipo != q.tipo || got.Valor != q.valor {
			t.Errorf("item %d = %s %s %d; queria %s %s %d",
				i, got.Data.Format("2006-01-02"), got.Tipo, got.Valor, q.data, q.tipo, q.valor)
		}
	}
}

func TestAgenda_IgnoraFaturaQuitadaECartaoArquivado(t *testing.T) {
	ctx := context.Background()
	quitada := &domain.ResumoCartao{
		Cartao: &domain.Cartao{ID: "c1", Nome: "Quitado", Ativo: true},
		APagar: &domain.Fatura{Competencia: "2026-10", Vencimento: dataEm("2026-10-27"), Total: 10000, Pago: 10000},
	}
	arquivado := &domain.ResumoCartao{
		Cartao: &domain.Cartao{ID: "c2", Nome: "Arquivado", Ativo: false},
		APagar: &domain.Fatura{Competencia: "2026-10", Vencimento: dataEm("2026-10-27"), Total: 10000},
	}
	cartoes := &fakeCartoesUseCase{lista: []*domain.ResumoCartao{quitada, arquivado}}

	agenda, err := agendaService(t, "2026-09-21", &fakeTransacoes{}, cartoes).GetAgenda(ctx, "u1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(agenda.Itens) != 0 {
		t.Fatalf("agenda = %+v; queria vazia", agenda.Itens)
	}
}

// O bloco de cartões é um extra: falhar nele não pode apagar as parcelas.
func TestAgenda_FalhaNosCartoesNaoDerrubaParcelas(t *testing.T) {
	ctx := context.Background()
	txs := &fakeTransacoes{items: []*domain.Transacao{
		parcelaEm("u1", "Academia", "2026-10-09", 9990, 2),
	}}
	cartoes := &fakeCartoesUseCase{err: errors.New("banco fora do ar")}

	agenda, err := agendaService(t, "2026-09-21", txs, cartoes).GetAgenda(ctx, "u1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(agenda.Itens) != 1 || agenda.Itens[0].Tipo != domain.CompromissoParcela {
		t.Fatalf("agenda = %+v; queria só a parcela", agenda.Itens)
	}
}

func TestAgenda_HorizonteLimitado(t *testing.T) {
	ctx := context.Background()
	svc := agendaService(t, "2026-09-21", &fakeTransacoes{}, nil)

	// Sem valor: o padrão. Acima do teto: cai no teto — agendar dois anos de
	// um financiamento de 360 parcelas estouraria os alarmes do aparelho.
	casos := map[int]string{
		0:   "2027-03-21",
		3:   "2026-12-21",
		999: "2027-09-21",
	}
	for meses, fim := range casos {
		a, err := svc.GetAgenda(ctx, "u1", meses)
		if err != nil {
			t.Fatal(err)
		}
		if !a.Fim.Equal(dataEm(fim)) {
			t.Errorf("meses=%d -> fim %s; queria %s", meses, a.Fim.Format("2006-01-02"), fim)
		}
		if !a.Inicio.Equal(dataEm("2026-09-21")) {
			t.Errorf("início = %s; queria hoje", a.Inicio.Format("2006-01-02"))
		}
	}
}
