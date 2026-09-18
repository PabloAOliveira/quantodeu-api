package services

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// RegexParserService interpreta mensagens de WhatsApp em lançamentos usando
// apenas a biblioteca padrão (regexp). Formatos suportados:
//
//	Gastei 150,00 mercado            -> saída   R$ 150,00  categoria "mercado"
//	Paguei R$ 80.50 de gasolina      -> saída   R$ 80,50   categoria "gasolina"
//	Recebi 1.500 do freela           -> entrada R$ 1.500   categoria "freela"
//	Entrou 3000 salário              -> entrada R$ 3.000   categoria "salário"
//	Gastei no mercado 150            -> saída   R$ 150     categoria "mercado"  (valor no final)
//	Rendeu 12,34 poupança            -> entrada R$ 12,34   categoria "rendimentos"
//
// O verbo é obrigatório e deve iniciar a mensagem — isso evita que qualquer
// conversa com número vire lançamento por engano.
type RegexParserService struct{}

var _ ports.MessageParser = (*RegexParserService)(nil)

// NewRegexParserService cria o parser.
func NewRegexParserService() *RegexParserService { return &RegexParserService{} }

const (
	verbosEntrada    = `entrou|entrada|recebi|recebido|ganhei|ganho|vendi|dep[oó]sito|depositaram|pix\s+recebido`
	verbosRendimento = `rendeu|rendimentos?|juros|dividendos?`
	verbosSaida      = `gastei|gasto|gastos|saiu|sa[ií]da|paguei|pagamento|pago|comprei|compra|d[eé]bito|transferi|pix\s+enviado`
	valorPattern     = `\d{1,3}(?:\.\d{3})+(?:,\d{1,2})?|\d{1,3}(?:,\d{3})+(?:\.\d{1,2})?|\d+(?:[.,]\d{1,2})?`
	preposicoes      = `de|do|da|dos|das|no|na|nos|nas|com|em|pra|pro|para|pelo|pela|um|uma`
	sufixoMoeda      = `(?:\s*(?:reais|real|conto|contos|pila|brl))?`
)

var (
	// Verbo + valor + descrição opcional: "Gastei R$ 150,00 no mercado"
	reValorPrimeiro = regexp.MustCompile(
		`(?i)^(?P<verbo>` + verbosEntrada + `|` + verbosRendimento + `|` + verbosSaida + `)` +
			`\s*[:\-]?\s*(?:(?:de|um|uma)\s+)?(?:r\$\s*)?(?P<valor>` + valorPattern + `)` + sufixoMoeda +
			`(?:\s+(?:(?:` + preposicoes + `)\s+)?(?P<desc>.+?))?\s*$`)

	// Verbo + descrição + valor no final: "Gastei no mercado 150"
	reValorNoFinal = regexp.MustCompile(
		`(?i)^(?P<verbo>` + verbosEntrada + `|` + verbosRendimento + `|` + verbosSaida + `)` +
			`\s*[:\-]?\s+(?:(?:` + preposicoes + `)\s+)?(?P<desc>.+?)\s+(?:r\$\s*)?(?P<valor>` + valorPattern + `)` + sufixoMoeda + `\s*$`)

	reEntrada    = regexp.MustCompile(`(?i)^(?:` + verbosEntrada + `)$`)
	reRendimento = regexp.MustCompile(`(?i)^(?:` + verbosRendimento + `)$`)
	reSaida      = regexp.MustCompile(`(?i)^(?:` + verbosSaida + `)$`)
	reEspacos    = regexp.MustCompile(`\s+`)
)

// Parse implementa ports.MessageParser.
func (p *RegexParserService) Parse(texto string) (*domain.LancamentoInterpretado, error) {
	s := limparTexto(texto)
	if s == "" || len(s) > 500 {
		return nil, domain.ErrUnparseableMessage
	}

	verbo, valorStr, desc, ok := match(reValorPrimeiro, s)
	if !ok {
		verbo, valorStr, desc, ok = match(reValorNoFinal, s)
	}
	if !ok {
		return nil, domain.ErrUnparseableMessage
	}

	valor, err := domain.ParseMoney(valorStr)
	if err != nil || valor <= 0 {
		return nil, domain.ErrUnparseableMessage
	}

	verbo = reEspacos.ReplaceAllString(verbo, " ")
	out := &domain.LancamentoInterpretado{Valor: valor, Descricao: desc}

	switch {
	case reRendimento.MatchString(verbo):
		out.Tipo = domain.TipoEntrada
		out.Categoria = domain.CategoriaRendimentos
	case reEntrada.MatchString(verbo):
		out.Tipo = domain.TipoEntrada
		out.Categoria = extrairCategoria(desc)
	case reSaida.MatchString(verbo):
		out.Tipo = domain.TipoSaida
		out.Categoria = extrairCategoria(desc)
	default:
		return nil, domain.ErrUnparseableMessage
	}

	if out.Descricao == "" {
		out.Descricao = out.Categoria
	}
	return out, nil
}

func match(re *regexp.Regexp, s string) (verbo, valor, desc string, ok bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return "", "", "", false
	}
	for i, name := range re.SubexpNames() {
		switch name {
		case "verbo":
			verbo = m[i]
		case "valor":
			valor = m[i]
		case "desc":
			desc = strings.TrimSpace(m[i])
		}
	}
	return verbo, valor, desc, true
}

// limparTexto remove emojis/pontuação nas pontas e espaços duplicados.
func limparTexto(t string) string {
	t = strings.TrimFunc(t, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	return reEspacos.ReplaceAllString(t, " ")
}

// extrairCategoria usa a primeira palavra significativa da descrição.
func extrairCategoria(desc string) string {
	for _, w := range strings.Fields(desc) {
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if w == "" || isPreposicao(w) {
			continue
		}
		return domain.NormalizeCategoria(w)
	}
	return domain.CategoriaPadrao
}

var prepSet = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, p := range strings.Split(preposicoes, "|") {
		m[p] = struct{}{}
	}
	return m
}()

func isPreposicao(w string) bool {
	_, ok := prepSet[strings.ToLower(w)]
	return ok
}
