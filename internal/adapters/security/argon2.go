// Package security contém adaptadores criptográficos: hash de senha
// (Argon2id), geração de tokens de sessão e assinatura de cookies (HMAC).
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// Argon2Params são os parâmetros do Argon2id.
// Padrão: 64 MiB, t=3, p=2 — acima do mínimo OWASP (19 MiB, t=2, p=1).
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params devolve parâmetros seguros para produção.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}
}

// Argon2idHasher implementa ports.PasswordHasher.
// Formato PHC: $argon2id$v=19$m=65536,t=3,p=2$<salt b64>$<hash b64>
type Argon2idHasher struct {
	p Argon2Params
	// sem limita cálculos simultâneos: cada hash usa Memory KiB de RAM, então
	// uma rajada de logins sem limite poderia esgotar a memória (DoS).
	sem chan struct{}
}

var _ ports.PasswordHasher = (*Argon2idHasher)(nil)

var errInvalidHash = errors.New("hash argon2id em formato inválido")

// NewArgon2idHasher cria o hasher.
func NewArgon2idHasher(p Argon2Params) *Argon2idHasher {
	if p.SaltLength == 0 {
		p.SaltLength = 16
	}
	if p.KeyLength == 0 {
		p.KeyLength = 32
	}
	return &Argon2idHasher{p: p, sem: make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))}
}

func (h *Argon2idHasher) idKey(password, salt []byte, t, m uint32, p uint8, l uint32) []byte {
	h.sem <- struct{}{}
	defer func() { <-h.sem }()
	return argon2.IDKey(password, salt, t, m, p, l)
}

// Hash gera o hash codificado com salt aleatório.
func (h *Argon2idHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2: salt: %w", err)
	}
	key := h.idKey([]byte(password), salt, h.p.Iterations, h.p.Memory, h.p.Parallelism, h.p.KeyLength)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.p.Memory, h.p.Iterations, h.p.Parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify compara a senha com o hash em tempo constante.
func (h *Argon2idHasher) Verify(password, encoded string) (bool, bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errInvalidHash
	}
	var mem, iter uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &par); err != nil {
		return false, false, errInvalidHash
	}
	if mem > 1<<22 || iter > 100 || par == 0 { // protege contra hashes maliciosos gigantes
		return false, false, errInvalidHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, false, errInvalidHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false, false, errInvalidHash
	}

	got := h.idKey([]byte(password), salt, iter, mem, par, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	needsRehash := mem != h.p.Memory || iter != h.p.Iterations || par != h.p.Parallelism ||
		uint32(len(want)) != h.p.KeyLength || uint32(len(salt)) != h.p.SaltLength
	return true, needsRehash, nil
}
