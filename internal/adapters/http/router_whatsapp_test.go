package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/session"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/security"
)

// Com o bot desligado (v1) as rotas de WhatsApp têm de sumir do router.
//
// A comparação é com um caminho que nunca existiu, e não com o 404 direto: o
// preflight de CORS registra um `OPTIONS /api/v1/*path`, então o Gin responde
// 405 para tudo que é desconhecido sob /api/v1. O que interessa é que as rotas
// do bot fiquem indistinguíveis de um caminho inexistente — quem sondar a API
// não descobre que elas existem em outra configuração.
func TestRotasDoWhatsAppSeguemOFlagDoBot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	caminhos := []string{
		"/api/v1/webhook/whatsapp",
		"/api/v1/me/telefone/verificacao",
		"/api/v1/me/telefone/verificacao/confirmar",
	}

	const inexistente = "/api/v1/caminho-que-nunca-existiu"

	for _, bot := range []bool{false, true} {
		signer, err := security.NewCookieSigner([][]byte{[]byte("segredo-de-teste-com-mais-de-32-caracteres")})
		if err != nil {
			t.Fatal(err)
		}
		r, _, err := NewRouter(RouterDeps{
			WhatsAppBot: bot,
			Cookies:     session.NewCookieManager("__Host-teste", signer),
			Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			t.Fatalf("bot=%v: %v", bot, err)
		}

		responder := func(caminho string) int {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, caminho, nil))
			return w.Code
		}
		semRota := responder(inexistente)

		for _, caminho := range caminhos {
			status := responder(caminho)
			registrada := status != semRota
			if registrada != bot {
				t.Errorf("bot=%v, POST %s: status %d (caminho inexistente responde %d)",
					bot, caminho, status, semRota)
			}
		}
	}
}
