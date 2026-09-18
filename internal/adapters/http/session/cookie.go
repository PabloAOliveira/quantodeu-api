// Package session gerencia o cookie de sessão HTTP (assinatura, leitura,
// escrita e remoção) de forma centralizada, garantindo que TODOS os pontos
// da aplicação apliquem os mesmos atributos de segurança.
package session

import (
	"net/http"
	"strings"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/security"
)

// CookieManager aplica a política do cookie de sessão.
type CookieManager struct {
	Name   string
	Signer *security.CookieSigner
}

// NewCookieManager cria o gerenciador. Recomenda-se o prefixo "__Host-", que
// obriga o navegador a exigir Secure, Path=/ e ausência de Domain.
func NewCookieManager(name string, signer *security.CookieSigner) *CookieManager {
	return &CookieManager{Name: name, Signer: signer}
}

// Set grava o cookie assinado com expiração alinhada à sessão.
func (m *CookieManager) Set(w http.ResponseWriter, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.Name,
		Value:    m.Signer.Sign(token),
		Path:     "/",
		Expires:  expiresAt.UTC(),
		MaxAge:   maxAge,
		HttpOnly: true,                    // inacessível via JavaScript (mitiga XSS)
		Secure:   true,                    // somente HTTPS (localhost é tratado como seguro pelos navegadores)
		SameSite: http.SameSiteStrictMode, // não enviado em requisições cross-site (mitiga CSRF)
	})
}

// Clear remove o cookie no navegador.
func (m *CookieManager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.Name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// Read extrai e valida a assinatura do cookie, devolvendo o token em claro.
func (m *CookieManager) Read(r *http.Request) (string, bool) {
	c, err := r.Cookie(m.Name)
	if err != nil || c.Value == "" {
		return "", false
	}
	return m.Signer.Verify(c.Value)
}

// ReadRequest obtém o token de sessão do cabeçalho "Authorization: Bearer"
// (apps nativos) ou, na falta dele, do cookie (navegador). O valor Bearer é o
// mesmo token assinado do cookie: a assinatura HMAC é validada nos dois casos.
// bearer indica de onde o token veio.
func (m *CookieManager) ReadRequest(r *http.Request) (token string, bearer bool, ok bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, value, found := strings.Cut(h, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return "", true, false
		}
		t, valid := m.Signer.Verify(strings.TrimSpace(value))
		return t, true, valid
	}
	t, valid := m.Read(r)
	return t, false, valid
}

// SignToken devolve o valor assinado entregue aos apps no modo token.
func (m *CookieManager) SignToken(token string) string { return m.Signer.Sign(token) }
