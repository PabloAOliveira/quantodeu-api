package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func dataBase() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }

func TestFinanciamento_TaxaMensal(t *testing.T) {
	efetiva := Financiamento{TaxaAnual: 8.66, TipoTaxa: TaxaEfetiva}.TaxaMensal()
	if math.Abs(efetiva-0.00694514) > 1e-6 {
		t.Fatalf("taxa efetiva mensal = %.8f", efetiva)
	}
	nominal := Financiamento{TaxaAnual: 12, TipoTaxa: TaxaNominal}.TaxaMensal()
	if math.Abs(nominal-0.01) > 1e-12 {
		t.Fatalf("taxa nominal mensal = %.8f", nominal)
	}
	if z := (Financiamento{TaxaAnual: 0, TipoTaxa: TaxaEfetiva}).TaxaMensal(); z != 0 {
		t.Fatalf("taxa zero = %v", z)
	}
}

func TestFinanciamento_Price(t *testing.T) {
	// R$ 100.000 em 12 meses a 1% a.m. (nominal 12% a.a.) -> PMT = R$ 8.884,88
	f := Financiamento{Sistema: SistemaPrice, ValorFinanciado: 10_000_000, TaxaAnual: 12, TipoTaxa: TaxaNominal}
	cron, err := f.Cronograma(12, dataBase())
	if err != nil {
		t.Fatal(err)
	}
	if len(cron) != 12 {
		t.Fatalf("parcelas = %d", len(cron))
	}
	if cron[0].Valor != 888488 {
		t.Errorf("1ª parcela = %d centavos; esperado 888488", cron[0].Valor)
	}
	if cron[0].Juros != 100000 || cron[0].Amortizacao != 788488 {
		t.Errorf("composição da 1ª parcela = juros %d, amort %d", cron[0].Juros, cron[0].Amortizacao)
	}
	// Parcela constante (exceto a última, que fecha o saldo).
	for _, c := range cron[:11] {
		if c.Valor != cron[0].Valor {
			t.Fatalf("parcela %d = %d (Price deve ser constante)", c.Numero, c.Valor)
		}
	}
	if s := cron[11].SaldoDevedor; s != 0 {
		t.Errorf("saldo final = %d; deveria zerar", s)
	}
	var amort Money
	for _, c := range cron {
		amort += c.Amortizacao
	}
	if amort != f.ValorFinanciado {
		t.Errorf("soma das amortizações = %d; esperado %d", amort, f.ValorFinanciado)
	}
	total, juros := TotaisCronograma(cron)
	if total-juros != f.ValorFinanciado {
		t.Errorf("total %d - juros %d != financiado %d", total, juros, f.ValorFinanciado)
	}
	if juros < 600000 || juros > 700000 { // ~R$ 6.618
		t.Errorf("juros totais = %d centavos", juros)
	}
	if !cron[0].Data.Equal(dataBase()) || !cron[11].Data.Equal(time.Date(2027, 9, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("datas = %v ... %v", cron[0].Data, cron[11].Data)
	}
}

func TestFinanciamento_SAC(t *testing.T) {
	// R$ 100.000 em 10 meses a 1% a.m.: amortização de R$ 10.000/mês.
	f := Financiamento{Sistema: SistemaSAC, ValorFinanciado: 10_000_000, TaxaAnual: 12, TipoTaxa: TaxaNominal}
	cron, err := f.Cronograma(10, dataBase())
	if err != nil {
		t.Fatal(err)
	}
	if cron[0].Valor != 1_100_000 { // 10.000 + 1.000 de juros
		t.Errorf("1ª parcela = %d; esperado 1100000", cron[0].Valor)
	}
	if cron[9].Valor != 1_010_000 { // 10.000 + 100 de juros
		t.Errorf("última parcela = %d; esperado 1010000", cron[9].Valor)
	}
	for k := 1; k < len(cron); k++ {
		if cron[k].Valor >= cron[k-1].Valor {
			t.Fatalf("SAC deveria ser decrescente: %d -> %d", cron[k-1].Valor, cron[k].Valor)
		}
		if cron[k].Amortizacao != cron[0].Amortizacao {
			t.Fatalf("amortização deveria ser constante: %d != %d", cron[k].Amortizacao, cron[0].Amortizacao)
		}
	}
	if cron[9].SaldoDevedor != 0 {
		t.Errorf("saldo final = %d", cron[9].SaldoDevedor)
	}
}

func TestFinanciamento_CentavosFecham(t *testing.T) {
	casos := []Financiamento{
		{Sistema: SistemaPrice, ValorFinanciado: 23_456_789, TaxaAnual: 8.66, TipoTaxa: TaxaEfetiva},
		{Sistema: SistemaSAC, ValorFinanciado: 23_456_789, TaxaAnual: 8.66, TipoTaxa: TaxaEfetiva},
		{Sistema: SistemaPrice, ValorFinanciado: 100_000, TaxaAnual: 0, TipoTaxa: TaxaEfetiva},
		{Sistema: SistemaSAC, ValorFinanciado: 100_001, TaxaAnual: 0, TipoTaxa: TaxaNominal},
	}
	for _, f := range casos {
		for _, n := range []int{1, 7, 360, 420} {
			cron, err := f.Cronograma(n, dataBase())
			if err != nil {
				t.Fatalf("%+v n=%d: %v", f, n, err)
			}
			var amort Money
			for _, c := range cron {
				if c.Valor <= 0 {
					t.Fatalf("%+v n=%d: parcela %d com valor %d", f, n, c.Numero, c.Valor)
				}
				amort += c.Amortizacao
			}
			if amort != f.ValorFinanciado || cron[len(cron)-1].SaldoDevedor != 0 {
				t.Fatalf("%+v n=%d: amortizado %d de %d, saldo final %d", f, n, amort, f.ValorFinanciado, cron[len(cron)-1].SaldoDevedor)
			}
		}
	}
}

func TestFinanciamento_Validacao(t *testing.T) {
	ok := Financiamento{Sistema: "", TipoTaxa: "", ValorFinanciado: 1000, TaxaAnual: 9}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.Sistema != SistemaSAC || ok.TipoTaxa != TaxaEfetiva {
		t.Fatalf("padrões = %s / %s", ok.Sistema, ok.TipoTaxa)
	}
	ruins := []Financiamento{
		{Sistema: "tabela", ValorFinanciado: 1000, TaxaAnual: 9},
		{Sistema: SistemaSAC, TipoTaxa: "x", ValorFinanciado: 1000, TaxaAnual: 9},
		{Sistema: SistemaSAC, ValorFinanciado: 0, TaxaAnual: 9},
		{Sistema: SistemaSAC, ValorFinanciado: 1000, TaxaAnual: 150},
		{Sistema: SistemaSAC, ValorFinanciado: 1000, TaxaAnual: -1},
	}
	for _, f := range ruins {
		if err := f.Validate(); err == nil {
			t.Errorf("deveria recusar %+v", f)
		}
	}
	f := Financiamento{Sistema: SistemaSAC, ValorFinanciado: 100, TaxaAnual: 9, TipoTaxa: TaxaEfetiva}
	if _, err := f.Cronograma(200, dataBase()); err == nil {
		t.Error("deveria recusar parcela menor que R$ 0,01")
	}
}

func TestParcelamento_ComFinanciamento(t *testing.T) {
	p, err := NewParcelamento(NewParcelamentoInput{
		UserID: "u1", Tipo: ParcelamentoParcelado, Descricao: "Apartamento MCMV", Categoria: "Moradia",
		TotalParcelas: 360, DataPrimeiraParcela: dataBase(),
		Financiamento: &Financiamento{Banco: "Caixa Econômica Federal", Sistema: SistemaSAC,
			ValorFinanciado: 20_000_000, TaxaAnual: 8.66, TipoTaxa: TaxaEfetiva},
	})
	if err != nil {
		t.Fatal(err)
	}
	parcelas := p.GerarParcelas()
	if len(parcelas) != 360 {
		t.Fatalf("parcelas = %d", len(parcelas))
	}
	if parcelas[0].Valor != p.ValorParcela {
		t.Errorf("valor_parcela (%d) deveria ser o da 1ª parcela (%d)", p.ValorParcela, parcelas[0].Valor)
	}
	var soma Money
	for _, t := range parcelas {
		soma += t.Valor
	}
	if soma != p.ValorTotal {
		t.Errorf("soma das parcelas %d != valor_total %d", soma, p.ValorTotal)
	}
	if p.ValorTotal <= 20_000_000 {
		t.Errorf("com juros o total (%d) deve superar o financiado", p.ValorTotal)
	}
	if parcelas[0].Valor <= parcelas[359].Valor {
		t.Error("SAC: primeira parcela deveria ser maior que a última")
	}
	// Financiamento exige tipo 'parcelado'.
	if _, err := NewParcelamento(NewParcelamentoInput{UserID: "u1", Tipo: ParcelamentoRecorrente, Descricao: "x",
		TotalParcelas: 10, DataPrimeiraParcela: dataBase(),
		Financiamento: &Financiamento{ValorFinanciado: 1000, TaxaAnual: 5}}); err == nil {
		t.Error("recorrente + financiamento deveria falhar")
	}
}

// ---------------------------------------------------------------------------
// Amortização extraordinária (pagamento extra mensal)
// ---------------------------------------------------------------------------

func contratoSAC() Financiamento {
	return Financiamento{
		Sistema: SistemaSAC, ValorFinanciado: 20000000, // R$ 200.000,00
		TaxaAnual: 8.66, TipoTaxa: TaxaEfetiva,
	}
}

func TestCronogramaComExtraEncurtaPrazoEReduzJuros(t *testing.T) {
	primeira := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	semExtra, err := contratoSAC().Cronograma(360, primeira)
	if err != nil {
		t.Fatalf("cronograma sem extra: %v", err)
	}
	_, jurosSem := TotaisCronograma(semExtra)

	f := contratoSAC()
	f.ExtraMensal = 10000 // R$ 100,00 por mês
	comExtra, err := f.Cronograma(360, primeira)
	if err != nil {
		t.Fatalf("cronograma com extra: %v", err)
	}
	_, jurosCom := TotaisCronograma(comExtra)

	if len(comExtra) >= len(semExtra) {
		t.Errorf("o aporte deveria encurtar o prazo: %d parcelas vs %d", len(comExtra), len(semExtra))
	}
	if jurosCom >= jurosSem {
		t.Errorf("o aporte deveria reduzir os juros: %d vs %d", jurosCom, jurosSem)
	}
	// O extra entra inteiro na amortização da primeira parcela.
	if got, want := comExtra[0].Amortizacao, semExtra[0].Amortizacao+10000; got != want {
		t.Errorf("amortização da 1ª parcela = %d, queria %d", got, want)
	}
}

func TestCronogramaComExtraQuitaExatamente(t *testing.T) {
	primeira := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	casos := []struct {
		nome    string
		sistema SistemaAmortizacao
		extra   Money
	}{
		{"SAC com R$ 100", SistemaSAC, 10000},
		{"SAC com R$ 1.000", SistemaSAC, 100000},
		{"Price com R$ 200", SistemaPrice, 20000},
		{"Price com aporte enorme", SistemaPrice, 50000000},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			f := contratoSAC()
			f.Sistema, f.ExtraMensal = c.sistema, c.extra

			parcelas, err := f.Cronograma(360, primeira)
			if err != nil {
				t.Fatalf("cronograma: %v", err)
			}
			if len(parcelas) == 0 {
				t.Fatal("cronograma vazio")
			}
			// O saldo precisa fechar em zero, sem cobrar a mais.
			if fim := parcelas[len(parcelas)-1].SaldoDevedor; fim != 0 {
				t.Errorf("saldo final = %d, queria 0", fim)
			}
			var amortizado Money
			for _, p := range parcelas {
				if p.Amortizacao < 0 {
					t.Fatalf("parcela %d com amortização negativa: %d", p.Numero, p.Amortizacao)
				}
				amortizado += p.Amortizacao
			}
			if amortizado != f.ValorFinanciado {
				t.Errorf("amortizado = %d, queria %d (o valor financiado)", amortizado, f.ValorFinanciado)
			}
		})
	}
}

