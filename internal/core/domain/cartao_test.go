package domain

import (
	"testing"
	"time"
)

func dia(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }

func cartao(t *testing.T, fechamento, vencimento int) *Cartao {
	t.Helper()
	c, err := NewCartao(NewCartaoInput{UserID: "u1", Nome: "Nubank",
		DiaFechamento: fechamento, DiaVencimento: vencimento, Limite: 500000})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A regra que mais confunde na vida real: comprou depois do fechamento, só
// paga na fatura seguinte.
func TestCartao_CompraDepoisDoFechamentoVaiParaAProximaFatura(t *testing.T) {
	c := cartao(t, 20, 21) // fecha 20, vence 21 — o caso do Pablo

	casos := []struct{ compra, vencimento string }{
		{"2026-09-19", "2026-09-21"}, // antes do fechamento: fatura deste mês
		{"2026-09-20", "2026-09-21"}, // NO fechamento: ainda entra
		{"2026-09-21", "2026-10-21"}, // depois: só no mês que vem
		{"2026-09-30", "2026-10-21"},
		{"2026-10-01", "2026-10-21"},
	}
	for _, caso := range casos {
		if got := c.FaturaDaCompra(dia(caso.compra)); !got.Equal(dia(caso.vencimento)) {
			t.Errorf("compra %s -> fatura %s; queria %s",
				caso.compra, got.Format("2006-01-02"), caso.vencimento)
		}
	}
}

// Fechamento depois do vencimento: a fatura vence no mês SEGUINTE.
func TestCartao_VencimentoNoMesSeguinte(t *testing.T) {
	c := cartao(t, 28, 5) // fecha 28, vence 5 — o formato mais comum

	casos := []struct{ compra, vencimento string }{
		{"2026-09-27", "2026-10-05"},
		{"2026-09-28", "2026-10-05"},
		{"2026-09-29", "2026-11-05"}, // passou do fechamento
		{"2026-10-28", "2026-11-05"},
	}
	for _, caso := range casos {
		if got := c.FaturaDaCompra(dia(caso.compra)); !got.Equal(dia(caso.vencimento)) {
			t.Errorf("compra %s -> fatura %s; queria %s",
				caso.compra, got.Format("2006-01-02"), caso.vencimento)
		}
	}
}

// Fechamento dia 31 em meses curtos cai no último dia.
func TestCartao_MesesCurtos(t *testing.T) {
	c := cartao(t, 31, 10)

	if got := c.FechamentoDoMes(dia("2026-02-15")); !got.Equal(dia("2026-02-28")) {
		t.Errorf("fechamento de fevereiro = %s; queria 2026-02-28", got.Format("2006-01-02"))
	}
	// Comprou em 28/02 (dia do fechamento) -> ainda entra na fatura que vence em março.
	if got := c.FaturaDaCompra(dia("2026-02-28")); !got.Equal(dia("2026-03-10")) {
		t.Errorf("compra 28/02 -> %s; queria 2026-03-10", got.Format("2006-01-02"))
	}
	// Comprou em 01/03, depois do fechamento de fevereiro.
	if got := c.FaturaDaCompra(dia("2026-03-01")); !got.Equal(dia("2026-04-10")) {
		t.Errorf("compra 01/03 -> %s; queria 2026-04-10", got.Format("2006-01-02"))
	}
}

func TestCartao_CicloDaFatura(t *testing.T) {
	c := cartao(t, 20, 21)
	venc := dia("2026-09-21")
	if got := c.FimDoCiclo(venc); !got.Equal(dia("2026-09-20")) {
		t.Errorf("fim do ciclo = %s; queria 2026-09-20", got.Format("2006-01-02"))
	}
	if got := c.InicioDoCiclo(venc); !got.Equal(dia("2026-08-21")) {
		t.Errorf("início do ciclo = %s; queria 2026-08-21", got.Format("2006-01-02"))
	}

	// Com vencimento no mês seguinte o ciclo também tem que fechar direito.
	d := cartao(t, 28, 5)
	venc = dia("2026-10-05")
	if got := d.FimDoCiclo(venc); !got.Equal(dia("2026-09-28")) {
		t.Errorf("fim do ciclo = %s; queria 2026-09-28", got.Format("2006-01-02"))
	}
	if got := d.InicioDoCiclo(venc); !got.Equal(dia("2026-08-29")) {
		t.Errorf("início do ciclo = %s; queria 2026-08-29", got.Format("2006-01-02"))
	}
}

// Todo dia do ciclo tem que cair na fatura daquele ciclo — sem buraco nem
// sobreposição entre faturas consecutivas.
func TestCartao_CicloCobreTodosOsDiasSemBuraco(t *testing.T) {
	// Inclui os pares de borda: fechamento e vencimento no fim do mês, iguais,
	// e invertidos — é onde o cálculo de "qual ciclo é este" costuma escorregar.
	config := []*Cartao{
		cartao(t, 20, 21), cartao(t, 28, 5), cartao(t, 1, 15), cartao(t, 31, 10),
		cartao(t, 31, 30), cartao(t, 30, 31), cartao(t, 15, 15), cartao(t, 1, 1),
		cartao(t, 29, 1), cartao(t, 5, 28),
	}
	for _, c := range config {
		// Dois anos, atravessando fevereiro bissexto (2028) e comum (2026/27).
		for d := dia("2026-01-01"); d.Before(dia("2028-06-01")); d = d.AddDate(0, 0, 1) {
			venc := c.FaturaDaCompra(d)
			inicio, fim := c.InicioDoCiclo(venc), c.FimDoCiclo(venc)
			if d.Before(inicio) || d.After(fim) {
				t.Fatalf("cartão %d/%d: compra em %s caiu na fatura de %s, cujo ciclo é %s a %s",
					c.DiaFechamento, c.DiaVencimento, d.Format("2006-01-02"),
					venc.Format("2006-01-02"), inicio.Format("2006-01-02"), fim.Format("2006-01-02"))
			}
		}
	}
}

func TestCartao_MelhorDiaDeCompra(t *testing.T) {
	c := cartao(t, 20, 21)
	if got := c.MelhorDiaCompra(dia("2026-09-05")); !got.Equal(dia("2026-09-21")) {
		t.Errorf("melhor dia = %s; queria 2026-09-21", got.Format("2006-01-02"))
	}
	// Comprar no melhor dia tem que render a fatura mais distante possível.
	melhor := c.MelhorDiaCompra(dia("2026-09-05"))
	if venc := c.FaturaDaCompra(melhor); !venc.Equal(dia("2026-10-21")) {
		t.Errorf("compra no melhor dia vence em %s; queria 2026-10-21", venc.Format("2006-01-02"))
	}
}

func TestCartao_Validacao(t *testing.T) {
	casos := []struct {
		nome string
		in   NewCartaoInput
	}{
		{"sem nome", NewCartaoInput{UserID: "u1", DiaFechamento: 20, DiaVencimento: 21}},
		{"fechamento 0", NewCartaoInput{UserID: "u1", Nome: "X", DiaFechamento: 0, DiaVencimento: 21}},
		{"fechamento 32", NewCartaoInput{UserID: "u1", Nome: "X", DiaFechamento: 32, DiaVencimento: 21}},
		{"vencimento 0", NewCartaoInput{UserID: "u1", Nome: "X", DiaFechamento: 20, DiaVencimento: 0}},
		{"limite negativo", NewCartaoInput{UserID: "u1", Nome: "X", DiaFechamento: 20, DiaVencimento: 21, Limite: -1}},
	}
	for _, caso := range casos {
		if _, err := NewCartao(caso.in); err == nil {
			t.Errorf("%s: deveria falhar", caso.nome)
		}
	}
}

// Cada fechamento tem que virar uma fatura distinta: se dois fechamentos
// caírem no mesmo vencimento, duas faturas viram uma e um mês de compras soma
// no outro. Foi exatamente o que acontecia com a regra antiga em cartões de
// fim de mês.
func TestCartao_CadaFechamentoGeraUmaFaturaDistinta(t *testing.T) {
	for fechamento := 1; fechamento <= 31; fechamento++ {
		for vencimento := 1; vencimento <= 31; vencimento++ {
			c := cartao(t, fechamento, vencimento)
			vistos := map[string]string{} // vencimento -> fechamento que o gerou
			for mes := dia("2026-01-01"); mes.Before(dia("2028-06-01")); mes = AddMonthsClamped(mes, 1) {
				f := c.FechamentoDoMes(mes)
				v := c.VencimentoDoFechamento(f)

				chave := v.Format("2006-01-02")
				if antes, repetido := vistos[chave]; repetido {
					t.Fatalf("cartão %d/%d: fechamentos %s e %s caem no mesmo vencimento %s",
						fechamento, vencimento, antes, f.Format("2006-01-02"), chave)
				}
				vistos[chave] = f.Format("2006-01-02")

				// E o caminho de volta tem que devolver o mesmo fechamento.
				if volta := c.FimDoCiclo(v); !MesmaData(volta, f) {
					t.Fatalf("cartão %d/%d: fechamento %s -> vencimento %s -> voltou %s",
						fechamento, vencimento, f.Format("2006-01-02"), chave,
						volta.Format("2006-01-02"))
				}
				// O vencimento nunca pode vir antes do fechamento.
				if v.Before(f) {
					t.Fatalf("cartão %d/%d: fecha %s mas vence antes, em %s",
						fechamento, vencimento, f.Format("2006-01-02"), chave)
				}
			}
		}
	}
}
