// Package email implementa ports.EmailSender (SMTP e log para desenvolvimento).
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// Modos de segurança da conexão SMTP.
const (
	TLSStartTLS = "starttls" // porta 587: conecta em claro e exige STARTTLS
	TLSImplicit = "tls"      // porta 465: TLS desde o primeiro byte
	TLSNone     = "none"     // apenas DEV (ex.: Mailpit na rede do compose)
)

// SMTPConfig configura o envio.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string // vazio = sem autenticação
	Password string
	From     string // "QuantoDeu <no-reply@seudominio.com.br>"
	TLSMode  string // starttls | tls | none
	Timeout  time.Duration
}

// SMTPSender envia e-mails por qualquer servidor SMTP (Gmail, Outlook,
// servidor próprio, Amazon SES, Mailpit...).
type SMTPSender struct {
	cfg  SMTPConfig
	from *mail.Address
}

var _ ports.EmailSender = (*SMTPSender)(nil)

// NewSMTPSender valida a configuração.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if cfg.Host == "" || cfg.Port <= 0 {
		return nil, errors.New("smtp: SMTP_HOST e SMTP_PORT são obrigatórios")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("smtp: SMTP_FROM inválido: %w", err)
	}
	switch cfg.TLSMode {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return nil, fmt.Errorf("smtp: SMTP_TLS deve ser starttls, tls ou none (recebido %q)", cfg.TLSMode)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &SMTPSender{cfg: cfg, from: from}, nil
}

// Send implementa ports.EmailSender.
func (s *SMTPSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	to, err := mail.ParseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("smtp: destinatário inválido: %w", err)
	}
	body, err := buildMessage(s.from, to, msg, time.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{}

	var conn net.Conn
	if s.cfg.TLSMode == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp: conectar em %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: handshake: %w", err)
	}
	defer c.Close()

	if s.cfg.TLSMode == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: servidor não oferece STARTTLS (use SMTP_TLS=tls na porta 465)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: STARTTLS: %w", err)
		}
	}

	if s.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp: servidor não aceita autenticação")
		}
		// PlainAuth recusa enviar a senha sem TLS (exceto para localhost).
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp: autenticação: %w", err)
		}
	}

	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp: escrever mensagem: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: finalizar mensagem: %w", err)
	}
	return c.Quit()
}

// buildMessage monta um MIME multipart/alternative (texto + HTML).
func buildMessage(from, to *mail.Address, msg domain.EmailMessage, now time.Time) ([]byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, errors.New("smtp: assunto com quebra de linha")
	}
	boundary := randomHex(16)
	domainPart := from.Address[strings.LastIndex(from.Address, "@")+1:]

	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%s@%s>", randomHex(12), domainPart))
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")

	part := func(contentType, content string) error {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, contentType)
		qp := quotedprintable.NewWriter(&b)
		if _, err := qp.Write([]byte(strings.ReplaceAll(content, "\n", "\r\n"))); err != nil {
			return err
		}
		if err := qp.Close(); err != nil {
			return err
		}
		b.WriteString("\r\n")
		return nil
	}

	if msg.HTML == "" {
		header("Content-Type", "text/plain; charset=UTF-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(strings.ReplaceAll(msg.Text, "\n", "\r\n")))
		_ = qp.Close()
		return b.Bytes(), nil
	}

	header("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
	b.WriteString("\r\n")
	if err := part("text/plain", msg.Text); err != nil {
		return nil, err
	}
	if err := part("text/html", msg.HTML); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// LogSender apenas registra no log o e-mail que SERIA enviado (DEV).
// NUNCA use em produção: o código de verificação vai para o log.
type LogSender struct {
	Log *slog.Logger
}

var _ ports.EmailSender = LogSender{}

// Send implementa ports.EmailSender.
func (s LogSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	s.Log.InfoContext(ctx, "[DEV] e-mail (não enviado)",
		slog.String("para", msg.To), slog.String("assunto", msg.Subject), slog.String("texto", msg.Text))
	return nil
}
