package domain

import (
	"testing"
	"time"
)

func TestCalcularProgresso(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	parcelas := []*Transacao{
		{Valor: 100, Data: d("2026-08-10")},
		{Valor: 100, Data: d("2026-09-15")}, // vence hoje: conta como paga
		{Valor: 150, Data: d("2026-10-10")}, // ajustada manualmente
		{Valor: 100, Data: d("2026-11-10")},
	}
	pr := CalcularProgresso(parcelas, d("2026-09-15"))
	if pr.ParcelasPagas != 2 || pr.ParcelasRestantes != 2 || pr.ValorPago != 200 || pr.ValorRestante != 250 {
		t.Fatalf("progresso = %+v", pr)
	}
	if pr.ProximaParcela == nil || !pr.ProximaParcela.Equal(d("2026-10-10")) {
		t.Fatalf("próxima = %v", pr.ProximaParcela)
	}
	if q := CalcularProgresso(parcelas, d("2027-01-01")); q.ParcelasRestantes != 0 || q.ProximaParcela != nil {
		t.Fatalf("quitado = %+v", q)
	}
}
