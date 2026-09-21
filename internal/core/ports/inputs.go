// Package ports define os contratos (interfaces) do hexágono.
//
//   - inputs.go  -> Portas PRIMÁRIAS (driving): o que a aplicação oferece.
//     Implementadas por internal/core/services e consumidas pelos
//     adaptadores de entrada (HTTP/Gin, webhook).
//   - outputs.go -> Portas SECUNDÁRIAS (driven): o que a aplicação precisa.
//     Definidas pelo núcleo e implementadas pelos adaptadores de saída
//     (PostgreSQL, Redis, Argon2id, clientes WhatsApp).
package ports

import (
	"context"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
)

// ---------------------------------------------------------------------------
// Autenticação / Usuário
// ---------------------------------------------------------------------------

// RegisterInput são os dados de cadastro.
type RegisterInput struct {
	Nome         string
	Email        string
	Telefone     string
	Senha        string
	SaldoInicial domain.Money
}

// LoginInput são as credenciais e metadados da requisição de login.
type LoginInput struct {
	Email     string
	Senha     string
	UserAgent string
	IP        string
}

// LoginOutput devolve o token EM CLARO (apenas para o cookie) e a sessão.
type LoginOutput struct {
	Token   string
	Session *domain.Session
	User    *domain.User
}

// AuthUseCase é a porta primária de autenticação.
type AuthUseCase interface {
	Register(ctx context.Context, in RegisterInput) (*domain.User, error)
	Login(ctx context.Context, in LoginInput) (*LoginOutput, error)
	Logout(ctx context.Context, token string) error
	// Authenticate valida o token de sessão e devolve o ID do usuário.
	Authenticate(ctx context.Context, token string) (*domain.Session, error)
	// ChangePassword troca a senha, encerra TODAS as sessões do usuário e
	// devolve uma sessão nova para o dispositivo atual.
	ChangePassword(ctx context.Context, in ChangePasswordInput) (*LoginOutput, error)
	// RevokeSessions encerra as sessões do usuário. Se keepToken for
	// informado, a sessão correspondente é preservada.
	RevokeSessions(ctx context.Context, userID, keepToken string) (int64, error)
}

// ChangePasswordInput são os dados de troca de senha.
type ChangePasswordInput struct {
	UserID     string
	SenhaAtual string
	NovaSenha  string
	UserAgent  string
	IP         string
}

// ProfileUseCase é a porta primária do perfil (/me).
type ProfileUseCase interface {
	GetProfile(ctx context.Context, userID string) (*domain.Perfil, error)
}

// ---------------------------------------------------------------------------
// Finanças
// ---------------------------------------------------------------------------

// CreateTransacaoInput são os dados de um lançamento manual.
type CreateTransacaoInput struct {
	Tipo      domain.TipoTransacao
	Valor     domain.Money
	Categoria string
	Descricao string
	Data      *time.Time // nil = hoje
}

// ListTransacoesInput define filtros e paginação.
type ListTransacoesInput struct {
	Ano       int
	Mes       int
	Categoria string
	Tipo      domain.TipoTransacao
	Page      int
	PageSize  int
}

// ListTransacoesOutput é uma página de lançamentos.
type ListTransacoesOutput struct {
	Items    []*domain.Transacao
	Total    int64
	Page     int
	PageSize int
	Periodo  domain.Periodo
}

// TransacaoUseCase é a porta primária de lançamentos.
type TransacaoUseCase interface {
	Create(ctx context.Context, userID string, in CreateTransacaoInput) (*domain.Transacao, error)
	List(ctx context.Context, userID string, in ListTransacoesInput) (*ListTransacoesOutput, error)
	Get(ctx context.Context, userID, id string) (*domain.Transacao, error)
	// Update substitui os campos editáveis (PUT). Parcelas aceitam edição de
	// valor/data/descrição/categoria, mas continuam vinculadas ao parcelamento.
	Update(ctx context.Context, userID, id string, in UpdateTransacaoInput) (*domain.Transacao, error)
	// Delete remove um lançamento. Parcelas devolvem domain.ErrParcelaGerenciada.
	Delete(ctx context.Context, userID, id string) error
}

// UpdateTransacaoInput são os dados de edição (todos obrigatórios, exceto Data).
type UpdateTransacaoInput struct {
	Tipo      domain.TipoTransacao
	Valor     domain.Money
	Categoria string
	Descricao string
	Data      *time.Time // nil = mantém a data atual
}

