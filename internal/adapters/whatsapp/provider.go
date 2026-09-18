// Package whatsapp contém os adaptadores dos provedores de WhatsApp:
//
//   - Converters (driving): validam a autenticidade do webhook e convertem o
//     payload proprietário em domain.MensagemRecebida.
//   - Senders (driven): implementam ports.WhatsAppSender para responder ao
//     usuário.
//
// Os converters dependem apenas de net/http — nada de Gin — para que possam
// ser reutilizados por outro framework ou por um consumidor de fila.
package whatsapp

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

// ErrUnauthorizedWebhook indica que a requisição não comprovou autenticidade.
var ErrUnauthorizedWebhook = errors.New("webhook não autorizado")

// ErrInvalidPayload indica payload malformado.
var ErrInvalidPayload = errors.New("payload de webhook inválido")

// Converter traduz o webhook de um provedor.
type Converter interface {
	Name() string
	// Authenticate valida a origem da requisição (token ou assinatura).
	Authenticate(r *http.Request, body []byte) error
	// Convert extrai zero ou mais mensagens de texto do payload. Eventos que
	// não são mensagens (status, presença, etc.) devolvem slice vazio.
	Convert(r *http.Request, body []byte) ([]domain.MensagemRecebida, error)
	// Ack devolve o content-type e o corpo esperados pelo provedor.
	Ack() (contentType string, body []byte)
}

// sharedTokenAuth valida um segredo compartilhado enviado em:
//   - header  X-Webhook-Token: <segredo>
//   - header  Authorization: Bearer <segredo>
//   - query   ?token=<segredo>   (para provedores que não enviam headers customizados)
//
// Se secret for vazio, a autenticação é desativada (apenas desenvolvimento;
// a config impede isso em produção).
func sharedTokenAuth(r *http.Request, secret string) error {
	if secret == "" {
		return nil
	}
	candidates := []string{
		r.Header.Get("X-Webhook-Token"),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		r.URL.Query().Get("token"),
	}
	for _, c := range candidates {
		if c != "" && subtle.ConstantTimeCompare([]byte(c), []byte(secret)) == 1 {
			return nil
		}
	}
	return ErrUnauthorizedWebhook
}

var jsonAck = []byte(`{"status":"ok"}`)
