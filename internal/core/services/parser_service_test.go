package services

import (
	"errors"
	"testing"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

func TestRegexParserService_Parse(t *testing.T) {
	p := NewRegexParserService()

	tests := []struct {
		in        string
		tipo      domain.TipoTransacao
		valor     domain.Money
		categoria string
		descricao string
	}{
		{"Gastei 150 mercado", domain.TipoSaida, 15000, "mercado", "mercado"},
		{"Gastei 150,00 mercado", domain.TipoSaida, 15000, "mercado", "mercado"},
		{"gastei 80.50 gasolina", domain.TipoSaida, 8050, "gasolina", "gasolina"},
		{"Paguei R$ 1.234,56 de aluguel", domain.TipoSaida, 123456, "aluguel", "aluguel"},
		{"Paguei R$89,9 conta de luz", domain.TipoSaida, 8990, "conta", "conta de luz"},
		{"Saida 45 uber", domain.TipoSaida, 4500, "uber", "uber"},
		{"Saída: 12,5 padaria", domain.TipoSaida, 1250, "padaria", "padaria"},
		{"SAIDA 30", domain.TipoSaida, 3000, "outros", "outros"},
		{"Comprei 59,90 reais no ifood", domain.TipoSaida, 5990, "ifood", "ifood"},
		{"Gastei no mercado 150", domain.TipoSaida, 15000, "mercado", "mercado"},
		{"Gastei com gasolina do carro R$ 200,00", domain.TipoSaida, 20000, "gasolina", "gasolina do carro"},
		{"Entrou 3000 salário", domain.TipoEntrada, 300000, "salário", "salário"},
		{"Recebi 1.500 do freela", domain.TipoEntrada, 150000, "freela", "freela"},
		{"Ganhei 250,75 freela design", domain.TipoEntrada, 25075, "freela", "freela design"},
		{"recebi 1,234.56 consultoria", domain.TipoEntrada, 123456, "consultoria", "consultoria"},
		{"Rendeu 12,34 poupança", domain.TipoEntrada, 1234, "rendimentos", "poupança"},
		{"  Gastei 10 café ☕ ", domain.TipoSaida, 1000, "café", "café"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := p.Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) erro inesperado: %v", tt.in, err)
			}
			if got.Tipo != tt.tipo || got.Valor != tt.valor || got.Categoria != tt.categoria || got.Descricao != tt.descricao {
				t.Fatalf("Parse(%q) = {%s %d %q %q}; want {%s %d %q %q}",
					tt.in, got.Tipo, got.Valor, got.Categoria, got.Descricao,
					tt.tipo, tt.valor, tt.categoria, tt.descricao)
			}
		})
	}
}

func TestRegexParserService_ParseInvalido(t *testing.T) {
	p := NewRegexParserService()
	for _, in := range []string{
		"", "oi", "bom dia, tudo bem?", "mercado 150", "gastei muito hoje",
		"gastei 0 nada", "gastei abc mercado", "150 gastei",
	} {
		if _, err := p.Parse(in); !errors.Is(err, domain.ErrUnparseableMessage) {
			t.Errorf("Parse(%q) esperava ErrUnparseableMessage, obteve %v", in, err)
		}
	}
}
