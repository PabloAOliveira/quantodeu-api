package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // exigido pelo algoritmo de assinatura do Twilio
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// TwilioConverter converte o webhook de mensagens recebidas do Twilio
// (application/x-www-form-urlencoded) e valida o X-Twilio-Signature.
type TwilioConverter struct {
	AuthToken  string
	WebhookURL string // URL pública EXATA configurada no console do Twilio
}

var _ Converter = (*TwilioConverter)(nil)

// Name implementa Converter.
func (*TwilioConverter) Name() string { return "twilio" }

// Ack devolve TwiML vazio (o Twilio espera XML).
func (*TwilioConverter) Ack() (string, []byte) {
	return "text/xml", []byte(`<?xml version="1.0" encoding="UTF-8"?><Response></Response>`)
}

// Authenticate valida a assinatura HMAC-SHA1 do Twilio:
// base64(HMAC-SHA1(authToken, URL + concat(sorted(key+value))))
func (c *TwilioConverter) Authenticate(r *http.Request, body []byte) error {
	sig := r.Header.Get("X-Twilio-Signature")
	if sig == "" || c.AuthToken == "" {
		return ErrUnauthorizedWebhook
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return ErrUnauthorizedWebhook
	}
	expected := TwilioSignature(c.AuthToken, c.WebhookURL, form)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return ErrUnauthorizedWebhook
	}
	return nil
}

// TwilioSignature calcula a assinatura esperada (exportada para testes).
func TwilioSignature(authToken, fullURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		vals := append([]string(nil), form[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			b.WriteString(k)
			b.WriteString(v)
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Convert implementa Converter.
func (c *TwilioConverter) Convert(_ *http.Request, body []byte) ([]domain.MensagemRecebida, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	from := form.Get("From")
	if !strings.HasPrefix(strings.ToLower(from), "whatsapp:") {
		return nil, nil
	}
	return []domain.MensagemRecebida{{
		Provider:  c.Name(),
		MessageID: form.Get("MessageSid"),
		Telefone:  from,
		Texto:     form.Get("Body"),
	}}, nil
}

// TwilioSender envia mensagens pela API REST do Twilio.
type TwilioSender struct {
	AccountSID string
	AuthToken  string
	From       string // ex.: whatsapp:+14155238886
	BaseURL    string // padrão https://api.twilio.com
	Client     *http.Client
}

var _ ports.WhatsAppSender = (*TwilioSender)(nil)

// SendText implementa ports.WhatsAppSender.
func (s *TwilioSender) SendText(ctx context.Context, telefone, texto string) error {
	base := s.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	endpoint := fmt.Sprintf("%s/2010-04-01/Accounts/%s/Messages.json", strings.TrimRight(base, "/"), url.PathEscape(s.AccountSID))
	form := url.Values{"From": {s.From}, "To": {"whatsapp:+" + strings.TrimPrefix(telefone, "+")}, "Body": {texto}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.AccountSID, s.AuthToken)
	return doRequest(s.Client, req)
}

// NoopSender descarta as respostas (quando WHATSAPP_REPLY_ENABLED=false).
type NoopSender struct{}

// SendText implementa ports.WhatsAppSender.
func (NoopSender) SendText(context.Context, string, string) error { return nil }

// LogSender apenas registra no log o que SERIA enviado. Útil em
// desenvolvimento para testar respostas e códigos sem um provedor real.
// NUNCA use em produção: o conteúdo (incluindo códigos) vai para o log.
type LogSender struct {
	Log *slog.Logger
}

// SendText implementa ports.WhatsAppSender.
func (s LogSender) SendText(ctx context.Context, telefone, texto string) error {
	s.Log.InfoContext(ctx, "[DEV] mensagem WhatsApp (não enviada)", slog.String("para", telefone), slog.String("texto", texto))
	return nil
}
