package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// ---------------------------------------------------------------------------
// Evolution API — converter
// ---------------------------------------------------------------------------

// EvolutionConverter converte o evento MESSAGES_UPSERT da Evolution API (v2).
type EvolutionConverter struct {
	Secret string
}

var _ Converter = (*EvolutionConverter)(nil)

// Name implementa Converter.
func (*EvolutionConverter) Name() string { return "evolution" }

// Ack implementa Converter.
func (*EvolutionConverter) Ack() (string, []byte) { return "application/json", jsonAck }

// Authenticate implementa Converter.
func (c *EvolutionConverter) Authenticate(r *http.Request, _ []byte) error {
	return sharedTokenAuth(r, c.Secret)
}

type evolutionPayload struct {
	Event    string          `json:"event"`
	Instance string          `json:"instance"`
	Data     json.RawMessage `json:"data"`
}

type evolutionMessage struct {
	Key struct {
		RemoteJID    string `json:"remoteJid"`
		RemoteJIDAlt string `json:"remoteJidAlt"`
		SenderPn     string `json:"senderPn"`
		FromMe       bool   `json:"fromMe"`
		ID           string `json:"id"`
	} `json:"key"`
	Message struct {
		Conversation        string `json:"conversation"`
		ExtendedTextMessage struct {
			Text string `json:"text"`
		} `json:"extendedTextMessage"`
	} `json:"message"`
	MessageTimestamp json.Number `json:"messageTimestamp"`
}

// Convert implementa Converter.
func (c *EvolutionConverter) Convert(_ *http.Request, body []byte) ([]domain.MensagemRecebida, error) {
	var p evolutionPayload
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	event := strings.ToLower(strings.ReplaceAll(p.Event, "_", "."))
	if event != "messages.upsert" || len(p.Data) == 0 {
		return nil, nil
	}

	// "data" pode ser objeto ou array, dependendo da versão.
	var msgs []evolutionMessage
	trimmed := bytes.TrimSpace(p.Data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &msgs); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	} else {
		var m evolutionMessage
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
		msgs = []evolutionMessage{m}
	}

	out := make([]domain.MensagemRecebida, 0, len(msgs))
	for _, m := range msgs {
		jid := m.Key.RemoteJID
		// Contas com LID (@lid) não expõem o telefone no remoteJid.
		if strings.HasSuffix(jid, "@lid") {
			if m.Key.SenderPn != "" {
				jid = m.Key.SenderPn
			} else if m.Key.RemoteJIDAlt != "" {
				jid = m.Key.RemoteJIDAlt
			}
		}
		texto := m.Message.Conversation
		if texto == "" {
			texto = m.Message.ExtendedTextMessage.Text
		}
		out = append(out, domain.MensagemRecebida{
			Provider:   c.Name(),
			MessageID:  m.Key.ID,
			Telefone:   jid,
			Texto:      texto,
			FromMe:     m.Key.FromMe,
			IsGroup:    strings.HasSuffix(m.Key.RemoteJID, "@g.us") || strings.HasSuffix(m.Key.RemoteJID, "@broadcast"),
			RecebidaEm: unixToTime(string(m.MessageTimestamp)),
		})
	}
	return out, nil
}

// unixToTime aceita timestamps em segundos ou milissegundos.
func unixToTime(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	if n > 1e12 {
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

// ---------------------------------------------------------------------------
// Evolution API — sender
// ---------------------------------------------------------------------------

// EvolutionSender envia mensagens de texto pela Evolution API.
type EvolutionSender struct {
	BaseURL  string
	APIKey   string
	Instance string
	Client   *http.Client
}

var _ ports.WhatsAppSender = (*EvolutionSender)(nil)

// SendText implementa ports.WhatsAppSender.
func (s *EvolutionSender) SendText(ctx context.Context, telefone, texto string) error {
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/message/sendText/" + url.PathEscape(s.Instance)
	payload, _ := json.Marshal(map[string]string{"number": telefone, "text": texto})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.APIKey)
	return doRequest(s.Client, req)
}

func doRequest(client *http.Client, req *http.Request) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("provedor respondeu %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}
