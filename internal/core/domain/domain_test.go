package domain

import (
	"testing"
	"time"
)

func TestParseMoney(t *testing.T) {
	ok := map[string]Money{
		"150": 15000, "150,00": 15000, "150,5": 15050, "80.50": 8050,
		"1.234,56": 123456, "1,234.56": 123456, "1.500": 150000, "1.234.567": 123456700,
		"R$ 1.500,00": 150000, "r$10": 1000, "1 500,00": 150000, "0,01": 1, "-10,00": -1000,
		",50": 50,
	}
	for in, want := range ok {
		got, err := ParseMoney(in)
		if err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "10,123,45", "1.2.3,456", "12,345"} {
		if in == "12,345" { // interpretado como milhar: 12345 reais
			if got, err := ParseMoney(in); err != nil || got != 1234500 {
				t.Errorf("ParseMoney(%q) = %d, %v", in, got, err)
			}
			continue
		}
		if _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) deveria falhar", in)
		}
	}
}

func TestMoneyFormat(t *testing.T) {
	if got := Money(123456789).BRL(); got != "R$ 1.234.567,89" {
		t.Errorf("BRL = %q", got)
	}
	if got := Money(-5).String(); got != "-0.05" {
		t.Errorf("String = %q", got)
	}
}

func TestNormalizePhoneBR(t *testing.T) {
	ok := map[string]string{
		"(11) 98765-4321":                 "5511987654321",
		"+55 11 98765-4321":               "5511987654321",
		"11987654321":                     "5511987654321",
		"1133334444":                      "551133334444",
		"5511987654321@s.whatsapp.net":    "5511987654321",
		"5511987654321:12@s.whatsapp.net": "5511987654321",
		"whatsapp:+5511987654321":         "5511987654321",
		"011987654321":                    "5511987654321",
	}
	for in, want := range ok {
		got, err := NormalizePhoneBR(in)
		if err != nil || got != want {
			t.Errorf("NormalizePhoneBR(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"123", "+1 415 555 1234 00", "11887654321", "0087654321"} {
		if _, err := NormalizePhoneBR(in); err == nil {
			t.Errorf("NormalizePhoneBR(%q) deveria falhar", in)
		}
	}
}

func TestPhoneLookupCandidates(t *testing.T) {
	c, err := PhoneLookupCandidates("551187654321@s.whatsapp.net")
	if err != nil || len(c) != 2 || c[1] != "5511987654321" {
		t.Fatalf("candidates = %v, %v", c, err)
	}
	c, _ = PhoneLookupCandidates("551112025266") // faixa nova de celular (91xxx)
	if len(c) != 2 || c[1] != "5511912025266" {
		t.Fatalf("candidates = %v", c)
	}
	c, _ = PhoneLookupCandidates("5511987654321")
	if len(c) != 2 || c[1] != "551187654321" {
		t.Fatalf("candidates = %v", c)
	}
}

func TestNewUserValidacoes(t *testing.T) {
	base := NewUserInput{Nome: "Maria Silva", Email: "Maria@Example.com", Telefone: "11987654321", Senha: "senhaForte1"}
	u, err := NewUser(base)
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "maria@example.com" || u.Telefone != "5511987654321" {
		t.Fatalf("normalização falhou: %+v", u)
	}
	bad := base
	bad.Senha = "semnumero"
	if _, err := NewUser(bad); err == nil {
		t.Error("senha sem número deveria falhar")
	}
	bad = base
	bad.Email = "maria@localhost"
	if _, err := NewUser(bad); err == nil {
		t.Error("e-mail sem domínio deveria falhar")
	}
}

func TestParcelamentoGerarParcelas(t *testing.T) {
	inicio := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	p, err := NewParcelamento(NewParcelamentoInput{
		UserID: "u1", Tipo: ParcelamentoParcelado, Descricao: "Notebook", Categoria: "Eletrônicos",
		ValorTotal: 100000, TotalParcelas: 3, DataPrimeiraParcela: inicio,
	})
	if err != nil {
		t.Fatal(err)
	}
	ps := p.GerarParcelas()
	if len(ps) != 3 {
		t.Fatalf("len = %d", len(ps))
	}
	var soma Money
	for _, x := range ps {
		soma += x.Valor
	}
	if soma != 100000 {
		t.Errorf("soma das parcelas = %d; want 100000", soma)
	}
	if ps[0].Valor != 33334 || ps[1].Valor != 33333 {
		t.Errorf("valores = %d, %d", ps[0].Valor, ps[1].Valor)
	}
	if d := ps[1].Data; d.Month() != time.February || d.Day() != 28 {
		t.Errorf("2ª parcela em %s; want 28/02", d.Format("2006-01-02"))
	}
	if ps[2].Descricao != "Notebook (3/3)" || ps[2].Categoria != "eletrônicos" {
		t.Errorf("parcela 3 = %q / %q", ps[2].Descricao, ps[2].Categoria)
	}

	r, err := NewParcelamento(NewParcelamentoInput{
		UserID: "u1", Tipo: ParcelamentoRecorrente, Descricao: "Academia",
		ValorParcela: 9990, TotalParcelas: 12, DataPrimeiraParcela: inicio,
	})
	if err != nil || r.ValorTotal != 119880 {
		t.Fatalf("recorrente: %+v, %v", r, err)
	}
}
