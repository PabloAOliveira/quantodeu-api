package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// TokenBytes é a entropia do token de sessão (256 bits).
const TokenBytes = 32

// SessionTokenManager implementa ports.TokenManager.
type SessionTokenManager struct{}

var _ ports.TokenManager = SessionTokenManager{}

// Generate cria um token aleatório de 256 bits em base64url.
func (SessionTokenManager) Generate() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash devolve o SHA-256 hex do token (o que é persistido no store).
func (SessionTokenManager) Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CookieSigner assina o valor do cookie com HMAC-SHA256.
//
// Valor do cookie: <token>.<assinatura base64url>
// Suporta rotação de chaves: assina com keys[0] e aceita qualquer uma na
// verificação. Isso rejeita cookies forjados ANTES de consultar o store,
// poupando o banco de requisições com tokens aleatórios.
type CookieSigner struct {
	keys [][]byte
}

// NewCookieSigner cria o assinador. É necessária ao menos uma chave.
func NewCookieSigner(keys [][]byte) (*CookieSigner, error) {
	if len(keys) == 0 {
		return nil, errors.New("cookie signer: nenhuma chave configurada")
	}
	return &CookieSigner{keys: keys}, nil
}

// Sign devolve o valor assinado para o cookie.
func (s *CookieSigner) Sign(token string) string {
	return token + "." + base64.RawURLEncoding.EncodeToString(s.mac(s.keys[0], token))
}

// Verify valida a assinatura e devolve o token original.
func (s *CookieSigner) Verify(value string) (string, bool) {
	token, sigB64, ok := strings.Cut(value, ".")
	if !ok || token == "" || len(value) > 256 {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", false
	}
	for _, k := range s.keys {
		if hmac.Equal(sig, s.mac(k, token)) {
			return token, true
		}
	}
	return "", false
}

func (s *CookieSigner) mac(key []byte, token string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("quantodeu-session:v1:"))
	m.Write([]byte(token))
	return m.Sum(nil)
}