// ResumoUseCase é a porta primária do resumo mensal.
type ResumoUseCase interface {
	GetResumo(ctx context.Context, userID string, ano, mes int) (*domain.Resumo, error)
	// GetAgenda lista o que vence nos próximos `meses` — é o que o app usa
	// para agendar as notificações no aparelho.
	GetAgenda(ctx context.Context, userID string, meses int) (*domain.Agenda, error)
}

// CreateParcelamentoInput são os dados de uma despesa parcelada/recorrente.
type CreateParcelamentoInput struct {
	Tipo                domain.TipoParcelamento
	Descricao           string
	Categoria           string
	ValorTotal          domain.Money
	ValorParcela        domain.Money
	TotalParcelas       int
	DataPrimeiraParcela *time.Time // nil = hoje
	// ParcelasJaPagas são as N primeiras parcelas quitadas antes do cadastro,
	// fora do app: entram no progresso como pagas, mas NÃO viram lançamento
	// (o saldo informado no cadastro já está descontado delas).
	ParcelasJaPagas int
	// Financiamento (opcional) projeta as parcelas por juros em vez de
	// dividir ValorTotal. Exige Tipo = parcelado.
	Financiamento *domain.Financiamento
}

// SimulacaoFinanciamentoInput são os dados de uma simulação (não persiste nada).
type SimulacaoFinanciamentoInput struct {
	Financiamento       domain.Financiamento
	TotalParcelas       int
	DataPrimeiraParcela *time.Time // nil = hoje
}

// SimulacaoFinanciamentoOutput é a tabela de amortização projetada.
type SimulacaoFinanciamentoOutput struct {
	Financiamento domain.Financiamento
	TaxaMensal    float64 // fração (0.0069 = 0,69% a.m.)
	Parcelas      []domain.ParcelaProjetada
	TotalAPagar   domain.Money
	TotalJuros    domain.Money

	// PrazoContratado é o prazo informado. Com amortização extraordinária o
	// contrato quita antes, então len(Parcelas) vem menor — a diferença entre
	// os dois é quanto a quitação foi antecipada.
	PrazoContratado int

	// SemAporte traz o mesmo contrato sem a amortização extraordinária, para
	// o cliente mostrar quanto o aporte economiza. Nil quando não há aporte.
	SemAporte *ComparativoFinanciamento
}

// ComparativoFinanciamento são os totais de um cenário alternativo.
type ComparativoFinanciamento struct {
	TotalParcelas int
	TotalAPagar   domain.Money
	TotalJuros    domain.Money
}

// ParcelamentoUseCase é a porta primária de parcelamentos.
type ParcelamentoUseCase interface {
	Create(ctx context.Context, userID string, in CreateParcelamentoInput) (*domain.Parcelamento, []*domain.Transacao, error)
	List(ctx context.Context, userID string) ([]*domain.Parcelamento, error)
	Get(ctx context.Context, userID, id string) (*domain.Parcelamento, []*domain.Transacao, error)
	// Update recalcula o parcelamento e REGERA todas as parcelas atomicamente
	// (edições manuais feitas em parcelas individuais são descartadas).
	Update(ctx context.Context, userID, id string, in CreateParcelamentoInput) (*domain.Parcelamento, []*domain.Transacao, error)
	// Delete remove o parcelamento. Com manterPagas=true, as parcelas com data
	// até hoje são preservadas como lançamentos avulsos (histórico real).
	Delete(ctx context.Context, userID, id string, manterPagas bool) (removidas int64, err error)
	// Simular projeta as parcelas de um financiamento sem gravar nada.
	Simular(ctx context.Context, in SimulacaoFinanciamentoInput) (*SimulacaoFinanciamentoOutput, error)
}

// ---------------------------------------------------------------------------
// Cartão de crédito
// ---------------------------------------------------------------------------

// CreateCartaoInput são os dados de cadastro/edição de um cartão.
type CreateCartaoInput struct {
	Nome          string
	Banco         string
	DiaFechamento int
	DiaVencimento int
	Limite        domain.Money
	Ativo         *bool // nil = mantém (só faz sentido no update)
}

// CreateCompraInput é uma compra no cartão (1 ou mais parcelas).
type CreateCompraInput struct {
	Valor         domain.Money // valor TOTAL da compra
	Categoria     string
	Descricao     string
	Data          *time.Time // nil = hoje
	TotalParcelas int        // 0 ou 1 = à vista
}

