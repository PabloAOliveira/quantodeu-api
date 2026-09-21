package domain

import (
	"testing"
	"time"
)

func TestCompraParcelada_CaiEmFaturasConsecutivas(t *testing.T) {
	c := cartao(t, 20, 21)
	c.ID = "cartao-1"

	parcelas, err := NewCompraCartao(c, NewCompraInput{
		UserID: "u1", CartaoID: c.ID, Valor: 100000, // R$ 1.000,00
		Categoria: "Eletrônicos", Descricao: "Notebook",
		DataCompra: dia("2026-09-15"), TotalParcelas: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parcelas) != 3 {
		t.Fatalf("parcelas = %d", len(parcelas))
	}

	// Resíduo na 1ª: 100000/3 = 33333, sobra 1 centavo.
	if parcelas[0].Valor != 33334 || parcelas[1].Valor != 33333 || parcelas[2].Valor != 33333 {
		t.Errorf("valores = %d, %d, %d", parcelas[0].Valor, parcelas[1].Valor, parcelas[2].Valor)
	}
	var soma Money
	for _, p := range parcelas {
		soma += p.Valor
	}
	if soma != 100000 {
		t.Errorf("soma = %d; queria 100000", soma)
	}

	// Compra em 15/09 (antes do fechamento dia 20) -> 1ª fatura vence 21/09.
	quer := []string{"2026-09-21", "2026-10-21", "2026-11-21"}
	for i, p := range parcelas {
		if !p.FaturaVencimento.Equal(dia(quer[i])) {
			t.Errorf("parcela %d na fatura %s; queria %s",
				i+1, p.FaturaVencimento.Format("2006-01-02"), quer[i])
		}
		if p.DataCompra != dia("2026-09-15") {
			t.Errorf("parcela %d perdeu a data da compra: %s", i+1, p.DataCompra)
		}
	}
	if parcelas[0].Descricao != "Notebook (1/3)" {
		t.Errorf("descrição = %q", parcelas[0].Descricao)
	}
	if parcelas[0].Categoria != "eletrônicos" {
		t.Errorf("categoria = %q (deveria normalizar)", parcelas[0].Categoria)
	}
}

func TestCompraAVista_NaoNumeraDescricao(t *testing.T) {
	c := cartao(t, 20, 21)
	parcelas, err := NewCompraCartao(c, NewCompraInput{
		UserID: "u1", CartaoID: c.ID, Valor: 5000, Categoria: "mercado",
		Descricao: "Pão", DataCompra: dia("2026-09-15"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parcelas) != 1 || parcelas[0].Descricao != "Pão" {
		t.Errorf("à vista = %d parcela(s), descrição %q", len(parcelas), parcelas[0].Descricao)
	}
}

func TestCompra_Validacao(t *testing.T) {
	c := cartao(t, 20, 21)
	base := NewCompraInput{UserID: "u1", CartaoID: c.ID, Valor: 5000,
		Categoria: "mercado", DataCompra: dia("2026-09-15")}

	if _, err := NewCompraCartao(nil, base); err == nil {
		t.Error("sem cartão deveria falhar")
	}
	semValor := base
	semValor.Valor = 0
	if _, err := NewCompraCartao(c, semValor); err == nil {
		t.Error("valor zero deveria falhar")
	}
	muitas := base
	muitas.TotalParcelas = 37
	if _, err := NewCompraCartao(c, muitas); err == nil {
		t.Error("37 parcelas deveria falhar")
	}
	centavos := base
	centavos.Valor, centavos.TotalParcelas = 2, 3 // 2 centavos em 3x
	if _, err := NewCompraCartao(c, centavos); err == nil {
		t.Error("parcela abaixo de R$ 0,01 deveria falhar")
	}
}

func TestMontarFatura_Status(t *testing.T) {
	c := cartao(t, 20, 21)
	c.ID = "cartao-1"
	venc := dia("2026-09-21")
	compras := []*CompraCartao{{Valor: 40000}, {Valor: 20000}}

	// Antes do fechamento (dia 20): ainda acumulando.
	f := MontarFatura(c, venc, compras, nil, dia("2026-09-15"))
	if f.Status != FaturaAberta {
		t.Errorf("status em 15/09 = %s; queria aberta", f.Status)
	}
	if f.Total != 60000 {
		t.Errorf("total = %d; queria 60000", f.Total)
	}
	if f.Competencia != "2026-09" {
		t.Errorf("competência = %s", f.Competencia)
	}

	// No dia do fechamento ainda aceita compra.
	if f := MontarFatura(c, venc, compras, nil, dia("2026-09-20")); f.Status != FaturaAberta {
		t.Errorf("status em 20/09 = %s; queria aberta", f.Status)
	}
	// Passou do fechamento: fechada, esperando pagamento.
	if f := MontarFatura(c, venc, compras, nil, dia("2026-09-21")); f.Status != FaturaFechada {
		t.Errorf("status em 21/09 = %s; queria fechada", f.Status)
	}
	// Com pagamento registrado: paga, não importa a data.
	pago := &PagamentoFatura{Valor: 60000, PagoEm: dia("2026-09-20")}
	if f := MontarFatura(c, venc, compras, pago, dia("2026-09-21")); f.Status != FaturaPaga {
		t.Errorf("status com pagamento = %s; queria paga", f.Status)
	}
}

func TestLimiteDisponivel(t *testing.T) {
	casos := []struct{ limite, comprometido, quer Money }{
		{500000, 120000, 380000},
		{500000, 500000, 0},
		{500000, 600000, 0}, // estourou: mostra zero, não negativo
		{0, 120000, 0},      // sem limite informado
	}
	for _, caso := range casos {
		if got := CalcularLimiteDisponivel(caso.limite, caso.comprometido); got != caso.quer {
			t.Errorf("limite %d, comprometido %d -> %d; queria %d",
				caso.limite, caso.comprometido, got, caso.quer)
		}
	}
}

func TestCompetencia(t *testing.T) {
	if got := CompetenciaDe(dia("2026-10-21")); got != "2026-10" {
		t.Errorf("competência = %q", got)
	}
	if _, err := ParseCompetencia("2026-13"); err == nil {
		t.Error("mês 13 deveria falhar")
	}
	got, err := ParseCompetencia("2026-10")
	if err != nil || got.Year() != 2026 || got.Month() != time.October {
		t.Errorf("parse = %v, %v", got, err)
	}
}

// Compra parcelada SEM descrição não pode virar um título que é só "(1/3)":
// o número da parcela já vai em campo próprio.
func TestCompraParceladaSemDescricao(t *testing.T) {
	c := cartao(t, 20, 21)
	parcelas, err := NewCompraCartao(c, NewCompraInput{
		UserID: "u1", CartaoID: c.ID, Valor: 30000, Categoria: "mercado",
		DataCompra: dia("2026-09-15"), TotalParcelas: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range parcelas {
		if p.Descricao != "" {
			t.Errorf("parcela %d com descrição %q; queria vazia", i+1, p.Descricao)
		}
		if p.NumeroParcela != i+1 || p.TotalParcelas != 3 {
			t.Errorf("parcela %d perdeu a numeração: %d/%d", i+1, p.NumeroParcela, p.TotalParcelas)
		}
	}

	// Com descrição, o sufixo continua.
	comDesc, _ := NewCompraCartao(c, NewCompraInput{
		UserID: "u1", CartaoID: c.ID, Valor: 30000, Categoria: "mercado",
		Descricao: "Notebook", DataCompra: dia("2026-09-15"), TotalParcelas: 3,
	})
	if comDesc[0].Descricao != "Notebook (1/3)" {
		t.Errorf("descrição = %q", comDesc[0].Descricao)
	}
}
