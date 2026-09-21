package ports

import (
	"context"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

// ---------------------------------------------------------------------------
// Persistência — ISOLAMENTO MULTI-TENANT
//
// Todo método que lê ou altera dados financeiros recebe userID como parâmetro
// OBRIGATÓRIO. As implementações devem SEMPRE escopar pelo usuário (no
// PostgreSQL: `WHERE user_id = $1` + Row-Level Security com app.user_id).
// Não existem métodos "FindByID(id)" sem userID para dados de tenant.
// ---------------------------------------------------------------------------

// UserRepository persiste usuários.
type UserRepository interface {
	// Create insere o usuário e preenche ID/CreatedAt/UpdatedAt.
	// Deve devolver domain.ErrEmailAlreadyExists / domain.ErrPhoneAlreadyExists.
	Create(ctx context.Context, u *domain.User) error
	FindByID(ctx context.Context, userID string) (*domain.User, error)
	// FindByEmail e FindByPhones são as ÚNICAS buscas sem tenant conhecido
	// (login e webhook). No PostgreSQL passam por funções SECURITY DEFINER.
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	FindByPhones(ctx context.Context, phones []string) (*domain.User, error)
	UpdatePasswordHash(ctx context.Context, userID, hash string) error
	MarkPhoneVerified(ctx context.Context, userID string, at time.Time) error
	MarkEmailVerified(ctx context.Context, userID string, at time.Time) error
}

// TransacaoFilter define filtros de listagem (sempre escopados ao usuário).
type TransacaoFilter struct {
	Inicio    time.Time // inclusivo
	Fim       time.Time // exclusivo
	Categoria string
	Tipo      domain.TipoTransacao
	Limit     int
	Offset    int
}

// TransacaoRepository persiste lançamentos.
type TransacaoRepository interface {
	// Create insere e preenche ID/CreatedAt. Se ExternalID já existir para o
	// usuário e origem, devolve domain.ErrDuplicateMessage.
	Create(ctx context.Context, userID string, t *domain.Transacao) error
	FindByID(ctx context.Context, userID, id string) (*domain.Transacao, error)
	// Update grava tipo, valor, categoria, descrição e data.
	Update(ctx context.Context, userID string, t *domain.Transacao) error
	Delete(ctx context.Context, userID, id string) error
	List(ctx context.Context, userID string, f TransacaoFilter) ([]*domain.Transacao, int64, error)
	// Totais agrega entradas/saídas no intervalo [inicio, fim).
	Totais(ctx context.Context, userID string, inicio, fim time.Time) (domain.TotaisPeriodo, error)
	// SaldoAte devolve a soma assinada de todos os lançamentos com data < ate.
	SaldoAte(ctx context.Context, userID string, ate time.Time) (domain.Money, error)
	TotaisPorCategoria(ctx context.Context, userID string, inicio, fim time.Time) ([]domain.TotalCategoria, error)
	ListParcelas(ctx context.Context, userID string, inicio, fim time.Time) ([]*domain.Transacao, error)
}

// ParcelamentoRepository persiste parcelamentos.
type ParcelamentoRepository interface {
	// CreateWithParcelas grava o parcelamento e todas as parcelas de forma
	// ATÔMICA (uma única transação de banco).
	CreateWithParcelas(ctx context.Context, userID string, p *domain.Parcelamento, parcelas []*domain.Transacao) error
	FindByID(ctx context.Context, userID, id string) (*domain.Parcelamento, error)
	ListParcelasDoParcelamento(ctx context.Context, userID, id string) ([]*domain.Transacao, error)
	// ReplaceWithParcelas atualiza o parcelamento e substitui TODAS as parcelas
	// atomicamente.
	ReplaceWithParcelas(ctx context.Context, userID string, p *domain.Parcelamento, parcelas []*domain.Transacao) error
	// Delete remove o parcelamento e suas parcelas. Se preservarAte não for
	// nil, parcelas com data <= preservarAte são desvinculadas (viram
	// lançamentos avulsos) em vez de removidas. Devolve quantas parcelas foram
	// removidas.
	Delete(ctx context.Context, userID, id string, preservarAte *time.Time) (int64, error)
	// List devolve os parcelamentos com Progresso calculado em relação a hoje.
	List(ctx context.Context, userID string, hoje time.Time) ([]*domain.Parcelamento, error)
}

// SessionStore persiste sessões (PostgreSQL ou Redis).
type SessionStore interface {
	Create(ctx context.Context, s *domain.Session) error
	// FindByTokenHash devolve domain.ErrSessionNotFound se inexistente/expirada.
	FindByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error)
	Touch(ctx context.Context, tokenHash string, lastSeen time.Time) error
	Delete(ctx context.Context, tokenHash string) error
	// DeleteAllForUser remove todas as sessões do usuário, exceto a de
	// exceptTokenHash (se não vazio). Devolve quantas foram removidas.
	DeleteAllForUser(ctx context.Context, userID, exceptTokenHash string) (int64, error)
	// DeleteExpired remove sessões expiradas (no-op em stores com TTL nativo).
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// LoginAttemptStore guarda falhas de login por chave (e-mail normalizado).
type LoginAttemptStore interface {
	Get(ctx context.Context, key string) (domain.LoginAttemptState, error)
	// RegisterFailure incrementa o contador (zerando-o se a primeira falha for
	// mais antiga que window) e devolve o total atual.
	RegisterFailure(ctx context.Context, key string, now time.Time, window time.Duration) (int, error)
	Lock(ctx context.Context, key string, until time.Time) error
	Reset(ctx context.Context, key string) error
}

// PhoneVerificationStore guarda o desafio pendente (no máximo um por usuário).
type PhoneVerificationStore interface {
	Save(ctx context.Context, v *domain.PhoneVerification) error
	// Get devolve domain.ErrNotFound se não houver desafio.
	Get(ctx context.Context, userID string) (*domain.PhoneVerification, error)
	IncrementAttempts(ctx context.Context, userID string) (int, error)
	Delete(ctx context.Context, userID string) error
}

// EmailVerificationStore guarda o desafio de e-mail pendente (um por usuário).
type EmailVerificationStore interface {
	Save(ctx context.Context, v *domain.EmailVerification) error
	// Get devolve domain.ErrNotFound se não houver desafio.
	Get(ctx context.Context, userID string) (*domain.EmailVerification, error)
	IncrementAttempts(ctx context.Context, userID string) (int, error)
	Delete(ctx context.Context, userID string) error
}

// RateLimiter limita requisições por chave (implementações em memória e Redis).
type RateLimiter interface {
	// Allow consome uma permissão. Quando negado, retryAfter indica a espera.
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// ---------------------------------------------------------------------------
// Segurança e utilitários
// ---------------------------------------------------------------------------

// PasswordHasher abstrai o algoritmo de hash (Argon2id/bcrypt).
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Verify compara em tempo constante. needsRehash=true indica que os
	// parâmetros mudaram e o hash deve ser atualizado.
	Verify(password, encodedHash string) (ok bool, needsRehash bool, err error)
}

// TokenManager gera tokens aleatórios e deriva o hash persistido.
type TokenManager interface {
	Generate() (token string, err error)
	Hash(token string) string
}

// CodeManager gera e protege códigos numéricos curtos (verificação).
type CodeManager interface {
	// GenerateNumeric devolve um código com `digits` dígitos (crypto/rand).
	GenerateNumeric(digits int) (string, error)
	// Hash devolve um HMAC do código vinculado ao usuário (com pepper do servidor).
	Hash(userID, code string) string
	// Equal compara dois hashes em tempo constante.
	Equal(a, b string) bool
}

// Clock abstrai o tempo para permitir testes determinísticos.
type Clock interface {
	Now() time.Time
}

// CartaoRepository persiste cartões, compras e pagamentos de fatura.
type CartaoRepository interface {
	Create(ctx context.Context, userID string, c *domain.Cartao) error
	FindByID(ctx context.Context, userID, id string) (*domain.Cartao, error)
	List(ctx context.Context, userID string) ([]*domain.Cartao, error)
	Update(ctx context.Context, userID string, c *domain.Cartao) error
	Delete(ctx context.Context, userID, id string) error

	// CreateCompras grava todas as parcelas de uma compra atomicamente.
	CreateCompras(ctx context.Context, userID string, compras []*domain.CompraCartao) error
	// ComprasDaFatura devolve as parcelas de um vencimento.
	ComprasDaFatura(ctx context.Context, userID, cartaoID string, vencimento time.Time) ([]*domain.CompraCartao, error)
	// ResumoDasFaturas devolve, numa consulta só, o total e o pagamento de cada
	// fatura com movimento — da mais recente para a mais antiga.
	ResumoDasFaturas(ctx context.Context, userID, cartaoID string) ([]domain.FaturaResumo, error)
	// TotalNaoPago soma as compras de faturas sem pagamento registrado — é o
	// que está comprometido do limite.
	TotalNaoPago(ctx context.Context, userID, cartaoID string) (domain.Money, error)
	// GrupoTemFaturaPaga informa se alguma parcela da compra caiu numa fatura
	// já paga — mexer nela mudaria um total que já virou dinheiro.
	GrupoTemFaturaPaga(ctx context.Context, userID, grupoID string) (bool, error)
	// DeleteCompraGrupo remove todas as parcelas de uma compra.
	DeleteCompraGrupo(ctx context.Context, userID, grupoID string) (int64, error)

	// PagamentoDaFatura devolve nil quando a fatura não foi paga.
	PagamentoDaFatura(ctx context.Context, userID, cartaoID string, vencimento time.Time) (*domain.PagamentoFatura, error)
	// RegistrarPagamento grava a transação de saída e o pagamento atomicamente.
	RegistrarPagamento(ctx context.Context, userID string, t *domain.Transacao, pag *domain.PagamentoFatura) error
	// RemoverPagamento apaga o pagamento e a transação de saída.
	RemoverPagamento(ctx context.Context, userID, cartaoID string, vencimento time.Time) error
}

// WhatsAppSender envia mensagens ao usuário (confirmações, códigos).
type WhatsAppSender interface {
	SendText(ctx context.Context, telefone, texto string) error
}

// EmailSender envia e-mails transacionais (SMTP, log em DEV...).
type EmailSender interface {
	Send(ctx context.Context, msg domain.EmailMessage) error
}

// Metrics registra métricas de NEGÓCIO sem acoplar o núcleo ao Prometheus.
type Metrics interface {
	TransacaoRegistrada(origem, tipo string)
	WebhookProcessado(provider, status string)
	LoginTentativa(resultado string) // sucesso | credenciais_invalidas | bloqueado
	VerificacaoTelefone(evento string)
	VerificacaoEmail(evento string)
	SessoesRevogadas(motivo string, total int64)
}
