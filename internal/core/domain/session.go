package domain

import "time"

// Session é uma sessão autenticada mantida no servidor.
//
// O token em claro NUNCA é persistido: guardamos apenas TokenHash
// (SHA-256). Assim, um vazamento do banco/Redis não permite sequestrar sessões.
type Session struct {
	TokenHash  string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	IP         string
}

// IsExpired informa se a sessão já expirou no instante now.
func (s *Session) IsExpired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}