func TestCronogramaSemExtraNaoMuda(t *testing.T) {
	primeira := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	f := contratoSAC()
	parcelas, err := f.Cronograma(360, primeira)
	if err != nil {
		t.Fatalf("cronograma: %v", err)
	}
	// Valores conferidos na resposta real da API antes desta mudança.
	if len(parcelas) != 360 {
		t.Fatalf("parcelas = %d, queria 360", len(parcelas))
	}
	if got := parcelas[0].Valor; got != 194658 {
		t.Errorf("1ª parcela = %d, queria 194658", got)
	}
	if got := parcelas[0].Juros; got != 138903 {
		t.Errorf("juros da 1ª = %d, queria 138903", got)
	}
	if got := parcelas[359].Valor; got != 55941 {
		t.Errorf("última parcela = %d, queria 55941", got)
	}
	total, juros := TotaisCronograma(parcelas)
	if total != 45071693 || juros != 25071693 {
		t.Errorf("totais = (%d, %d), queria (45071693, 25071693)", total, juros)
	}
}

func TestPrazoQuitacao(t *testing.T) {
	primeira := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	f := contratoSAC()
	f.ExtraMensal = 10000

	comExtra, contratado, err := f.PrazoQuitacao(360, primeira)
	if err != nil {
		t.Fatalf("prazo: %v", err)
	}
	if contratado != 360 {
		t.Errorf("prazo contratado = %d, queria 360", contratado)
	}
	if comExtra >= contratado {
		t.Errorf("com aporte deveria quitar antes: %d vs %d", comExtra, contratado)
	}
}

func TestValidateRejeitaExtraNegativo(t *testing.T) {
	f := contratoSAC()
	f.ExtraMensal = -1

	err := f.Validate()
	if err == nil {
		t.Fatal("esperava erro de validação")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "financiamento.pagamento_extra_mensal" {
		t.Errorf("erro = %v, queria validation_error em financiamento.pagamento_extra_mensal", err)
	}
}
