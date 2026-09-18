package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Money representa um valor monetário em CENTAVOS (BRL).
// Nunca usamos float para dinheiro: evita erros de arredondamento.
type Money int64

// MaxMoney limita valores absurdos (R$ 1 bilhão) para evitar overflow em somas.
const MaxMoney Money = 100_000_000_000

// Reais devolve o valor como float apenas para exibição/serialização.
func (m Money) Reais() float64 { return float64(m) / 100 }

// String formata o valor no padrão decimal com ponto: "1234.56".
func (m Money) String() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// BRL formata o valor no padrão brasileiro: "R$ 1.234,56".
func (m Money) BRL() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		v = -v
	}
	inteiro := strconv.FormatInt(v/100, 10)
	var b strings.Builder
	for i, r := range inteiro {
		if i > 0 && (len(inteiro)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return fmt.Sprintf("%sR$ %s,%02d", sign, b.String(), v%100)
}

// ParseMoney converte representações textuais comuns em Money.
//
// Aceita: "150", "150,00", "150,5", "80.50", "1.234,56", "1,234.56",
// "1234.5", "R$ 1.500", "1 500,00", "-10,00".
//
// Heurística para o separador decimal:
//   - se existem '.' e ',' o ÚLTIMO a aparecer é o decimal;
//   - se existe só um tipo de separador e ele aparece uma única vez seguido de
//     1 ou 2 dígitos, é decimal ("80.50", "150,5");
//   - caso contrário é separador de milhar ("1.500", "1.234.567").
func ParseMoney(raw string) (Money, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.ToUpper(s), "R$")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, NewValidationError("valor", "valor vazio")
	}

	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	}

	for _, r := range s {
		if !(r >= '0' && r <= '9') && r != '.' && r != ',' {
			return 0, NewValidationError("valor", fmt.Sprintf("valor inválido: %q", raw))
		}
	}

	lastDot := strings.LastIndex(s, ".")
	lastComma := strings.LastIndex(s, ",")
	decimalSep := byte(0)

	switch {
	case lastDot >= 0 && lastComma >= 0:
		if lastDot > lastComma {
			decimalSep = '.'
		} else {
			decimalSep = ','
		}
	case lastDot >= 0 || lastComma >= 0:
		sep := byte('.')
		idx := lastDot
		if lastComma >= 0 {
			sep, idx = ',', lastComma
		}
		digitsAfter := len(s) - idx - 1
		if strings.Count(s, string(sep)) == 1 && digitsAfter >= 1 && digitsAfter <= 2 {
			decimalSep = sep
		}
	}

	intPart, fracPart := s, ""
	if decimalSep != 0 {
		idx := strings.LastIndexByte(s, decimalSep)
		intPart, fracPart = s[:idx], s[idx+1:]
	}
	if strings.ContainsAny(intPart, ".,") && !validThousands(intPart) {
		return 0, NewValidationError("valor", fmt.Sprintf("valor inválido: %q", raw))
	}
	intPart = strings.NewReplacer(".", "", ",", "").Replace(intPart)

	if strings.ContainsAny(fracPart, ".,") || len(fracPart) > 2 {
		return 0, NewValidationError("valor", fmt.Sprintf("valor inválido: %q", raw))
	}
	if intPart == "" {
		intPart = "0"
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}

	reais, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || reais > math.MaxInt64/100 {
		return 0, NewValidationError("valor", fmt.Sprintf("valor inválido: %q", raw))
	}
	cents, _ := strconv.ParseInt(fracPart, 10, 64)

	m := Money(reais*100 + cents)
	if m > MaxMoney {
		return 0, NewValidationError("valor", "valor acima do limite permitido")
	}
	if negative {
		m = -m
	}
	return m, nil
}

// validThousands verifica o agrupamento de milhar: "1.234.567" ou "1,234".
// Aceita apenas UM tipo de separador na parte inteira; o primeiro grupo tem
// 1..3 dígitos e os demais exatamente 3.
func validThousands(s string) bool {
	if strings.Contains(s, ".") && strings.Contains(s, ",") {
		return false
	}
	groups := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == ',' })
	if len(groups) != strings.Count(s, ".")+strings.Count(s, ",")+1 {
		return false // separadores consecutivos ou nas pontas
	}
	for i, g := range groups {
		if (i == 0 && (len(g) < 1 || len(g) > 3)) || (i > 0 && len(g) != 3) {
			return false
		}
	}
	return true
}
