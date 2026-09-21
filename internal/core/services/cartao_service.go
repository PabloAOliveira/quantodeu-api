package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// CartaoService implementa ports.CartaoUseCase.
//
// A regra que manda em tudo aqui: compra no cartão NÃO mexe no saldo. Ela é
// dívida com o banco. O dinheiro só se move quando a fatura é paga — e aí sim
// nasce uma transação de saída, na data do pagamento. É por isso que a fatura
// do mês passado aparece nas despesas do mês em que você pagou.
type CartaoService struct {
	cartoes    ports.CartaoRepository
	transacoes ports.TransacaoRepository
	clock      ports.Clock
	loc        *time.Location
	log        *slog.Logger
}

var _ ports.CartaoUseCase = (*CartaoService)(nil)

// NewCartaoService cria o serviço.
func NewCartaoService(cartoes ports.CartaoRepository, transacoes ports.TransacaoRepository,
	clock ports.Clock, loc *time.Location, log *slog.Logger) *CartaoService {
	if loc == nil {
		loc = time.UTC
	}
	return &CartaoService{cartoes: cartoes, transacoes: transacoes, clock: clock, loc: loc, log: log}
}

func (s *CartaoService) hoje() time.Time { return domain.DateOnly(s.clock.Now().In(s.loc)) }

// naData converte uma data opcional para o fuso da aplicação (nil = hoje).
func (s *CartaoService) naData(d *time.Time) time.Time {
	if d == nil {
		return s.hoje()
	}
	y, m, dd := d.Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, s.loc)
}

// ---------------------------------------------------------------------------
// Cartões
// ---------------------------------------------------------------------------

// Create cadastra um cartão.
func (s *CartaoService) Create(ctx context.Context, userID string, in ports.CreateCartaoInput) (*domain.Cartao, error) {
	c, err := domain.NewCartao(domain.NewCartaoInput{
		UserID: userID, Nome: in.Nome, Banco: in.Banco,
		DiaFechamento: in.DiaFechamento, DiaVencimento: in.DiaVencimento, Limite: in.Limite,
	})
	if err != nil {
		return nil, err
	}
	if err := s.cartoes.Create(ctx, userID, c); err != nil {
		return nil, fmt.Errorf("criar cartão: %w", err)
	}
	return c, nil
}

// Update altera os dados do cartão (inclusive arquivar, com Ativo=false).
func (s *CartaoService) Update(ctx context.Context, userID, id string, in ports.CreateCartaoInput) (*domain.Cartao, error) {
	atual, err := s.cartoes.FindByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	novo, err := domain.NewCartao(domain.NewCartaoInput{
		UserID: userID, Nome: in.Nome, Banco: in.Banco,
		DiaFechamento: in.DiaFechamento, DiaVencimento: in.DiaVencimento, Limite: in.Limite,
	})
	if err != nil {
		return nil, err
	}
	novo.ID, novo.CreatedAt = atual.ID, atual.CreatedAt
	novo.Ativo = atual.Ativo
	if in.Ativo != nil {
		novo.Ativo = *in.Ativo
	}
	if err := s.cartoes.Update(ctx, userID, novo); err != nil {
		return nil, fmt.Errorf("atualizar cartão: %w", err)
	}
	return novo, nil
}

// Delete só remove cartão sem histórico: com compras, o certo é arquivar, para
// não apagar o passado financeiro junto.
func (s *CartaoService) Delete(ctx context.Context, userID, id string) error {
	if _, err := s.cartoes.FindByID(ctx, userID, id); err != nil {
		return err
	}
	faturas, err := s.cartoes.ResumoDasFaturas(ctx, userID, id)
	if err != nil {
		return fmt.Errorf("cartão: consultar histórico: %w", err)
	}
	if len(faturas) > 0 {
		return domain.ErrCartaoComHistorico
	}
	return s.cartoes.Delete(ctx, userID, id)
}

// List devolve os cartões com as duas faturas que interessam e o limite livre.
func (s *CartaoService) List(ctx context.Context, userID string) ([]*domain.ResumoCartao, error) {
	cartoes, err := s.cartoes.List(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("listar cartões: %w", err)
	}
	out := make([]*domain.ResumoCartao, 0, len(cartoes))
	for _, c := range cartoes {
		resumo, err := s.resumo(ctx, userID, c)
		if err != nil {
			return nil, err
		}
		out = append(out, resumo)
	}
	return out, nil
}

// Get devolve um cartão com o mesmo resumo da lista.
func (s *CartaoService) Get(ctx context.Context, userID, id string) (*domain.ResumoCartao, error) {
	c, err := s.cartoes.FindByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return s.resumo(ctx, userID, c)
}

