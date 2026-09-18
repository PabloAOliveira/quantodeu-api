package domain

import (
	"testing"
	"time"
)

// Cadastro de um financiamento que começou meses atrás: as parcelas antigas já
// saíram da conta fora do app, então elas não podem virar lançamento — só
// contar como pagas no progresso.
func TestParcelasJaPagasNaoViramLancamento(t *testing.T) {
	inicio := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	p, err := NewParcelamento(NewParcelamentoInput{
		UserID: "u1", Tipo: ParcelamentoParcelado, Descricao: "Apartamento", Categoria: "Moradia",
		TotalParcelas: 360, DataPrimeiraParcela: inicio, ParcelasJaPagas: 5,
		Financiamento: &Financiamento{Sistema: SistemaSAC, ValorFinanciado: 20_000_000,
			TaxaAnual: 8.66, TipoTaxa: TaxaEfetiva},
	})
	if err != nil {
		t.Fatal(err)
	}

	parcelas := p.GerarParcelas()
	if len(parcelas) != 355 {
		t.Fatalf("parcelas geradas = %d; want 355", len(parcelas))
	}
	// A numeração é a do contrato: a primeira lançada é a 6ª.
	if n := derefNumero(parcelas[0].NumeroParcela); n != 6 {
		t.Errorf("1ª parcela lançada = nº %d; want 6", n)
	}
	if parcelas[0].Descricao != "Apartamento (6/360)" {
		t.Errorf("descrição = %q", parcelas[0].Descricao)
	}
	if d := parcelas[0].Data; d.Year() != 2026 || d.Month() != time.September {
		t.Errorf("1ª parcela lançada em %s; want set/2026", d.Format("2006-01-02"))
	}

	// O contrato continua inteiro: total e prazo não encolhem.
	if p.TotalParcelas != 360 {
		t.Errorf("total_parcelas = %d; want 360", p.TotalParcelas)
	}
	var lancado Money
	for _, x := range parcelas {
		lancado += x.Valor
	}
	if lancado+p.ValorJaPago() != p.ValorTotal {
		t.Errorf("lançado (%d) + já pago (%d) != total (%d)", lancado, p.ValorJaPago(), p.ValorTotal)
	}

	// Progresso em out/2026: 5 pagas antes + a de setembro e a de outubro.
	pr := p.ProgressoEm(parcelas, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if pr.ParcelasPagas != 7 {
		t.Errorf("parcelas pagas = %d; want 7", pr.ParcelasPagas)
	}
	if pr.ParcelasRestantes != 353 {
		t.Errorf("parcelas restantes = %d; want 353", pr.ParcelasRestantes)
	}
	if pr.ValorPago <= p.ValorJaPago() {
		t.Errorf("valor pago (%d) deveria incluir as parcelas anteriores (%d)", pr.ValorPago, p.ValorJaPago())
	}
	if pr.ValorPago+pr.ValorRestante != p.ValorTotal {
		t.Errorf("pago (%d) + restante (%d) != total (%d)", pr.ValorPago, pr.ValorRestante, p.ValorTotal)
	}
}

// No parcelado comum o resíduo de centavos continua na 1ª parcela do contrato,
// mesmo quando ela não é lançada.
func TestParcelasJaPagasNoParceladoComum(t *testing.T) {
	inicio := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	p, err := NewParcelamento(NewParcelamentoInput{
		UserID: "u1", Tipo: ParcelamentoParcelado, Descricao: "Notebook",
		ValorTotal: 100000, TotalParcelas: 3, DataPrimeiraParcela: inicio, ParcelasJaPagas: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	parcelas := p.GerarParcelas()
	if len(parcelas) != 2 {
		t.Fatalf("parcelas = %d; want 2", len(parcelas))
	}
	if p.ValorJaPago() != 33334 {
		t.Errorf("valor já pago = %d; want 33334 (a 1ª absorve o resíduo)", p.ValorJaPago())
	}
	if parcelas[0].Valor != 33333 || parcelas[1].Valor != 33333 {
		t.Errorf("valores lançados = %d, %d", parcelas[0].Valor, parcelas[1].Valor)
	}
}

func TestParcelasJaPagasValidacao(t *testing.T) {
	inicio := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	base := func(ja int) NewParcelamentoInput {
		return NewParcelamentoInput{
			UserID: "u1", Tipo: ParcelamentoParcelado, Descricao: "Notebook",
			ValorTotal: 100000, TotalParcelas: 3, DataPrimeiraParcela: inicio, ParcelasJaPagas: ja,
		}
	}
	if _, err := NewParcelamento(base(-1)); err == nil {
		t.Error("negativo deveria falhar")
	}
	// Quitar tudo não faz sentido: sobraria um parcelamento sem parcela alguma.
	if _, err := NewParcelamento(base(3)); err == nil {
		t.Error("já pagas == total deveria falhar")
	}
	if _, err := NewParcelamento(base(2)); err != nil {
		t.Errorf("já pagas < total deveria passar: %v", err)
	}
}

func derefNumero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
