package email

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

func TestBuildMessage_MultipartUTF8(t *testing.T) {
	from, _ := mail.ParseAddress("QuantoDeu <no-reply@quantodeu.com.br>")
	to, _ := mail.ParseAddress("maria@exemplo.com")
	raw, err := buildMessage(from, to, domain.EmailMessage{
		To: "maria@exemplo.com", Subject: "123456 é o seu código", Text: "Olá, Maria!\nCódigo: 123456", HTML: "<p>Olá, <b>123456</b></p>",
	}, time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subj != "123456 é o seu código" {
		t.Fatalf("assunto = %q", subj)
	}
	mt, params, _ := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if mt != "multipart/alternative" {
		t.Fatalf("content-type = %s", mt)
	}
	r := multipart.NewReader(m.Body, params["boundary"])
	var parts []string
	for {
		p, err := r.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		parts = append(parts, p.Header.Get("Content-Type")+"|"+string(b))
	}
	if len(parts) != 2 || !strings.Contains(parts[0], "Olá, Maria!") || !strings.Contains(parts[1], "<b>123456</b>") {
		t.Fatalf("partes = %q", parts)
	}
}

func TestBuildMessage_RejeitaInjecaoDeCabecalho(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@quantodeu.com.br")
	to, _ := mail.ParseAddress("maria@exemplo.com")
	if _, err := buildMessage(from, to, domain.EmailMessage{Subject: "oi\r\nBcc: x@y.com", Text: "x"}, time.Now()); err == nil {
		t.Fatal("assunto com CRLF deveria ser rejeitado")
	}
}

func TestNewSMTPSender_Validacao(t *testing.T) {
	if _, err := NewSMTPSender(SMTPConfig{Host: "smtp.x", Port: 587, From: "QuantoDeu <a@b.com>", TLSMode: "starttls"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []SMTPConfig{
		{Port: 587, From: "a@b.com", TLSMode: "starttls"},
		{Host: "x", Port: 587, From: "sem-arroba", TLSMode: "starttls"},
		{Host: "x", Port: 587, From: "a@b.com", TLSMode: "ssl"},
	} {
		if _, err := NewSMTPSender(c); err == nil {
			t.Errorf("config inválida aceita: %+v", c)
		}
	}
}
