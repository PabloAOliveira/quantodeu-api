package domain

import "strings"

// NormalizePhoneBR normaliza um telefone brasileiro para o formato E.164 sem
// o '+': 55 + DDD (2) + número (8 ou 9 dígitos). Ex.: "(11) 98765-4321" ->
// "5511987654321".
//
// Aceita entradas com ou sem DDI, máscara, "whatsapp:+55..." (Twilio) ou
// JIDs do WhatsApp ("5511987654321@s.whatsapp.net").
func NormalizePhoneBR(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(s), "whatsapp:") { // Twilio
		s = s[len("whatsapp:"):]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 { // JID do WhatsApp
		s = s[:i]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 { // sufixo de device no JID "5511...:12"
		s = s[:i]
	}

	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := strings.TrimLeft(b.String(), "0") // remove prefixo de operadora "0"

	switch len(digits) {
	case 10, 11: // DDD + número, sem DDI
		digits = "55" + digits
	case 12, 13:
		if !strings.HasPrefix(digits, "55") {
			return "", NewValidationError("telefone", "apenas telefones brasileiros (+55) são suportados")
		}
	default:
		return "", NewValidationError("telefone", "telefone deve conter DDD e número (ex.: 11987654321)")
	}

	ddd := digits[2:4]
	if ddd[0] == '0' || ddd[1] == '0' {
		return "", NewValidationError("telefone", "DDD inválido")
	}
	local := digits[4:]
	if len(local) == 9 && local[0] != '9' {
		return "", NewValidationError("telefone", "celular com 9 dígitos deve iniciar com 9")
	}
	return digits, nil
}

// PhoneLookupCandidates devolve as variações possíveis de um telefone para
// busca. O WhatsApp frequentemente entrega números de celular brasileiros SEM
// o nono dígito (ex.: 551187654321), enquanto o usuário cadastra COM o nono
// dígito (5511987654321). Buscamos pelas duas formas.
func PhoneLookupCandidates(raw string) ([]string, error) {
	n, err := NormalizePhoneBR(raw)
	if err != nil {
		return nil, err
	}
	candidates := []string{n}
	local := n[4:]
	switch {
	case len(local) == 9 && local[0] == '9':
		candidates = append(candidates, n[:4]+local[1:])
	case len(local) == 8: // sem o nono dígito: tenta também a forma com 9
		// A forma exata vem primeiro, então um fixo cadastrado com os mesmos
		// 8 dígitos continua tendo prioridade.
		candidates = append(candidates, n[:4]+"9"+local)
	}
	return candidates, nil
}