// PagarFaturaInput registra o pagamento de uma fatura.
type PagarFaturaInput struct {
	// Data em que o dinheiro saiu da conta. nil = hoje. É ESTA data que define
	// em que mês o gasto aparece — não o vencimento da fatura.
	Data *time.Time
	// Valor pago. Zero = o total da fatura.
	Valor domain.Money
}

// CartaoUseCase é a porta primária de cartões e faturas.
type CartaoUseCase interface {
	Create(ctx context.Context, userID string, in CreateCartaoInput) (*domain.Cartao, error)
	List(ctx context.Context, userID string) ([]*domain.ResumoCartao, error)
	Get(ctx context.Context, userID, id string) (*domain.ResumoCartao, error)
	Update(ctx context.Context, userID, id string, in CreateCartaoInput) (*domain.Cartao, error)
	// Delete remove o cartão. Com histórico, devolve domain.ErrCartaoComHistorico
	// — o certo é arquivar (Update com Ativo=false).
	Delete(ctx context.Context, userID, id string) error

	// Faturas lista as faturas com movimento, da mais recente para a mais antiga.
	Faturas(ctx context.Context, userID, cartaoID string) ([]*domain.Fatura, error)
	// Fatura devolve uma competência ("2026-10") com as compras dela.
	Fatura(ctx context.Context, userID, cartaoID, competencia string) (*domain.Fatura, error)
	// PagarFatura cria a saída no saldo e marca a fatura como paga.
	PagarFatura(ctx context.Context, userID, cartaoID, competencia string, in PagarFaturaInput) (*domain.Fatura, error)
	// DesfazerPagamento remove a saída e volta a fatura para fechada.
	DesfazerPagamento(ctx context.Context, userID, cartaoID, competencia string) error

	// RegistrarCompra grava a compra (uma linha por parcela).
	RegistrarCompra(ctx context.Context, userID, cartaoID string, in CreateCompraInput) ([]*domain.CompraCartao, error)
	// ExcluirCompra remove a compra inteira pelo grupo (todas as parcelas).
	ExcluirCompra(ctx context.Context, userID, grupoID string) (int64, error)
}

// ---------------------------------------------------------------------------
// Verificação de telefone
// ---------------------------------------------------------------------------

// StartVerificationOutput orienta o cliente sobre como concluir a verificação.
type StartVerificationOutput struct {
	Metodo    domain.MetodoVerificacao
	ExpiresAt time.Time
	// CodigoParaEnviar só é preenchido no método "mensagem": o usuário deve
	// enviar "verificar <código>" do próprio WhatsApp.
	CodigoParaEnviar string
	Instrucao        string
}

// PhoneVerificationUseCase é a porta primária da verificação de telefone.
type PhoneVerificationUseCase interface {
	Start(ctx context.Context, userID string, metodo domain.MetodoVerificacao) (*StartVerificationOutput, error)
	// Confirm valida o código digitado no app (método "codigo").
	Confirm(ctx context.Context, userID, codigo string) error
}

// StartEmailVerificationOutput orienta o próximo passo da verificação de e-mail.
type StartEmailVerificationOutput struct {
	Email     string
	ExpiresAt time.Time
}

// EmailVerificationUseCase é a porta primária da confirmação de e-mail.
type EmailVerificationUseCase interface {
	// Start gera um código de 6 dígitos e o envia para o e-mail do usuário.
	Start(ctx context.Context, userID string) (*StartEmailVerificationOutput, error)
	// Confirm valida o código digitado no app.
	Confirm(ctx context.Context, userID, codigo string) error
	// IsVerified informa se o e-mail do usuário já foi confirmado.
	IsVerified(ctx context.Context, userID string) (bool, error)
}

// ---------------------------------------------------------------------------
// WhatsApp
// ---------------------------------------------------------------------------

// WebhookUseCase é a porta primária de processamento de mensagens recebidas.
type WebhookUseCase interface {
	ProcessMessage(ctx context.Context, msg domain.MensagemRecebida) (*domain.ResultadoWebhook, error)
}

// MessageParser interpreta texto livre ("Gastei 150,00 mercado").
// É uma porta para permitir trocar o parser Regex por outro (ex.: LLM).
type MessageParser interface {
	Parse(texto string) (*domain.LancamentoInterpretado, error)
}
