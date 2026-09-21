// Package dto define os contratos JSON da API. As tags JSON/binding/doc ficam
// AQUI, no adaptador — nunca nas entidades de domínio. A especificação OpenAPI
// é gerada a partir destas structs (ver internal/adapters/http/openapi).
package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// ---------------------------------------------------------------------------
// Tipos auxiliares
// ---------------------------------------------------------------------------

// Money aceita número JSON (150.5) ou string ("150,50", "R$ 1.234,56").
type Money struct {
	Value domain.Money
	Set   bool
}

// UnmarshalJSON implementa json.Unmarshaler.
func (m *Money) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	raw := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
	}
	v, err := domain.ParseMoney(raw)
	if err != nil {
		return err
	}
	m.Value, m.Set = v, true
	return nil
}

// OpenAPISchema documenta o formato flexível.
func (Money) OpenAPISchema() map[string]any {
	return map[string]any{
		"description": "Valor em reais: número (150.5) ou texto (\"150,50\", \"R$ 1.234,56\"). Internamente armazenado em centavos.",
		"oneOf": []any{
			map[string]any{"type": "number", "examples": []any{150.5}},
			map[string]any{"type": "string", "examples": []any{"150,50"}},
		},
	}
}

// Date aceita "YYYY-MM-DD".
type Date struct {
	time.Time
}

// UnmarshalJSON implementa json.Unmarshaler.
func (d *Date) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("data deve ser string no formato YYYY-MM-DD")
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return errors.New("data deve estar no formato YYYY-MM-DD")
	}
	d.Time = t
	return nil
}

// OpenAPISchema documenta o formato de data.
func (Date) OpenAPISchema() map[string]any {
	return map[string]any{"type": "string", "format": "date", "examples": []any{"2026-09-15"}}
}

// Ptr devolve nil quando a data não foi informada.
func (d *Date) Ptr() *time.Time {
	if d == nil || d.IsZero() {
		return nil
	}
	t := d.Time
	return &t
}

// MoneyResponse representa dinheiro na saída: decimal em string + centavos.
type MoneyResponse struct {
	Valor     string `json:"valor" doc:"Decimal com ponto" example:"150.50"`
	Centavos  int64  `json:"centavos" example:"15050"`
	Formatado string `json:"formatado" example:"R$ 150,50"`
}