// resumo monta as duas faturas que convivem em qualquer mês: a que está
// acumulando e a fechada mais antiga ainda não paga.
func (s *CartaoService) resumo(ctx context.Context, userID string, c *domain.Cartao) (*domain.ResumoCartao, error) {
	hoje := s.hoje()
	r := &domain.ResumoCartao{Cartao: c, MelhorDiaCompra: c.MelhorDiaCompra(hoje)}

	comprometido, err := s.cartoes.TotalNaoPago(ctx, userID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("cartão: total não pago: %w", err)
	}
	r.LimiteDisponivel = domain.CalcularLimiteDisponivel(c.Limite, comprometido)

	faturas, err := s.cartoes.ResumoDasFaturas(ctx, userID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("cartão: resumo das faturas: %w", err)
	}

	// A "a pagar" é a fatura não paga mais antiga que tem compras: é a próxima
	// conta que vai chegar. O campo `status` dela diz se já fechou ou se ainda
	// está acumulando.
	for i := len(faturas) - 1; i >= 0; i-- { // do mais antigo para o mais novo
		if faturas[i].Pagamento == nil && faturas[i].Total > 0 {
			r.APagar = domain.MontarDoResumo(c, faturas[i], hoje)
			break
		}
	}

	// A "em aberto" é o ciclo que contém hoje — onde cairia uma compra feita
	// agora. Antes do fechamento ela É a mesma que você vai pagar, e repetir o
	// mesmo número em dois lugares só confunde: nesse caso, some.
	vencAberta := c.FaturaDaCompra(hoje)
	if r.APagar == nil || !domain.MesmaData(r.APagar.Vencimento, vencAberta) {
		aberta := domain.FaturaResumo{Vencimento: vencAberta}
		for _, f := range faturas {
			if domain.MesmaData(f.Vencimento, vencAberta) {
				aberta = f
				break
			}
		}
		r.EmAberto = domain.MontarDoResumo(c, aberta, hoje)
	}
	return r, nil
}

// montar junta compras + pagamento numa fatura.
func (s *CartaoService) montar(ctx context.Context, userID string, c *domain.Cartao, vencimento, hoje time.Time) (*domain.Fatura, error) {
	compras, err := s.cartoes.ComprasDaFatura(ctx, userID, c.ID, vencimento)
	if err != nil {
		return nil, fmt.Errorf("cartão: compras da fatura: %w", err)
	}
	pag, err := s.cartoes.PagamentoDaFatura(ctx, userID, c.ID, vencimento)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("cartão: pagamento da fatura: %w", err)
	}
	return domain.MontarFatura(c, vencimento, compras, pag, hoje), nil
}

// ---------------------------------------------------------------------------
// Faturas
// ---------------------------------------------------------------------------

// Faturas lista o histórico, da mais recente para a mais antiga.
func (s *CartaoService) Faturas(ctx context.Context, userID, cartaoID string) ([]*domain.Fatura, error) {
	c, err := s.cartoes.FindByID(ctx, userID, cartaoID)
	if err != nil {
		return nil, err
	}
	faturas, err := s.cartoes.ResumoDasFaturas(ctx, userID, cartaoID)
	if err != nil {
		return nil, fmt.Errorf("cartão: resumo das faturas: %w", err)
	}
	hoje := s.hoje()
	out := make([]*domain.Fatura, 0, len(faturas))
	for _, f := range faturas {
		// Sem as compras: a lista mostra totais, e o detalhe é quem as carrega.
		out = append(out, domain.MontarDoResumo(c, f, hoje))
	}
	return out, nil
}

// Fatura devolve uma competência com as compras dela.
func (s *CartaoService) Fatura(ctx context.Context, userID, cartaoID, competencia string) (*domain.Fatura, error) {
	c, err := s.cartoes.FindByID(ctx, userID, cartaoID)
	if err != nil {
		return nil, err
	}
	vencimento, err := s.vencimentoDe(c, competencia)
	if err != nil {
		return nil, err
	}
	return s.montar(ctx, userID, c, vencimento, s.hoje())
}

// vencimentoDe traduz "2026-10" na data exata de vencimento daquele mês.
func (s *CartaoService) vencimentoDe(c *domain.Cartao, competencia string) (time.Time, error) {
	mes, err := domain.ParseCompetencia(competencia)
	if err != nil {
		return time.Time{}, err
	}
	mes = time.Date(mes.Year(), mes.Month(), 1, 0, 0, 0, 0, s.loc)
	primeiro := mes.AddDate(0, 1, -1).Day()
	dia := c.DiaVencimento
	if dia > primeiro {
		dia = primeiro
	}
	return time.Date(mes.Year(), mes.Month(), dia, 0, 0, 0, 0, s.loc), nil
}

