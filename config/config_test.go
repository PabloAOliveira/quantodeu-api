package config

import (
	"os"
	"strings"
	"testing"
)

// base define o mínimo para a config carregar, deixando o teste livre para
// mexer só no que interessa.
func base(t *testing.T) {
	t.Helper()
	os.Clearenv()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("SESSION_SECRETS", strings.Repeat("s", 40))
}

func carregar(t *testing.T) error {
	t.Helper()
	_, err := Load()
	return err
}

// Em produção, exigir credenciais de WhatsApp de quem não usa WhatsApp travaria
// o deploy do v1 — que sobe sem bot nenhum.
func TestProducaoSemBotNaoExigeWhatsApp(t *testing.T) {
	base(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("SESSION_COOKIE_NAME", "__Host-quantodeu_session")
	t.Setenv("METRICS_ADDR", "127.0.0.1:9091")
	t.Setenv("EMAIL_PROVIDER", "smtp")
	t.Setenv("SMTP_HOST", "smtp.gmail.com")
	t.Setenv("SMTP_FROM", "QuantoDeu <conta@gmail.com>")
	t.Setenv("SMTP_USERNAME", "conta@gmail.com")
	t.Setenv("SMTP_PASSWORD", "abcdefghijklmnop")

	if err := carregar(t); err != nil {
		t.Fatalf("deveria carregar sem WHATSAPP_WEBHOOK_SECRET: %v", err)
	}

	// Com o bot ligado (v2), aí sim o segredo do webhook é obrigatório.
	t.Setenv("WHATSAPP_BOT_ENABLED", "true")
	err := carregar(t)
	if err == nil || !strings.Contains(err.Error(), "WHATSAPP_WEBHOOK_SECRET") {
		t.Fatalf("com bot ligado deveria exigir o segredo do webhook: %v", err)
	}
}

func TestSMTPGmail(t *testing.T) {
	casos := []struct {
		nome   string
		env    map[string]string
		contem string // vazio = deve passar
	}{
		{
			nome: "senha de app válida",
			env:  map[string]string{"SMTP_PASSWORD": "abcd efgh ijkl mnop"}, // o Google mostra com espaços
		},
		{
			nome:   "sem credenciais",
			env:    map[string]string{"SMTP_USERNAME": "", "SMTP_PASSWORD": ""},
			contem: "senha de app",
		},
		{
			nome:   "senha da conta em vez da senha de app",
			env:    map[string]string{"SMTP_PASSWORD": "minhaSenhaNormal123"},
			contem: "senha de APP",
		},
		{
			nome:   "porta 465 com starttls",
			env:    map[string]string{"SMTP_PORT": "465"},
			contem: "porta 465",
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			base(t)
			t.Setenv("EMAIL_PROVIDER", "smtp")
			t.Setenv("SMTP_HOST", "smtp.gmail.com")
			t.Setenv("SMTP_FROM", "QuantoDeu <conta@gmail.com>")
			t.Setenv("SMTP_USERNAME", "conta@gmail.com")
			t.Setenv("SMTP_PASSWORD", "abcdefghijklmnop")
			for k, v := range c.env {
				t.Setenv(k, v)
			}

			err := carregar(t)
			if c.contem == "" {
				if err != nil {
					t.Fatalf("deveria passar: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.contem) {
				t.Fatalf("erro esperado contendo %q; veio: %v", c.contem, err)
			}
		})
	}
}

// Outro SMTP (SES, Brevo, servidor próprio) não leva as regras do Gmail.
func TestSMTPNaoGmailSemRegrasDoGmail(t *testing.T) {
	base(t)
	t.Setenv("EMAIL_PROVIDER", "smtp")
	t.Setenv("SMTP_HOST", "email-smtp.sa-east-1.amazonaws.com")
	t.Setenv("SMTP_FROM", "QuantoDeu <no-reply@quantodeu.com.br>")
	t.Setenv("SMTP_USERNAME", "AKIAEXEMPLO")
	t.Setenv("SMTP_PASSWORD", "uma-senha-longa-do-ses-que-nao-tem-16")

	if err := carregar(t); err != nil {
		t.Fatalf("SMTP genérico não deveria cair nas regras do Gmail: %v", err)
	}
}
