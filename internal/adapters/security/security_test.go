package security

import (
	"strings"
	"testing"
)

func TestArgon2idHasher(t *testing.T) {
	h := NewArgon2idHasher(Argon2Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1})
	enc, err := h.Hash("senhaForte1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("formato inesperado: %s", enc)
	}
	if ok, rehash, err := h.Verify("senhaForte1", enc); !ok || rehash || err != nil {
		t.Fatalf("Verify correta = %v %v %v", ok, rehash, err)
	}
	if ok, _, _ := h.Verify("senhaErrada1", enc); ok {
		t.Fatal("senha errada foi aceita")
	}
	stronger := NewArgon2idHasher(Argon2Params{Memory: 32 * 1024, Iterations: 2, Parallelism: 1})
	if ok, rehash, _ := stronger.Verify("senhaForte1", enc); !ok || !rehash {
		t.Fatal("deveria sinalizar needsRehash quando os parâmetros mudam")
	}
	if _, _, err := h.Verify("x", "$bcrypt$abc"); err == nil {
		t.Fatal("hash inválido deveria gerar erro")
	}
}

func TestCookieSigner(t *testing.T) {
	old, _ := NewCookieSigner([][]byte{[]byte("chave-antiga-com-mais-de-32-caracteres")})
	s, _ := NewCookieSigner([][]byte{
		[]byte("chave-nova-com-mais-de-32-caracteres!!"),
		[]byte("chave-antiga-com-mais-de-32-caracteres"),
	})
	tm := SessionTokenManager{}
	tok, _ := tm.Generate()

	if got, ok := s.Verify(s.Sign(tok)); !ok || got != tok {
		t.Fatal("assinatura válida rejeitada")
	}
	if _, ok := s.Verify(old.Sign(tok)); !ok {
		t.Fatal("chave antiga (rotação) deveria ser aceita")
	}
	forged := tok + ".AAAA"
	if _, ok := s.Verify(forged); ok {
		t.Fatal("assinatura forjada aceita")
	}
	if len(tm.Hash(tok)) != 64 || tm.Hash(tok) == tok {
		t.Fatal("hash do token inválido")
	}
}

func TestHMACCodeManager(t *testing.T) {
	if _, err := NewHMACCodeManager([]byte("curto")); err == nil {
		t.Fatal("pepper curto deveria falhar")
	}
	c, _ := NewHMACCodeManager([]byte("pepper-com-mais-de-32-caracteres-aqui!!"))
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := c.GenerateNumeric(6)
		if err != nil || len(code) != 6 || strings.Trim(code, "0123456789") != "" {
			t.Fatalf("código inválido %q: %v", code, err)
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Fatalf("pouca aleatoriedade: %d distintos", len(seen))
	}
	h := c.Hash("user-1", "123456")
	if !c.Equal(h, c.Hash("user-1", "123456")) || c.Equal(h, c.Hash("user-2", "123456")) {
		t.Fatal("hash deve ser determinístico e vinculado ao usuário")
	}
	other, _ := NewHMACCodeManager([]byte("OUTRO-pepper-com-mais-de-32-caracteres!!"))
	if c.Equal(h, other.Hash("user-1", "123456")) {
		t.Fatal("pepper diferente deve gerar hash diferente")
	}
}
