package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// HMACCodeManager implementa ports.CodeManager.
//
// Códigos de 6 dígitos têm só 10^6 combinações: se o banco vazasse com o
// SHA-256 puro, bastaria testar todas. Por isso o hash é um HMAC com um
// pepper que fica apenas na configuração do servidor, e vinculado ao usuário.
type HMACCodeManager struct {
	key []byte
}

var _ ports.CodeManager = (*HMACCodeManager)(nil)

// NewHMACCodeManager deriva a chave do pepper informado (ex.: SESSION_SECRETS[0]).
func NewHMACCodeManager(pepper []byte) (*HMACCodeManager, error) {
	if len(pepper) < 32 {
		return nil, errors.New("code manager: pepper deve ter ao menos 32 bytes")
	}
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte("quantodeu:phone-verification:v1"))
	return &HMACCodeManager{key: m.Sum(nil)}, nil
}

// GenerateNumeric devolve `digits` dígitos uniformemente aleatórios.
func (c *HMACCodeManager) GenerateNumeric(digits int) (string, error) {
	if digits < 4 || digits > 12 {
		return "", errors.New("code manager: quantidade de dígitos inválida")
	}
	var b strings.Builder
	ten := big.NewInt(10)
	for i := 0; i < digits; i++ {
		n, err := rand.Int(rand.Reader, ten) // sem viés de módulo
		if err != nil {
			return "", err
		}
		b.WriteByte(byte('0' + n.Int64()))
	}
	return b.String(), nil
}

// Hash devolve HMAC-SHA256(key, userID|code) em hex.
func (c *HMACCodeManager) Hash(userID, code string) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(userID))
	m.Write([]byte{0})
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

// Equal compara em tempo constante.
func (c *HMACCodeManager) Equal(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}
