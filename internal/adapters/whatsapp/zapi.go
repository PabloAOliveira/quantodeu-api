package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// ZAPIConverter converte o webhook "Ao receber" (ReceivedCallback) da Z-API.
type ZAPIConverter struct {
	Secret string
}

var _ Converter = (*ZAPIConverter)(nil)

// Name implementa Converter.
func (*ZAPIConverter) Name() string { return "zapi" }

// Ack implementa Converter.
func (*ZAPIConverter) Ack() (string, []byte) { return "application/json", jsonAck }

// Authenticate implementa Converter.
func (c *ZAPIConverter) Authenticate(r *http.Request, _ []byte) error {
	return sharedTokenAuth(r, c.Secret)
}

type zapiPayload struct {
	Type      string      `json:"type"`
	Phone     string      `json:"phone"`
	MessageID string      `json:"messageId"`
	FromMe    bool        `json:"fromMe"`
	IsGroup   bool        `json:"isGroup"`
	Momment   json.Number `json:"momment"` // (sic) nome do campo na Z-API
	Text      struct {
		Message string `json:"message"`
	} `json:"text"`
}

// Convert implementa Converter.
func (c *ZAPIConverter) Convert(_ *http.Request, body []byte) ([]domain.MensagemRecebida, error) {
	var p zapiPayload
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if p.Type != "" && p.Type != "ReceivedCallback" {
		return nil, nil
	}
	return []domain.MensagemRecebida{{
		Provider:   c.Name(),
		MessageID:  p.MessageID,
		Telefone:   p.Phone,
		Texto:      p.Text.Message,
		FromMe:     p.FromMe,
		IsGroup:    p.IsGroup || strings.Contains(p.Phone, "-group"),
		RecebidaEm: unixToTime(string(p.Momment)),
	}}, nil
}

// ZAPISender envia mensagens pela Z-API.
type ZAPISender struct {
	BaseURL     string
	InstanceID  string
	Token       string
	ClientToken string
	Client      *http.Client
}

var _ ports.WhatsAppSender = (*ZAPISender)(nil)

// SendText implementa ports.WhatsAppSender.
func (s *ZAPISender) SendText(ctx context.Context, telefone, texto string) error {
	endpoint := fmt.Sprintf("%s/instances/%s/token/%s/send-text",
		strings.TrimRight(s.BaseURL, "/"), url.PathEscape(s.InstanceID), url.PathEscape(s.Token))
	payload, _ := json.Marshal(map[string]string{"phone": telefone, "message": texto})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.ClientToken != "" {
		req.Header.Set("Client-Token", s.ClientToken)
	}
	return doRequest(s.Client, req)
}
