package whatsapp

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEvolutionConverter(t *testing.T) {
	c := &EvolutionConverter{Secret: "s3cr3t"}
	body := []byte(`{
	  "event": "messages.upsert", "instance": "quantodeu",
	  "data": {
	    "key": {"remoteJid": "551187654321@s.whatsapp.net", "fromMe": false, "id": "3EB0ABC"},
	    "message": {"conversation": "Gastei 150,00 mercado"},
	    "messageTimestamp": 1757937600
	  }
	}`)

	r := httptest.NewRequest("POST", "/api/v1/webhook/whatsapp", nil)
	if err := c.Authenticate(r, body); !errors.Is(err, ErrUnauthorizedWebhook) {
		t.Fatal("sem token deveria ser rejeitado")
	}
	r.Header.Set("X-Webhook-Token", "s3cr3t")
	if err := c.Authenticate(r, body); err != nil {
		t.Fatal(err)
	}

	msgs, err := c.Convert(r, body)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Convert = %v, %v", msgs, err)
	}
	m := msgs[0]
	if m.Telefone != "551187654321@s.whatsapp.net" || m.Texto != "Gastei 150,00 mercado" || m.MessageID != "3EB0ABC" || m.IsGroup || m.RecebidaEm.IsZero() {
		t.Fatalf("mensagem = %+v", m)
	}

	group := []byte(`{"event":"MESSAGES_UPSERT","data":[{"key":{"remoteJid":"1203@g.us","id":"x"},"message":{"extendedTextMessage":{"text":"oi"}}}]}`)
	msgs, _ = c.Convert(r, group)
	if len(msgs) != 1 || !msgs[0].IsGroup || msgs[0].Texto != "oi" {
		t.Fatalf("grupo = %+v", msgs)
	}

	other, _ := c.Convert(r, []byte(`{"event":"connection.update","data":{}}`))
	if len(other) != 0 {
		t.Fatal("eventos que não são mensagens devem ser ignorados")
	}
}

func TestZAPIConverter(t *testing.T) {
	c := &ZAPIConverter{}
	body := []byte(`{"type":"ReceivedCallback","phone":"5511987654321","messageId":"A1","fromMe":false,"isGroup":false,"momment":1757937600000,"text":{"message":"Recebi 1500 freela"}}`)
	msgs, err := c.Convert(httptest.NewRequest("POST", "/", nil), body)
	if err != nil || len(msgs) != 1 || msgs[0].Texto != "Recebi 1500 freela" || msgs[0].RecebidaEm.Year() != 2025 {
		t.Fatalf("Convert = %+v, %v", msgs, err)
	}
}

func TestTwilioConverter(t *testing.T) {
	c := &TwilioConverter{AuthToken: "tok", WebhookURL: "https://api.quantodeu.app/api/v1/webhook/whatsapp"}
	form := url.Values{"From": {"whatsapp:+5511987654321"}, "Body": {"Paguei 80.50 gasolina"}, "MessageSid": {"SM1"}}
	body := []byte(form.Encode())

	r := httptest.NewRequest("POST", "/api/v1/webhook/whatsapp", strings.NewReader(string(body)))
	r.Header.Set("X-Twilio-Signature", TwilioSignature("tok", c.WebhookURL, form))
	if err := c.Authenticate(r, body); err != nil {
		t.Fatalf("assinatura válida rejeitada: %v", err)
	}
	r.Header.Set("X-Twilio-Signature", "invalida")
	if err := c.Authenticate(r, body); err == nil {
		t.Fatal("assinatura inválida aceita")
	}
	msgs, err := c.Convert(r, body)
	if err != nil || len(msgs) != 1 || msgs[0].MessageID != "SM1" {
		t.Fatalf("Convert = %+v, %v", msgs, err)
	}
}