// PagarFatura é o momento em que o dinheiro sai da conta.
//
// Cria UMA transação de saída na data do pagamento — não no vencimento, não no
// mês das compras. É por isso que a fatura de setembro paga em outubro aparece
// nas despesas de outubro.
func (s *CartaoService) PagarFatura(ctx context.Context, userID, cartaoID, competencia string, in ports.PagarFaturaInput) (*domain.Fatura, error) {
	c, err := s.cartoes.FindByID(ctx, userID, cartaoID)
	if err != nil {
		return nil, err
	}
	vencimento, err := s.vencimentoDe(c, competencia)
	if err != nil {
		return nil, err
	}
	hoje := s.hoje()
	fatura, err := s.montar(ctx, userID, c, vencimento, hoje)
	if err != nil {
		return nil, err
	}
	if fatura.Status == domain.FaturaPaga {
		return nil, domain.ErrFaturaJaPaga
	}
	if fatura.Total <= 0 {
		return nil, domain.ErrFaturaVazia
	}

	valor := in.Valor
	if valor <= 0 {
		valor = fatura.Total
	}
	pagoEm := s.naData(in.Data)

	t, err := domain.NewTransacao(domain.NewTransacaoInput{
		UserID: userID, Tipo: domain.TipoSaida, Valor: valor,
		Categoria: "cartão de crédito",
		Descricao: "Fatura " + c.Nome + " · " + fatura.Competencia,
		Data:      pagoEm, Origem: domain.OrigemManual,
	})
	if err != nil {
		return nil, err
	}
	pag := &domain.PagamentoFatura{
		UserID: userID, CartaoID: c.ID, Vencimento: vencimento,
		Valor: valor, PagoEm: pagoEm,
	}
	if err := s.cartoes.RegistrarPagamento(ctx, userID, t, pag); err != nil {
		return nil, fmt.Errorf("pagar fatura: %w", err)
	}
	pag.TransacaoID = t.ID
	fatura.Pagamento = pag
	fatura.Status = domain.FaturaPaga
	s.log.InfoContext(ctx, "fatura paga",
		slog.String("user_id", userID), slog.String("cartao_id", c.ID),
		slog.String("competencia", fatura.Competencia))
	return fatura, nil
}

// DesfazerPagamento remove a saída do saldo e devolve a fatura para fechada.
func (s *CartaoService) DesfazerPagamento(ctx context.Context, userID, cartaoID, competencia string) error {
	c, err := s.cartoes.FindByID(ctx, userID, cartaoID)
	if err != nil {
		return err
	}
	vencimento, err := s.vencimentoDe(c, competencia)
	if err != nil {
		return err
	}
	if _, err := s.cartoes.PagamentoDaFatura(ctx, userID, cartaoID, vencimento); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrFaturaNaoPaga
		}
		return err
	}
	return s.cartoes.RemoverPagamento(ctx, userID, cartaoID, vencimento)
}

// ---------------------------------------------------------------------------
// Compras
// ---------------------------------------------------------------------------

// RegistrarCompra grava a compra. Parcelada, cai em faturas consecutivas.
func (s *CartaoService) RegistrarCompra(ctx context.Context, userID, cartaoID string, in ports.CreateCompraInput) ([]*domain.CompraCartao, error) {
	c, err := s.cartoes.FindByID(ctx, userID, cartaoID)
	if err != nil {
		return nil, err
	}
	compras, err := domain.NewCompraCartao(c, domain.NewCompraInput{
		UserID: userID, CartaoID: cartaoID, Valor: in.Valor,
		Categoria: in.Categoria, Descricao: in.Descricao,
		DataCompra: s.naData(in.Data), TotalParcelas: in.TotalParcelas,
	})
	if err != nil {
		return nil, err
	}
	// Lançar numa fatura já paga mudaria um total que já virou dinheiro na
	// conta: a fatura continuaria "paga", mas por um valor diferente do que
	// saiu. Melhor barrar do que guardar uma incoerência silenciosa.
	if _, err := s.cartoes.PagamentoDaFatura(ctx, userID, cartaoID, compras[0].FaturaVencimento); err == nil {
		return nil, domain.ErrFaturaFechadaParaCompra
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("registrar compra: %w", err)
	}
	if err := s.cartoes.CreateCompras(ctx, userID, compras); err != nil {
		return nil, fmt.Errorf("registrar compra: %w", err)
	}
	return compras, nil
}

// ExcluirCompra apaga a compra inteira — todas as parcelas, inclusive as que
// já caíram em faturas futuras.
func (s *CartaoService) ExcluirCompra(ctx context.Context, userID, grupoID string) (int64, error) {
	// Mesma razão do lançamento: apagar uma parcela que já foi paga deixaria o
	// pagamento sem lastro.
	pago, err := s.cartoes.GrupoTemFaturaPaga(ctx, userID, grupoID)
	if err != nil {
		return 0, err
	}
	if pago {
		return 0, domain.ErrFaturaFechadaParaCompra
	}
	n, err := s.cartoes.DeleteCompraGrupo(ctx, userID, grupoID)
	if err != nil {
		return 0, fmt.Errorf("excluir compra: %w", err)
	}
	if n == 0 {
		return 0, domain.ErrNotFound
	}
	return n, nil
}