// NewMoney converte domain.Money.
func NewMoney(m domain.Money) MoneyResponse {
	return MoneyResponse{Valor: m.String(), Centavos: int64(m), Formatado: m.BRL()}
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// RegisterRequest é o corpo de POST /auth/register.
type RegisterRequest struct {
	Nome         string `json:"nome" binding:"required,max=120" example:"Maria Silva"`
	Email        string `json:"email" binding:"required,max=254" example:"maria@exemplo.com"`
	Telefone     string `json:"telefone" binding:"required,max=30" doc:"Celular com DDD (máscara opcional)" example:"(11) 98765-4321"`
	Senha        string `json:"senha" binding:"required,max=128" doc:"8 a 128 caracteres, com letra e número" example:"senhaForte123"`
	SaldoInicial Money  `json:"saldo_inicial" binding:"omitempty"`
}

// LoginRequest é o corpo de POST /auth/login.
type LoginRequest struct {
	Email string `json:"email" binding:"required,max=254" example:"maria@exemplo.com"`
	Senha string `json:"senha" binding:"required,max=128" example:"senhaForte123"`
	Modo  string `json:"modo,omitempty" binding:"omitempty,oneof=cookie token" doc:"cookie (padrão, navegador) grava o cookie HttpOnly; token (apps nativos) devolve o token no corpo para usar em Authorization: Bearer" example:"cookie"`
}

// UserResponse é a representação pública do usuário.
type UserResponse struct {
	ID                   string        `json:"id" example:"3952de08-d227-4b5d-bb94-5475899d5115"`
	Nome                 string        `json:"nome" example:"Maria Silva"`
	Email                string        `json:"email" example:"maria@exemplo.com"`
	Telefone             string        `json:"telefone" doc:"Normalizado: 55 + DDD + número" example:"5511987654321"`
	TelefoneVerificado   bool          `json:"telefone_verificado" doc:"Somente telefones verificados podem lançar pelo WhatsApp"`
	TelefoneVerificadoEm *time.Time    `json:"telefone_verificado_em,omitempty"`
	EmailVerificado      bool          `json:"email_verificado" doc:"Com EMAIL_VERIFICATION_REQUIRED=true, transações, resumo e parcelamentos exigem e-mail confirmado (403 email_nao_verificado)"`
	EmailVerificadoEm    *time.Time    `json:"email_verificado_em,omitempty"`
	SaldoInicial         MoneyResponse `json:"saldo_inicial"`
	CriadoEm             time.Time     `json:"criado_em"`
}

// NewUserResponse converte domain.User (nunca expõe PasswordHash).
func NewUserResponse(u *domain.User) UserResponse {
	return UserResponse{
		ID: u.ID, Nome: u.Nome, Email: u.Email, Telefone: u.Telefone,
		TelefoneVerificado: u.TelefoneVerificado(), TelefoneVerificadoEm: u.TelefoneVerificadoEm,
		EmailVerificado: u.EmailVerificado(), EmailVerificadoEm: u.EmailVerificadoEm,
		SaldoInicial: NewMoney(u.SaldoInicial), CriadoEm: u.CreatedAt,
	}
}

// LoginResponse é devolvido após autenticar.
type LoginResponse struct {
	User      UserResponse `json:"user"`
	ExpiresAt time.Time    `json:"expira_em"`
	Token     string       `json:"token,omitempty" doc:"Somente no modo token: envie em Authorization: Bearer <token>. Guarde em armazenamento seguro (Keychain/Keystore)."`
	TokenType string       `json:"token_type,omitempty" example:"Bearer"`
}

// MeResponse é devolvido por GET /me.
type MeResponse struct {
	UserResponse
	SaldoAtual MoneyResponse `json:"saldo_atual"`
	Mes        MesResponse   `json:"mes"`
}

// MesResponse traz os totais do mês corrente.
type MesResponse struct {
	Referencia  string        `json:"referencia" example:"2026-09"`
	Entradas    MoneyResponse `json:"total_entradas"`
	Saidas      MoneyResponse `json:"total_saidas"`
	Rendimentos MoneyResponse `json:"rendimentos"`
}

// NewMeResponse converte domain.Perfil.
func NewMeResponse(p *domain.Perfil) MeResponse {
	return MeResponse{
		UserResponse: NewUserResponse(p.User),
		SaldoAtual:   NewMoney(p.SaldoAtual),
		Mes: MesResponse{
			Referencia:  p.Periodo.Inicio.Format("2006-01"),
			Entradas:    NewMoney(p.EntradasMes),
			Saidas:      NewMoney(p.SaidasMes),
			Rendimentos: NewMoney(p.Rendimentos),
		},
	}
}

// ChangePasswordRequest é o corpo de PUT /me/senha.
type ChangePasswordRequest struct {
	SenhaAtual string `json:"senha_atual" binding:"required,max=128" example:"senhaForte123"`
	NovaSenha  string `json:"nova_senha" binding:"required,max=128" example:"outraSenha456"`
}

// RevokeSessionsQuery são os parâmetros de DELETE /me/sessoes.
type RevokeSessionsQuery struct {
	ManterAtual bool `form:"manter_atual" doc:"true = encerra apenas as OUTRAS sessões (padrão: encerra todas, inclusive a atual)"`
}

// RevokeSessionsResponse informa quantas sessões foram encerradas.
type RevokeSessionsResponse struct {
	Encerradas   int64 `json:"sessoes_encerradas" example:"3"`
	ManteveAtual bool  `json:"manteve_atual"`
}

// ---------------------------------------------------------------------------
// Verificação de telefone
// ---------------------------------------------------------------------------

// StartVerificationRequest é o corpo de POST /me/telefone/verificacao.
type StartVerificationRequest struct {
	Metodo string `json:"metodo" binding:"omitempty,oneof=codigo mensagem" doc:"codigo = a API envia o código ao WhatsApp e você o digita no app; mensagem = o app mostra o código e você o envia do seu WhatsApp (\"verificar 123456\"). Vazio escolhe automaticamente." example:"mensagem"`
}

// StartVerificationResponse orienta o próximo passo.
type StartVerificationResponse struct {
	Metodo           string    `json:"metodo" example:"mensagem"`
	ExpiraEm         time.Time `json:"expira_em"`
	CodigoParaEnviar string    `json:"codigo_para_enviar,omitempty" doc:"Somente no método 'mensagem'" example:"482913"`
	Instrucao        string    `json:"instrucao" example:"Envie a mensagem \"verificar 482913\" do WhatsApp 5511987654321..."`
}

// ConfirmVerificationRequest é o corpo de POST /me/telefone/verificacao/confirmar.
type ConfirmVerificationRequest struct {
	Codigo string `json:"codigo" binding:"required,len=6,numeric" example:"482913"`
}

// StartEmailVerificationResponse confirma o envio do código por e-mail.
type StartEmailVerificationResponse struct {
	Email     string    `json:"email" example:"maria@exemplo.com"`
	ExpiraEm  time.Time `json:"expira_em"`
	Instrucao string    `json:"instrucao" example:"Enviamos um código de 6 dígitos para maria@exemplo.com. Confirme em POST /api/v1/me/email/verificacao/confirmar."`
}

// MessageResponse é uma resposta simples.
type MessageResponse struct {
	Mensagem string `json:"mensagem" example:"telefone verificado"`
}

// ---------------------------------------------------------------------------
// Transações
// ---------------------------------------------------------------------------

// CreateTransacaoRequest é o corpo de POST /transacoes.
type CreateTransacaoRequest struct {
	Tipo      string `json:"tipo" binding:"required,oneof=entrada saida" example:"saida"`
	Valor     Money  `json:"valor"`
	Categoria string `json:"categoria" binding:"max=60" doc:"Normalizada para minúsculas; vazia vira 'outros'" example:"mercado"`
	Descricao string `json:"descricao" binding:"max=255" example:"compras do mês"`
	Data      *Date  `json:"data" doc:"Padrão: hoje (fuso APP_TIMEZONE)"`
}

// UpdateTransacaoRequest é o corpo de PUT /transacoes/{id} (substituição).
type UpdateTransacaoRequest = CreateTransacaoRequest

// ListTransacoesQuery são os filtros de GET /transacoes.
type ListTransacoesQuery struct {
	Mes       int    `form:"mes" binding:"omitempty,min=1,max=12" doc:"Padrão: mês corrente" example:"9"`
	Ano       int    `form:"ano" binding:"omitempty,min=2000,max=2100" example:"2026"`
	Categoria string `form:"categoria" binding:"max=60" example:"mercado"`
	Tipo      string `form:"tipo" binding:"omitempty,oneof=entrada saida"`
	Page      int    `form:"page" binding:"omitempty,min=1,max=100000" example:"1"`
	PageSize  int    `form:"page_size" binding:"omitempty,min=1,max=100" example:"20"`
}

// TransacaoResponse representa um lançamento.
type TransacaoResponse struct {
	ID             string        `json:"id"`
	Tipo           string        `json:"tipo" example:"saida"`
	Valor          MoneyResponse `json:"valor"`
	Categoria      string        `json:"categoria" example:"mercado"`
	Descricao      string        `json:"descricao" example:"compras do mês"`
	Data           string        `json:"data" example:"2026-09-15"`
	Origem         string        `json:"origem" doc:"manual | whatsapp | parcelamento" example:"manual"`
	ParcelamentoID *string       `json:"parcelamento_id,omitempty"`
	NumeroParcela  *int          `json:"numero_parcela,omitempty"`
	CriadoEm       time.Time     `json:"criado_em"`
}

// NewTransacaoResponse converte domain.Transacao.
func NewTransacaoResponse(t *domain.Transacao) TransacaoResponse {
	return TransacaoResponse{
		ID: t.ID, Tipo: string(t.Tipo), Valor: NewMoney(t.Valor), Categoria: t.Categoria,
		Descricao: t.Descricao, Data: t.Data.Format("2006-01-02"), Origem: string(t.Origem),
		ParcelamentoID: t.ParcelamentoID, NumeroParcela: t.NumeroParcela, CriadoEm: t.CreatedAt,
	}
}

// NewTransacoesResponse converte uma lista.
func NewTransacoesResponse(items []*domain.Transacao) []TransacaoResponse {
	out := make([]TransacaoResponse, 0, len(items))
	for _, t := range items {
		out = append(out, NewTransacaoResponse(t))
	}
	return out
}

// Pagination descreve a paginação.
type Pagination struct {
	Page       int   `json:"page" example:"1"`
	PageSize   int   `json:"page_size" example:"20"`
	Total      int64 `json:"total" example:"42"`
	TotalPages int64 `json:"total_pages" example:"3"`
}

// ListTransacoesResponse é a página de GET /transacoes.
type ListTransacoesResponse struct {
	Referencia string              `json:"referencia" example:"2026-09"`
	Items      []TransacaoResponse `json:"items"`
	Pagination Pagination          `json:"pagination"`
}

// ---------------------------------------------------------------------------
// Resumo
// ---------------------------------------------------------------------------

// ResumoQuery são os parâmetros de GET /resumo.
type ResumoQuery struct {
	Mes int `form:"mes" binding:"omitempty,min=1,max=12" doc:"Padrão: mês corrente" example:"9"`
	Ano int `form:"ano" binding:"omitempty,min=2000,max=2100" example:"2026"`
}

// AgendaQuery são os parâmetros de GET /agenda.
type AgendaQuery struct {
	Meses int `form:"meses" binding:"omitempty,min=1,max=12" doc:"Horizonte em meses (padrão 6, máximo 12)" example:"6"`
}

// CompromissoResponse é uma conta com data marcada.
type CompromissoResponse struct {
	Tipo      string        `json:"tipo" doc:"parcela | fatura" example:"parcela"`
	Data      string        `json:"data" example:"2026-10-09"`
	Valor     MoneyResponse `json:"valor"`
	Titulo    string        `json:"titulo" doc:"O que aparece no aviso" example:"Academia"`
	Categoria string        `json:"categoria" example:"saude"`

	ParcelamentoID string `json:"parcelamento_id,omitempty"`
	NumeroParcela  int    `json:"numero_parcela,omitempty"`

	CartaoID    string `json:"cartao_id,omitempty"`
	Competencia string `json:"competencia,omitempty" example:"2026-10"`
}

// AgendaResponse é o que vence daqui para a frente.
type AgendaResponse struct {
	Inicio string                `json:"inicio" example:"2026-09-21"`
	Fim    string                `json:"fim" example:"2027-03-21"`
	Itens  []CompromissoResponse `json:"itens"`
}

// NewAgendaResponse converte a agenda.
func NewAgendaResponse(a *domain.Agenda) AgendaResponse {
	out := AgendaResponse{
		Inicio: a.Inicio.Format("2006-01-02"),
		Fim:    a.Fim.Format("2006-01-02"),
		Itens:  make([]CompromissoResponse, 0, len(a.Itens)),
	}
	for _, i := range a.Itens {
		out.Itens = append(out.Itens, CompromissoResponse{
			Tipo: string(i.Tipo), Data: i.Data.Format("2006-01-02"),
			Valor: NewMoney(i.Valor), Titulo: i.Titulo, Categoria: i.Categoria,
			ParcelamentoID: i.ParcelamentoID, NumeroParcela: i.NumeroParcela,
			CartaoID: i.CartaoID, Competencia: i.Competencia,
		})
	}
	return out
}

// CategoriaResponse agrega por categoria.
type CategoriaResponse struct {
	Categoria string        `json:"categoria" example:"mercado"`
	Tipo      string        `json:"tipo" example:"saida"`
	Total     MoneyResponse `json:"total"`
	Qtd       int64         `json:"quantidade" example:"4"`
}

// ResumoResponse é devolvido por GET /resumo.
type ResumoResponse struct {
	Referencia       string              `json:"referencia" example:"2026-09"`
	Receitas         MoneyResponse       `json:"receitas"`
	Despesas         MoneyResponse       `json:"despesas"`
	ResultadoMes     MoneyResponse       `json:"resultado_mes"`
	SaldoGeral       MoneyResponse       `json:"saldo_geral" doc:"Saldo projetado no fim do mês consultado"`
	SaldoAtual       MoneyResponse       `json:"saldo_atual" doc:"Saldo considerando lançamentos até hoje"`
	CustosParcelados MoneyResponse       `json:"custos_fixos_parcelados"`
	Parcelas         []TransacaoResponse `json:"parcelas_do_mes"`
	Cartoes          []CartaoResponse    `json:"cartoes,omitempty" doc:"Por cartão: a fatura a pagar e a que está acumulando. Compras no cartão NÃO entram em 'despesas' — o que entra é o pagamento da fatura, na data em que o dinheiro saiu."`
	PorCategoria     []CategoriaResponse `json:"por_categoria"`
}

// NewResumoResponse converte domain.Resumo.
func NewResumoResponse(r *domain.Resumo) ResumoResponse {
	cats := make([]CategoriaResponse, 0, len(r.PorCategoria))
	for _, c := range r.PorCategoria {
		cats = append(cats, CategoriaResponse{Categoria: c.Categoria, Tipo: string(c.Tipo), Total: NewMoney(c.Total), Qtd: c.Qtd})
	}
	cartoes := make([]CartaoResponse, 0, len(r.Cartoes))
	for _, c := range r.Cartoes {
		cartoes = append(cartoes, NewCartaoResponse(c))
	}
	return ResumoResponse{
		Referencia:       r.Periodo.Inicio.Format("2006-01"),
		Receitas:         NewMoney(r.Receitas),
		Despesas:         NewMoney(r.Despesas),
		ResultadoMes:     NewMoney(r.ResultadoMes),
		SaldoGeral:       NewMoney(r.SaldoGeral),
		SaldoAtual:       NewMoney(r.SaldoAtual),
		CustosParcelados: NewMoney(r.CustosParcelados),
		Parcelas:         NewTransacoesResponse(r.Parcelas),
		PorCategoria:     cats,
		Cartoes:          cartoes,
	}
}

// ---------------------------------------------------------------------------
// Parcelamentos
// ---------------------------------------------------------------------------

// CreateParcelamentoRequest é o corpo de POST /parcelamentos e PUT /parcelamentos/{id}.
type CreateParcelamentoRequest struct {
	Tipo                string `json:"tipo" binding:"omitempty,oneof=parcelado recorrente" doc:"parcelado = valor_total dividido; recorrente = valor_parcela repetido" example:"parcelado"`
	Descricao           string `json:"descricao" binding:"required,max=255" example:"Notebook"`
	Categoria           string `json:"categoria" binding:"max=60" example:"eletronicos"`
	ValorTotal          Money  `json:"valor_total" binding:"omitempty" doc:"Obrigatório para 'parcelado'"`
	ValorParcela        Money  `json:"valor_parcela" binding:"omitempty" doc:"Obrigatório para 'recorrente'"`
	TotalParcelas       int    `json:"total_parcelas" binding:"required,min=1,max=420" example:"10"`
	DataPrimeiraParcela *Date  `json:"data_primeira_parcela" doc:"Padrão: hoje (no PUT: mantém a atual)"`
	// ParcelasJaPagas pula as N primeiras parcelas na hora de lançar.
	ParcelasJaPagas int `json:"parcelas_ja_pagas" binding:"omitempty,min=0,max=419" doc:"Opcional. Nº de parcelas já quitadas ANTES do cadastro (fora do app): elas contam no progresso como pagas, mas não viram lançamento nem mexem no saldo. Use ao cadastrar um financiamento que começou meses atrás. No PUT, omitir volta para zero e as parcelas antigas são lançadas de novo." example:"5"`
	// Financiamento calcula o valor das parcelas a partir dos juros.
	Financiamento *FinanciamentoRequest `json:"financiamento" doc:"Opcional. Quando enviado, substitui valor_total: as parcelas são projetadas pela tabela de amortização (exige tipo 'parcelado'). No PUT, omitir remove o financiamento."`
}

// FinanciamentoRequest são os dados do contrato de financiamento.
type FinanciamentoRequest struct {
	Banco           string  `json:"banco" binding:"max=120" example:"Caixa Econômica Federal"`
	Sistema         string  `json:"sistema" binding:"omitempty,oneof=price sac" doc:"sac = parcela decrescente (padrão, usual em imóveis); price = parcela fixa" example:"sac"`
	ValorFinanciado Money   `json:"valor_financiado" doc:"Valor do contrato, já descontada a entrada"`
	TaxaJurosAnual  float64 `json:"taxa_juros_anual" binding:"min=0,max=100" doc:"Em % ao ano (0 a 100)" example:"8.66"`
	TipoTaxa        string  `json:"tipo_taxa" binding:"omitempty,oneof=efetiva nominal" doc:"efetiva (padrão): i_mes = (1+i_ano)^(1/12)-1; nominal: i_mes = i_ano/12" example:"efetiva"`
	PagamentoExtra  Money   `json:"pagamento_extra_mensal" doc:"Opcional. Valor pago a mais todo mês (amortização extraordinária): encurta o prazo e reduz os juros. As parcelas são geradas já com o aporte."`
}

// Domain converte para o domínio.
func (f FinanciamentoRequest) Domain() *domain.Financiamento {
	return &domain.Financiamento{
		Banco: f.Banco, Sistema: domain.SistemaAmortizacao(f.Sistema),
		ValorFinanciado: f.ValorFinanciado.Value, TaxaAnual: f.TaxaJurosAnual,
		TipoTaxa: domain.TipoTaxa(f.TipoTaxa), ExtraMensal: f.PagamentoExtra.Value,
	}
}

// SimularFinanciamentoRequest é o corpo de POST /parcelamentos/simular.
type SimularFinanciamentoRequest struct {
	Banco               string  `json:"banco" binding:"max=120" example:"Caixa Econômica Federal"`
	Sistema             string  `json:"sistema" binding:"omitempty,oneof=price sac" example:"sac"`
	ValorFinanciado     Money   `json:"valor_financiado"`
	TaxaJurosAnual      float64 `json:"taxa_juros_anual" binding:"min=0,max=100" example:"8.66"`
	TipoTaxa            string  `json:"tipo_taxa" binding:"omitempty,oneof=efetiva nominal" example:"efetiva"`
	PagamentoExtra      Money   `json:"pagamento_extra_mensal" doc:"Opcional. Valor pago a mais todo mês: a simulação já devolve o prazo encurtado."`
	TotalParcelas       int     `json:"total_parcelas" binding:"required,min=1,max=420" example:"360"`
	DataPrimeiraParcela *Date   `json:"data_primeira_parcela" doc:"Padrão: hoje"`
}

// FinanciamentoResponse descreve o contrato gravado.
type FinanciamentoResponse struct {
	Banco               string        `json:"banco" example:"Caixa Econômica Federal"`
	Sistema             string        `json:"sistema" example:"sac"`
	ValorFinanciado     MoneyResponse `json:"valor_financiado"`
	TaxaJurosAnual      float64       `json:"taxa_juros_anual" example:"8.66"`
	TipoTaxa            string        `json:"tipo_taxa" example:"efetiva"`
	TaxaJurosMensal     float64       `json:"taxa_juros_mensal" doc:"Em % ao mês, derivada da taxa anual" example:"0.694514"`
	TotalJurosProjetado MoneyResponse `json:"total_juros_projetado" doc:"Soma das parcelas menos o valor financiado"`
	PagamentoExtra      MoneyResponse `json:"pagamento_extra_mensal" doc:"Amortização extraordinária embutida em cada parcela (zero quando não há)"`
	PrazoContratado     int           `json:"prazo_contratado" doc:"Meses do contrato. Com aporte, total_parcelas do parcelamento vem menor: a diferença é a antecipação da quitação." example:"360"`
}

// SimulacaoResponse é devolvida por POST /parcelamentos/simular.
type SimulacaoResponse struct {
	Financiamento        FinanciamentoResponse      `json:"financiamento"`
	TotalParcelas        int                        `json:"total_parcelas" example:"360"`
	PrazoContratado      int                        `json:"prazo_contratado" doc:"Prazo informado. Com pagamento extra, total_parcelas vem menor: a diferença é a antecipação da quitação." example:"360"`
	ValorPrimeiraParcela MoneyResponse              `json:"valor_primeira_parcela"`
	ValorUltimaParcela   MoneyResponse              `json:"valor_ultima_parcela"`
	TotalAPagar          MoneyResponse              `json:"total_a_pagar"`
	TotalJuros           MoneyResponse              `json:"total_juros"`
	Parcelas             []ParcelaProjetadaResponse `json:"parcelas" doc:"Tabela de amortização mês a mês"`
	SemAporte            *ComparativoResponse       `json:"sem_aporte,omitempty" doc:"Mesmo contrato sem a amortização extraordinária. Só aparece quando há pagamento_extra_mensal."`
}

// ComparativoResponse são os totais de um cenário alternativo.
type ComparativoResponse struct {
	TotalParcelas int           `json:"total_parcelas" example:"360"`
	TotalAPagar   MoneyResponse `json:"total_a_pagar"`
	TotalJuros    MoneyResponse `json:"total_juros"`
}

// ParcelaProjetadaResponse é uma linha da tabela de amortização.
type ParcelaProjetadaResponse struct {
	Numero       int           `json:"numero" example:"1"`
	Data         string        `json:"data" example:"2026-10-10"`
	Valor        MoneyResponse `json:"valor"`
	Juros        MoneyResponse `json:"juros"`
	Amortizacao  MoneyResponse `json:"amortizacao"`
	SaldoDevedor MoneyResponse `json:"saldo_devedor" doc:"Saldo após pagar esta parcela"`
}

// NewSimulacaoResponse converte a saída do caso de uso.
func NewSimulacaoResponse(out *ports.SimulacaoFinanciamentoOutput) SimulacaoResponse {
	parcelas := make([]ParcelaProjetadaResponse, 0, len(out.Parcelas))
	for _, c := range out.Parcelas {
		parcelas = append(parcelas, ParcelaProjetadaResponse{
			Numero: c.Numero, Data: c.Data.Format("2006-01-02"), Valor: NewMoney(c.Valor),
			Juros: NewMoney(c.Juros), Amortizacao: NewMoney(c.Amortizacao), SaldoDevedor: NewMoney(c.SaldoDevedor),
		})
	}
	ultima := out.Parcelas[len(out.Parcelas)-1]
	return SimulacaoResponse{
		Financiamento:        newFinanciamentoResponse(&out.Financiamento, out.TotalJuros),
		TotalParcelas:        len(out.Parcelas),
		PrazoContratado:      out.PrazoContratado,
		ValorPrimeiraParcela: NewMoney(out.Parcelas[0].Valor),
		ValorUltimaParcela:   NewMoney(ultima.Valor),
		TotalAPagar:          NewMoney(out.TotalAPagar),
		TotalJuros:           NewMoney(out.TotalJuros),
		Parcelas:             parcelas,
		SemAporte:            newComparativoResponse(out.SemAporte),
	}
}

func newComparativoResponse(c *ports.ComparativoFinanciamento) *ComparativoResponse {
	if c == nil {
		return nil
	}
	return &ComparativoResponse{
		TotalParcelas: c.TotalParcelas,
		TotalAPagar:   NewMoney(c.TotalAPagar),
		TotalJuros:    NewMoney(c.TotalJuros),
	}
}

func newFinanciamentoResponse(f *domain.Financiamento, totalJuros domain.Money) FinanciamentoResponse {
	return FinanciamentoResponse{
		Banco: f.Banco, Sistema: string(f.Sistema), ValorFinanciado: NewMoney(f.ValorFinanciado),
		TaxaJurosAnual: f.TaxaAnual, TipoTaxa: string(f.TipoTaxa),
		TaxaJurosMensal:     math.Round(f.TaxaMensal()*100*1e6) / 1e6,
		TotalJurosProjetado: NewMoney(totalJuros),
		PagamentoExtra:      NewMoney(f.ExtraMensal),
		PrazoContratado:     f.PrazoContratado,
	}
}

// DeleteParcelamentoQuery são os parâmetros de DELETE /parcelamentos/{id}.
type DeleteParcelamentoQuery struct {
	ManterPagas bool `form:"manter_pagas" doc:"true = preserva as parcelas com data até hoje como lançamentos avulsos (histórico)"`
}

// DeleteParcelamentoResponse informa o efeito da exclusão.
type DeleteParcelamentoResponse struct {
	ParcelasRemovidas    int64 `json:"parcelas_removidas" example:"8"`
	ManteveParcelasPagas bool  `json:"manteve_pagas"`
}

// ParcelamentoResponse representa um parcelamento.
type ParcelamentoResponse struct {
	ID                  string                 `json:"id"`
	Tipo                string                 `json:"tipo" example:"parcelado"`
	Descricao           string                 `json:"descricao" example:"Notebook"`
	Categoria           string                 `json:"categoria" example:"eletronicos"`
	ValorTotal          MoneyResponse          `json:"valor_total"`
	ValorParcela        MoneyResponse          `json:"valor_parcela"`
	TotalParcelas       int                    `json:"total_parcelas" example:"10"`
	ParcelasJaPagas     int                    `json:"parcelas_ja_pagas" doc:"Parcelas quitadas antes do cadastro: não existem como lançamento, mas contam como pagas no progresso" example:"0"`
	DataPrimeiraParcela string                 `json:"data_primeira_parcela" example:"2026-10-10"`
	DataUltimaParcela   string                 `json:"data_ultima_parcela" example:"2027-07-10"`
	CriadoEm            time.Time              `json:"criado_em"`
	Progresso           *ProgressoResponse     `json:"progresso,omitempty"`
	Financiamento       *FinanciamentoResponse `json:"financiamento,omitempty"`
}

// ProgressoResponse mostra o andamento: parcelas com data até hoje contam como pagas.
type ProgressoResponse struct {
	Status            string        `json:"status" doc:"a_iniciar | em_andamento | quitado" example:"em_andamento"`
	ParcelasPagas     int           `json:"parcelas_pagas" example:"3"`
	ParcelasRestantes int           `json:"parcelas_restantes" example:"7"`
	ValorPago         MoneyResponse `json:"valor_pago" doc:"Total já amortizado"`
	ValorRestante     MoneyResponse `json:"valor_restante"`
	Percentual        int           `json:"percentual" doc:"0 a 100, pela quantidade de parcelas" example:"30"`
	ProximaParcela    string        `json:"proxima_parcela,omitempty" doc:"Data da próxima parcela (ausente se quitado)" example:"2026-10-10"`
}

// NewParcelamentoResponse converte domain.Parcelamento.
func NewParcelamentoResponse(p *domain.Parcelamento) ParcelamentoResponse {
	out := ParcelamentoResponse{
		ID: p.ID, Tipo: string(p.Tipo), Descricao: p.Descricao, Categoria: p.Categoria,
		ValorTotal: NewMoney(p.ValorTotal), ValorParcela: NewMoney(p.ValorParcela),
		TotalParcelas:       p.TotalParcelas,
		ParcelasJaPagas:     p.ParcelasJaPagas,
		DataPrimeiraParcela: p.DataPrimeiraParcela.Format("2006-01-02"),
		DataUltimaParcela:   domain.AddMonthsClamped(p.DataPrimeiraParcela, p.TotalParcelas-1).Format("2006-01-02"),
		CriadoEm:            p.CreatedAt,
		Progresso:           newProgresso(p.Progresso),
	}
	if p.Financiamento != nil {
		fin := newFinanciamentoResponse(p.Financiamento, p.ValorTotal-p.Financiamento.ValorFinanciado)
		out.Financiamento = &fin
	}
	return out
}

func newProgresso(pr *domain.ProgressoParcelamento) *ProgressoResponse {
	if pr == nil {
		return nil
	}
	out := &ProgressoResponse{
		ParcelasPagas: pr.ParcelasPagas, ParcelasRestantes: pr.ParcelasRestantes,
		ValorPago: NewMoney(pr.ValorPago), ValorRestante: NewMoney(pr.ValorRestante),
	}
	if total := pr.ParcelasPagas + pr.ParcelasRestantes; total > 0 {
		out.Percentual = pr.ParcelasPagas * 100 / total
	}
	switch {
	case pr.ParcelasRestantes == 0:
		out.Status = "quitado"
	case pr.ParcelasPagas == 0:
		out.Status = "a_iniciar"
	default:
		out.Status = "em_andamento"
	}
	if pr.ProximaParcela != nil {
		out.ProximaParcela = pr.ProximaParcela.Format("2006-01-02")
	}
	return out
}

// ParcelamentoDetalheResponse inclui as parcelas.
type ParcelamentoDetalheResponse struct {
	Parcelamento ParcelamentoResponse `json:"parcelamento"`
	Parcelas     []TransacaoResponse  `json:"parcelas"`
}

// ListParcelamentosResponse lista parcelamentos.
type ListParcelamentosResponse struct {
	Items []ParcelamentoResponse `json:"items"`
}

// ---------------------------------------------------------------------------
// Cartão de crédito
// ---------------------------------------------------------------------------

// CartaoRequest é o corpo de POST/PUT /cartoes.
type CartaoRequest struct {
	Nome          string `json:"nome" binding:"required,max=60" example:"Nubank"`
	Banco         string `json:"banco" binding:"max=60" example:"Nu Pagamentos"`
	DiaFechamento int    `json:"dia_fechamento" binding:"required,min=1,max=31" doc:"Dia em que a fatura fecha o ciclo de compras" example:"20"`
	DiaVencimento int    `json:"dia_vencimento" binding:"required,min=1,max=31" doc:"Dia do pagamento. O vencimento é sempre a PRÓXIMA ocorrência dele após o fechamento — por isso o mesmo par de campos cobre 'fecha 20, vence 21' (mesmo mês) e 'fecha 28, vence 5' (mês seguinte)." example:"21"`
	Limite        Money  `json:"limite" doc:"Opcional. Habilita o cálculo do limite disponível."`
	Ativo         *bool  `json:"ativo" doc:"Só no PUT: false arquiva o cartão sem apagar o histórico."`
}

// CompraCartaoRequest é o corpo de POST /cartoes/{id}/compras.
type CompraCartaoRequest struct {
	Valor         Money  `json:"valor" doc:"Valor TOTAL da compra; parcelado, o resíduo de centavos vai na 1ª"`
	Categoria     string `json:"categoria" binding:"max=60" example:"eletronicos"`
	Descricao     string `json:"descricao" binding:"max=255" example:"Notebook"`
	Data          *Date  `json:"data" doc:"Data da compra. Padrão: hoje. Compra DEPOIS do fechamento cai na fatura seguinte."`
	TotalParcelas int    `json:"total_parcelas" binding:"omitempty,min=1,max=36" doc:"1 (padrão) = à vista" example:"3"`
}

// PagarFaturaRequest é o corpo de POST /cartoes/{id}/faturas/{ref}/pagar.
type PagarFaturaRequest struct {
	Data  *Date `json:"data" doc:"Data em que o dinheiro saiu da conta (padrão: hoje). É ELA que define em que mês o gasto aparece — não o vencimento da fatura."`
	Valor Money `json:"valor" doc:"Opcional: o padrão é o total da fatura."`
}

// CartaoResponse representa um cartão.
type CartaoResponse struct {
	ID               string          `json:"id"`
	Nome             string          `json:"nome" example:"Nubank"`
	Banco            string          `json:"banco" example:"Nu Pagamentos"`
	DiaFechamento    int             `json:"dia_fechamento" example:"20"`
	DiaVencimento    int             `json:"dia_vencimento" example:"21"`
	Limite           MoneyResponse   `json:"limite"`
	Ativo            bool            `json:"ativo"`
	CriadoEm         time.Time       `json:"criado_em"`
	LimiteDisponivel MoneyResponse   `json:"limite_disponivel" doc:"Limite menos tudo que ainda não foi pago"`
	MelhorDiaCompra  string          `json:"melhor_dia_compra" doc:"Dia seguinte ao fechamento: comprando nele você ganha o maior prazo até o pagamento" example:"2026-09-21"`
	FaturaAPagar     *FaturaResponse `json:"fatura_a_pagar,omitempty" doc:"Fechada e ainda não paga (ausente quando não há)"`
	FaturaEmAberto   *FaturaResponse `json:"fatura_em_aberto,omitempty" doc:"Ainda acumulando compras"`
}

// FaturaResponse é um ciclo do cartão.
type FaturaResponse struct {
	Competencia     string                    `json:"competencia" example:"2026-10"`
	Vencimento      string                    `json:"vencimento" example:"2026-10-21"`
	InicioCiclo     string                    `json:"inicio_ciclo" example:"2026-09-21"`
	FimCiclo        string                    `json:"fim_ciclo" doc:"Fechamento: compras após esta data caem na fatura seguinte" example:"2026-10-20"`
	Total           MoneyResponse             `json:"total"`
	Status          string                    `json:"status" doc:"aberta | fechada | parcial | paga" example:"fechada"`
	PagoEm          string                    `json:"pago_em,omitempty" doc:"Data do pagamento mais recente" example:"2026-10-20"`
	ValorPago       *MoneyResponse            `json:"valor_pago,omitempty" doc:"Soma do que já saiu da conta por esta fatura"`
	Restante        MoneyResponse             `json:"restante" doc:"Quanto ainda falta pagar (zero quando quitada)"`
	UltimoPagamento *MoneyResponse            `json:"ultimo_pagamento,omitempty" doc:"Valor do pagamento mais recente — é o que desfazer remove"`
	Pagamentos      []PagamentoFaturaResponse `json:"pagamentos,omitempty" doc:"Só no detalhe da fatura, do mais antigo para o mais novo"`
	Compras         []CompraCartaoResponse    `json:"compras,omitempty"`
}

// PagamentoFaturaResponse é um pagamento da fatura. Uma fatura pode ter vários:
// pagar parte agora e o resto depois é comum, e cada parte sai da conta no seu
// próprio dia.
type PagamentoFaturaResponse struct {
	ID          string        `json:"id"`
	Valor       MoneyResponse `json:"valor"`
	PagoEm      string        `json:"pago_em" example:"2026-10-20"`
	TransacaoID string        `json:"transacao_id" doc:"A saída que este pagamento criou"`
}

// CompraCartaoResponse é uma parcela de uma compra.
type CompraCartaoResponse struct {
	ID            string        `json:"id"`
	GrupoID       string        `json:"grupo_id" doc:"Mesmo em todas as parcelas de uma compra; use para excluir a compra inteira"`
	Valor         MoneyResponse `json:"valor" doc:"Valor DESTA parcela"`
	Categoria     string        `json:"categoria"`
	Descricao     string        `json:"descricao"`
	Data          string        `json:"data" doc:"Data da compra" example:"2026-09-15"`
	NumeroParcela int           `json:"numero_parcela" example:"1"`
	TotalParcelas int           `json:"total_parcelas" example:"3"`
}

// NewCartaoResponse converte o resumo do cartão.
func NewCartaoResponse(r *domain.ResumoCartao) CartaoResponse {
	c := r.Cartao
	out := CartaoResponse{
		ID: c.ID, Nome: c.Nome, Banco: c.Banco,
		DiaFechamento: c.DiaFechamento, DiaVencimento: c.DiaVencimento,
		Limite: NewMoney(c.Limite), Ativo: c.Ativo, CriadoEm: c.CreatedAt,
		LimiteDisponivel: NewMoney(r.LimiteDisponivel),
		MelhorDiaCompra:  r.MelhorDiaCompra.Format("2006-01-02"),
	}
	if r.APagar != nil {
		f := NewFaturaResponse(r.APagar)
		out.FaturaAPagar = &f
	}
	if r.EmAberto != nil {
		f := NewFaturaResponse(r.EmAberto)
		out.FaturaEmAberto = &f
	}
	return out
}

// NewFaturaResponse converte uma fatura.
func NewFaturaResponse(f *domain.Fatura) FaturaResponse {
	out := FaturaResponse{
		Competencia: f.Competencia,
		Vencimento:  f.Vencimento.Format("2006-01-02"),
		InicioCiclo: f.InicioCiclo.Format("2006-01-02"),
		FimCiclo:    f.FimCiclo.Format("2006-01-02"),
		Total:       NewMoney(f.Total),
		Status:      string(f.Status),
		Restante:    NewMoney(f.Restante()),
	}
	if f.Pago > 0 {
		pago := NewMoney(f.Pago)
		out.ValorPago = &pago
	}
	if !f.UltimoPagamentoEm.IsZero() {
		out.PagoEm = f.UltimoPagamentoEm.Format("2006-01-02")
		ultimo := NewMoney(f.UltimoPagamentoValor)
		out.UltimoPagamento = &ultimo
	}
	for _, p := range f.Pagamentos {
		out.Pagamentos = append(out.Pagamentos, PagamentoFaturaResponse{
			ID: p.ID, Valor: NewMoney(p.Valor), PagoEm: p.PagoEm.Format("2006-01-02"),
			TransacaoID: p.TransacaoID,
		})
	}
	for _, c := range f.Compras {
		out.Compras = append(out.Compras, CompraCartaoResponse{
			ID: c.ID, GrupoID: c.GrupoID, Valor: NewMoney(c.Valor),
			Categoria: c.Categoria, Descricao: c.Descricao,
			Data:          c.DataCompra.Format("2006-01-02"),
			NumeroParcela: c.NumeroParcela, TotalParcelas: c.TotalParcelas,
		})
	}
	return out
}

// ListCartoesResponse lista cartões.
type ListCartoesResponse struct {
	Items []CartaoResponse `json:"items"`
}

// ListFaturasResponse lista faturas (sem as compras).
type ListFaturasResponse struct {
	Items []FaturaResponse `json:"items"`
}

// CompraCriadaResponse devolve as parcelas geradas.
type CompraCriadaResponse struct {
	GrupoID  string                 `json:"grupo_id"`
	Parcelas []CompraCartaoResponse `json:"parcelas"`
}

// ---------------------------------------------------------------------------
// Webhook / saúde / erros
// ---------------------------------------------------------------------------

// EvolutionWebhookExample documenta o payload da Evolution API (exemplo).
type EvolutionWebhookExample struct {
	Event string               `json:"event" example:"messages.upsert"`
	Data  EvolutionDataExample `json:"data"`
}

// EvolutionDataExample é o bloco data do evento.
type EvolutionDataExample struct {
	Key     EvolutionKeyExample     `json:"key"`
	Message EvolutionMessageExample `json:"message"`
}

// EvolutionKeyExample identifica remetente e mensagem.
type EvolutionKeyExample struct {
	RemoteJID string `json:"remoteJid" example:"5511987654321@s.whatsapp.net"`
	FromMe    bool   `json:"fromMe"`
	ID        string `json:"id" example:"3EB0C767D26A1D8B"`
}

// EvolutionMessageExample é o conteúdo textual.
type EvolutionMessageExample struct {
	Conversation string `json:"conversation" example:"Gastei 150,00 mercado"`
}

// AckResponse é a resposta do webhook.
type AckResponse struct {
	Status string `json:"status" example:"ok"`
}

// HealthResponse é a resposta de /healthz e /readyz.
type HealthResponse struct {
	Status string            `json:"status" example:"ok"`
	Checks map[string]string `json:"checks,omitempty"`
}

// ErrorBody é o envelope padrão de erro.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail descreve o erro.
type ErrorDetail struct {
	Code       string `json:"code" doc:"Identificador estável (validation_error, unauthorized, account_locked, rate_limited...)" example:"validation_error"`
	Message    string `json:"message" example:"obrigatório"`
	Field      string `json:"field,omitempty" example:"valor"`
	RetryAfter int    `json:"retry_after_segundos,omitempty" example:"60"`
	RequestID  string `json:"request_id,omitempty"`
}
