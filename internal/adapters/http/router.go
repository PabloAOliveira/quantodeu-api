// Package http monta o roteador Gin (adaptador primário/driving).
package http

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/dto"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/handlers"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/openapi"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/session"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// Limiters agrupa os rate limiters por escopo (memória ou Redis).
type Limiters struct {
	API     ports.RateLimiter // geral, por IP
	Auth    ports.RateLimiter // login/registro, por IP
	Webhook ports.RateLimiter
}

// RouterDeps agrupa as dependências do roteador.
type RouterDeps struct {
	Production     bool
	Version        string
	AllowedOrigins []string
	TrustedProxies []string
	MaxBodyBytes   int64
	DocsEnabled    bool
	TracingEnabled bool

	Limiters Limiters
	Metrics  interface {
		middlewares.HTTPRecorder
		middlewares.RateLimitObserver
	}
	Cookies *session.CookieManager
	Log     *slog.Logger

	// WhatsAppBot registra o webhook e a verificação de telefone. Desligado
	// (v1, sem bot) essas rotas não existem — e não aparecem no OpenAPI.
	WhatsAppBot bool

	Auth ports.AuthUseCase
	// EmailVerification + RequireVerifiedEmail: exige e-mail confirmado nas
	// rotas financeiras.
	EmailVerification    ports.EmailVerificationUseCase
	RequireVerifiedEmail bool
	AuthHandler          *handlers.AuthHandler
	Financas             *handlers.FinancasHandler
	Cartoes              *handlers.CartaoHandler
	Webhook              *handlers.WebhookHandler
	Health               *handlers.HealthHandler
}

// NewRouter cria o engine Gin com middlewares, rotas e a especificação OpenAPI.
func NewRouter(d RouterDeps) (*gin.Engine, *openapi.Doc, error) {
	if d.Production {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.RedirectTrailingSlash = false

	// Sem proxies confiáveis, ClientIP() usa o RemoteAddr (não confia em
	// X-Forwarded-For forjado).
	var proxies []string
	if len(d.TrustedProxies) > 0 {
		proxies = d.TrustedProxies
	}
	if err := r.SetTrustedProxies(proxies); err != nil {
		return nil, nil, err
	}

	chain := []gin.HandlerFunc{middlewares.RequestID(), middlewares.Recovery(d.Log)}
	if d.TracingEnabled {
		chain = append(chain, otelgin.Middleware("quantodeu-api",
			otelgin.WithFilter(func(req *http.Request) bool {
				return req.URL.Path != "/healthz" && req.URL.Path != "/readyz"
			})))
	}
	if d.Metrics != nil {
		chain = append(chain, middlewares.Metrics(d.Metrics))
	}
	chain = append(chain,
		middlewares.Logger(d.Log),
		middlewares.SecurityHeaders(d.Production),
		middlewares.BodyLimit(d.MaxBodyBytes),
	)
	r.Use(chain...)

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "rota não encontrada"}})
	})
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": gin.H{"code": "method_not_allowed", "message": "método não permitido"}})
	})

	doc := openapi.New(openapi.Info{
		Title:   "QuantoDeu API",
		Version: d.Version,
		Description: "API do app de finanças pessoais QuantoDeu.\n\n" +
			"**Como testar pelo Swagger:** 1) `POST /api/v1/auth/register`; 2) `POST /api/v1/auth/login` — o navegador guarda o cookie de sessão (HttpOnly) e as rotas protegidas passam a funcionar; 3) use as demais rotas.\n\n" +
			"Valores monetários aceitam número (`150.5`) ou texto (`\"150,50\"`). Erros seguem o envelope `{\"error\": {\"code\", \"message\", \"field\"}}`.",
		CookieName: d.Cookies.Name,
	}, dto.ErrorBody{})

	rl := func(l ports.RateLimiter, scope string) gin.HandlerFunc {
		return middlewares.RateLimit(l, scope, d.Metrics, d.Log)
	}

	// Saúde
	doc.Register(r, "", openapi.Route{Method: http.MethodGet, Path: "/healthz", OperationID: "liveness", Summary: "Liveness", Tags: []string{"Saúde"},
		Responses: map[int]openapi.Response{200: {Description: "Processo ativo", Body: dto.HealthResponse{}}}}, d.Health.Live)
	doc.Register(r, "", openapi.Route{Method: http.MethodGet, Path: "/readyz", OperationID: "readiness", Summary: "Readiness (PostgreSQL/Redis)", Tags: []string{"Saúde"},
		Responses: map[int]openapi.Response{200: {Description: "Dependências OK", Body: dto.HealthResponse{}}, 503: {Description: "Dependência indisponível", Body: dto.HealthResponse{}}}}, d.Health.Ready)

	if d.DocsEnabled {
		doc.RegisterUI(r)
	}

	const base = "/api/v1"
	v1 := r.Group(base)

	// Webhook: sem CORS/CSRF/cookie; autenticado por token/assinatura do provedor.
	if d.WhatsAppBot {
		doc.Register(v1, base, openapi.Route{
			Method: http.MethodPost, Path: "/webhook/whatsapp", OperationID: "webhookWhatsApp",
			Summary: "Recebe mensagens do provedor de WhatsApp", Tags: []string{"WhatsApp"}, Security: openapi.WebhookAuth,
			Description: "Payload do provedor configurado em WHATSAPP_PROVIDER (o exemplo é da Evolution API). " +
				"Mensagens: `Gastei 150,00 mercado`, `Recebi 1500 freela`, `Saldo`, `verificar 123456`. " +
				"Números não verificados só podem enviar o comando de verificação.",
			Body:      dto.EvolutionWebhookExample{},
			Responses: map[int]openapi.Response{200: {Description: "Evento aceito (inclusive quando ignorado)", Body: dto.AckResponse{}}},
			Errors:    []int{http.StatusBadRequest},
		}, rl(d.Limiters.Webhook, "webhook"), d.Webhook.WhatsApp)
	}

	app := v1.Group("",
		middlewares.CORS(d.AllowedOrigins),
		middlewares.CSRFProtection(d.AllowedOrigins),
		rl(d.Limiters.API, "api"),
	)
	app.OPTIONS("/*path", func(c *gin.Context) {}) // pré-flight CORS

	auth := app.Group("/auth")
	ab := base + "/auth"
	doc.Register(auth, ab, openapi.Route{Method: http.MethodPost, Path: "/register", OperationID: "register", Summary: "Cadastrar usuário", Tags: []string{"Autenticação"},
		Body: dto.RegisterRequest{}, Responses: map[int]openapi.Response{201: {Description: "Usuário criado", Body: dto.UserResponse{}}}, Errors: []int{http.StatusConflict}},
		rl(d.Limiters.Auth, "register"), d.AuthHandler.Register)
	doc.Register(auth, ab, openapi.Route{Method: http.MethodPost, Path: "/login", OperationID: "login", Summary: "Login (cookie no navegador ou token para apps)", Tags: []string{"Autenticação"},
		Description: "`modo: cookie` (padrão) grava o cookie HttpOnly. `modo: token` (apps nativos) devolve `token` para enviar em `Authorization: Bearer`. Após 5 falhas para o mesmo e-mail em 24h, a conta é bloqueada progressivamente (1, 2, 4... até 30 min) com 429 `account_locked` e cabeçalho Retry-After.",
		Body:        dto.LoginRequest{}, Responses: map[int]openapi.Response{200: {Description: "Autenticado (Set-Cookie no modo cookie; token no corpo no modo token)", Body: dto.LoginResponse{}}}, Errors: []int{http.StatusUnauthorized}},
		rl(d.Limiters.Auth, "login"), d.AuthHandler.Login)
	doc.Register(auth, ab, openapi.Route{Method: http.MethodPost, Path: "/logout", OperationID: "logout", Summary: "Logout", Tags: []string{"Autenticação"},
		Responses: map[int]openapi.Response{204: {Description: "Sessão encerrada"}}}, d.AuthHandler.Logout)

	protected := app.Group("", middlewares.AuthRequired(d.Auth, d.Cookies, d.Log))
	reg := func(rt openapi.Route, h gin.HandlerFunc) {
		rt.Security = openapi.CookieAuth
		doc.Register(protected, base, rt, h)
	}

	// Conta
	conta := []string{"Conta"}
	reg(openapi.Route{Method: http.MethodGet, Path: "/me", OperationID: "me", Summary: "Perfil, saldo e totais do mês", Tags: conta,
		Responses: map[int]openapi.Response{200: {Description: "Perfil", Body: dto.MeResponse{}}}}, d.AuthHandler.Me)
	reg(openapi.Route{Method: http.MethodPut, Path: "/me/senha", OperationID: "changePassword", Summary: "Trocar senha (encerra todas as sessões)", Tags: conta,
		Description: "Encerra as sessões de TODOS os dispositivos e emite um cookie novo para este navegador.",
		Body:        dto.ChangePasswordRequest{}, Responses: map[int]openapi.Response{200: {Description: "Senha alterada; novo cookie emitido", Body: dto.LoginResponse{}}}}, d.AuthHandler.ChangePassword)
	reg(openapi.Route{Method: http.MethodDelete, Path: "/me/sessoes", OperationID: "revokeSessions", Summary: "Encerrar sessões", Tags: conta,
		Query: dto.RevokeSessionsQuery{}, Responses: map[int]openapi.Response{200: {Description: "Sessões encerradas", Body: dto.RevokeSessionsResponse{}}}}, d.AuthHandler.RevokeSessions)
	if d.WhatsAppBot {
		reg(openapi.Route{Method: http.MethodPost, Path: "/me/telefone/verificacao", OperationID: "startPhoneVerification", Summary: "Iniciar verificação do telefone", Tags: conta,
			Description: "Gera um código de 6 dígitos (válido por 10 min, novo código a cada 60 s). Método `mensagem`: envie `verificar <código>` do seu WhatsApp. Método `codigo`: o código chega no WhatsApp e você confirma na rota /confirmar.",
			Body:        dto.StartVerificationRequest{}, Responses: map[int]openapi.Response{202: {Description: "Desafio criado", Body: dto.StartVerificationResponse{}}}, Errors: []int{http.StatusConflict}}, d.AuthHandler.StartPhoneVerification)
		reg(openapi.Route{Method: http.MethodPost, Path: "/me/telefone/verificacao/confirmar", OperationID: "confirmPhoneVerification", Summary: "Confirmar código recebido no WhatsApp", Tags: conta,
			Body: dto.ConfirmVerificationRequest{}, Responses: map[int]openapi.Response{200: {Description: "Telefone verificado", Body: dto.MessageResponse{}}}}, d.AuthHandler.ConfirmPhoneVerification)
	}

	reg(openapi.Route{Method: http.MethodPost, Path: "/me/email/verificacao", OperationID: "startEmailVerification", Summary: "Enviar/reenviar código de confirmação do e-mail", Tags: conta,
		Description: "O cadastro já envia o código automaticamente. Use esta rota para reenviar (1 envio por minuto; código válido por 30 min).",
		Responses:   map[int]openapi.Response{202: {Description: "Código enviado", Body: dto.StartEmailVerificationResponse{}}}, Errors: []int{http.StatusConflict, http.StatusServiceUnavailable}}, d.AuthHandler.StartEmailVerification)
	reg(openapi.Route{Method: http.MethodPost, Path: "/me/email/verificacao/confirmar", OperationID: "confirmEmailVerification", Summary: "Confirmar código recebido por e-mail", Tags: conta,
		Body: dto.ConfirmVerificationRequest{}, Responses: map[int]openapi.Response{200: {Description: "E-mail verificado", Body: dto.MessageResponse{}}}}, d.AuthHandler.ConfirmEmailVerification)

	// Rotas financeiras: exigem e-mail confirmado (se habilitado).
	if d.RequireVerifiedEmail && d.EmailVerification != nil {
		protected = protected.Group("", middlewares.EmailVerifiedRequired(d.EmailVerification, d.Log))
		reg = func(rt openapi.Route, h gin.HandlerFunc) {
			rt.Security = openapi.CookieAuth
			rt.Errors = append(rt.Errors, http.StatusForbidden)
			doc.Register(protected, base, rt, h)
		}
	}

	// Transações
	tx := []string{"Transações"}
	notFound := []int{http.StatusNotFound}
	reg(openapi.Route{Method: http.MethodGet, Path: "/transacoes", OperationID: "listTransacoes", Summary: "Listar lançamentos do mês", Tags: tx,
		Query: dto.ListTransacoesQuery{}, Responses: map[int]openapi.Response{200: {Description: "Página de lançamentos", Body: dto.ListTransacoesResponse{}}}}, d.Financas.ListTransacoes)
	reg(openapi.Route{Method: http.MethodPost, Path: "/transacoes", OperationID: "createTransacao", Summary: "Lançamento manual", Tags: tx,
		Body: dto.CreateTransacaoRequest{}, Responses: map[int]openapi.Response{201: {Description: "Criado", Body: dto.TransacaoResponse{}}}}, d.Financas.CreateTransacao)
	reg(openapi.Route{Method: http.MethodGet, Path: "/transacoes/:id", OperationID: "getTransacao", Summary: "Detalhar lançamento", Tags: tx,
		Responses: map[int]openapi.Response{200: {Description: "Lançamento", Body: dto.TransacaoResponse{}}}, Errors: notFound}, d.Financas.GetTransacao)
	reg(openapi.Route{Method: http.MethodPut, Path: "/transacoes/:id", OperationID: "updateTransacao", Summary: "Editar lançamento", Tags: tx,
		Description: "Substitui tipo, valor, categoria, descrição e data. Parcelas podem ter valor/data ajustados, mas continuam sendo saídas do parcelamento.",
		Body:        dto.UpdateTransacaoRequest{}, Responses: map[int]openapi.Response{200: {Description: "Atualizado", Body: dto.TransacaoResponse{}}}, Errors: notFound}, d.Financas.UpdateTransacao)
	reg(openapi.Route{Method: http.MethodDelete, Path: "/transacoes/:id", OperationID: "deleteTransacao", Summary: "Excluir lançamento", Tags: tx,
		Description: "Parcelas não podem ser excluídas individualmente (409): exclua ou edite o parcelamento.",
		Responses:   map[int]openapi.Response{204: {Description: "Excluído"}}, Errors: []int{http.StatusNotFound, http.StatusConflict}}, d.Financas.DeleteTransacao)

	reg(openapi.Route{Method: http.MethodGet, Path: "/resumo", OperationID: "resumo", Summary: "Resumo consolidado do mês", Tags: []string{"Resumo"},
		Query: dto.ResumoQuery{}, Responses: map[int]openapi.Response{200: {Description: "Resumo", Body: dto.ResumoResponse{}}}}, d.Financas.Resumo)

	// Parcelamentos
	pc := []string{"Parcelamentos"}
	reg(openapi.Route{Method: http.MethodGet, Path: "/parcelamentos", OperationID: "listParcelamentos", Summary: "Listar parcelamentos", Tags: pc,
		Responses: map[int]openapi.Response{200: {Description: "Parcelamentos", Body: dto.ListParcelamentosResponse{}}}}, d.Financas.ListParcelamentos)
	reg(openapi.Route{Method: http.MethodPost, Path: "/parcelamentos", OperationID: "createParcelamento", Summary: "Registrar parcelamento, despesa recorrente ou financiamento", Tags: pc,
		Description: "Com o bloco `financiamento`, as parcelas são projetadas por juros (SAC ou Price) e cada mês pode ter um valor diferente.",
		Body:        dto.CreateParcelamentoRequest{}, Responses: map[int]openapi.Response{201: {Description: "Criado com as parcelas", Body: dto.ParcelamentoDetalheResponse{}}}}, d.Financas.CreateParcelamento)
	cartoes := []string{"Cartões"}
	reg(openapi.Route{Method: http.MethodGet, Path: "/cartoes", OperationID: "listCartoes",
		Summary: "Listar cartões", Tags: cartoes,
		Description: "Cada cartão vem com as duas faturas que convivem em qualquer mês: a **fechada** (a pagar) e a **em aberto** (ainda acumulando), mais o limite disponível e o melhor dia de compra.",
		Responses:   map[int]openapi.Response{200: {Description: "Cartões", Body: dto.ListCartoesResponse{}}}}, d.Cartoes.ListCartoes)
	reg(openapi.Route{Method: http.MethodPost, Path: "/cartoes", OperationID: "createCartao",
		Summary: "Cadastrar cartão", Tags: cartoes, Body: dto.CartaoRequest{},
		Responses: map[int]openapi.Response{201: {Description: "Cartão criado", Body: dto.CartaoResponse{}}}}, d.Cartoes.CreateCartao)
	reg(openapi.Route{Method: http.MethodGet, Path: "/cartoes/:id", OperationID: "getCartao",
		Summary: "Detalhar cartão", Tags: cartoes,
		Responses: map[int]openapi.Response{200: {Description: "Cartão", Body: dto.CartaoResponse{}}}}, d.Cartoes.GetCartao)
	reg(openapi.Route{Method: http.MethodPut, Path: "/cartoes/:id", OperationID: "updateCartao",
		Summary: "Editar cartão (ou arquivar com ativo=false)", Tags: cartoes, Body: dto.CartaoRequest{},
		Responses: map[int]openapi.Response{200: {Description: "Cartão atualizado", Body: dto.CartaoResponse{}}}}, d.Cartoes.UpdateCartao)
	reg(openapi.Route{Method: http.MethodDelete, Path: "/cartoes/:id", OperationID: "deleteCartao",
		Summary: "Excluir cartão", Tags: cartoes,
		Description: "Só funciona em cartão sem compras. Com histórico devolve 409 `cartao_com_historico` — o certo é arquivar (PUT com `ativo: false`), para não apagar o passado financeiro.",
		Responses:   map[int]openapi.Response{204: {Description: "Removido"}}, Errors: []int{http.StatusConflict}}, d.Cartoes.DeleteCartao)

	reg(openapi.Route{Method: http.MethodGet, Path: "/cartoes/:id/faturas", OperationID: "listFaturas",
		Summary: "Histórico de faturas", Tags: cartoes,
		Responses: map[int]openapi.Response{200: {Description: "Faturas, da mais recente para a mais antiga", Body: dto.ListFaturasResponse{}}}}, d.Cartoes.ListFaturas)
	reg(openapi.Route{Method: http.MethodGet, Path: "/cartoes/:id/faturas/:competencia", OperationID: "getFatura",
		Summary: "Detalhar fatura de uma competência (AAAA-MM)", Tags: cartoes,
		Responses: map[int]openapi.Response{200: {Description: "Fatura com as compras", Body: dto.FaturaResponse{}}}}, d.Cartoes.GetFatura)
	reg(openapi.Route{Method: http.MethodPost, Path: "/cartoes/:id/faturas/:competencia/pagar", OperationID: "pagarFatura",
		Summary: "Marcar fatura como paga", Tags: cartoes, Body: dto.PagarFaturaRequest{},
		Description: "Cria UMA saída no saldo na data do pagamento. É esse lançamento que entra nas despesas do mês — a compra no cartão, não. Por isso a fatura de setembro paga em outubro aparece nas despesas de outubro.",
		Responses:   map[int]openapi.Response{200: {Description: "Fatura paga", Body: dto.FaturaResponse{}}}, Errors: []int{http.StatusConflict, http.StatusUnprocessableEntity}}, d.Cartoes.PagarFatura)
	reg(openapi.Route{Method: http.MethodDelete, Path: "/cartoes/:id/faturas/:competencia/pagar", OperationID: "desfazerPagamentoFatura",
		Summary: "Desfazer o pagamento da fatura", Tags: cartoes,
		Description: "Remove a saída do saldo e devolve a fatura para 'fechada'.",
		Responses:   map[int]openapi.Response{204: {Description: "Pagamento desfeito"}}, Errors: []int{http.StatusConflict}}, d.Cartoes.DesfazerPagamento)

	reg(openapi.Route{Method: http.MethodPost, Path: "/cartoes/:id/compras", OperationID: "createCompraCartao",
		Summary: "Lançar compra no cartão", Tags: cartoes, Body: dto.CompraCartaoRequest{},
		Description: "NÃO mexe no saldo: compra no cartão é dívida com o banco. Parcelada, gera uma linha por parcela em faturas consecutivas. Compra feita DEPOIS do fechamento cai na fatura seguinte.",
		Responses:   map[int]openapi.Response{201: {Description: "Compra registrada", Body: dto.CompraCriadaResponse{}}}}, d.Cartoes.CreateCompra)
	reg(openapi.Route{Method: http.MethodDelete, Path: "/compras/:grupo", OperationID: "deleteCompraCartao",
		Summary: "Excluir compra (todas as parcelas)", Tags: cartoes,
		Responses: map[int]openapi.Response{204: {Description: "Removida"}}}, d.Cartoes.DeleteCompra)

	reg(openapi.Route{Method: http.MethodPost, Path: "/parcelamentos/simular", OperationID: "simularFinanciamento", Summary: "Simular financiamento (não grava nada)", Tags: pc,
		Description: "Projeta a tabela de amortização a partir do valor financiado, prazo, taxa anual e sistema (SAC ou Price). Use para mostrar a parcela antes de cadastrar.",
		Body:        dto.SimularFinanciamentoRequest{}, Responses: map[int]openapi.Response{200: {Description: "Projeção", Body: dto.SimulacaoResponse{}}}}, d.Financas.SimularFinanciamento)
	reg(openapi.Route{Method: http.MethodGet, Path: "/parcelamentos/:id", OperationID: "getParcelamento", Summary: "Detalhar parcelamento e parcelas", Tags: pc,
		Responses: map[int]openapi.Response{200: {Description: "Parcelamento", Body: dto.ParcelamentoDetalheResponse{}}}, Errors: notFound}, d.Financas.GetParcelamento)
	reg(openapi.Route{Method: http.MethodPut, Path: "/parcelamentos/:id", OperationID: "updateParcelamento", Summary: "Editar parcelamento (regera as parcelas)", Tags: pc,
		Description: "Recalcula e REGERA todas as parcelas atomicamente; ajustes manuais feitos em parcelas individuais são descartados.",
		Body:        dto.CreateParcelamentoRequest{}, Responses: map[int]openapi.Response{200: {Description: "Atualizado", Body: dto.ParcelamentoDetalheResponse{}}}, Errors: notFound}, d.Financas.UpdateParcelamento)
	reg(openapi.Route{Method: http.MethodDelete, Path: "/parcelamentos/:id", OperationID: "deleteParcelamento", Summary: "Excluir parcelamento", Tags: pc,
		Query: dto.DeleteParcelamentoQuery{}, Responses: map[int]openapi.Response{200: {Description: "Excluído", Body: dto.DeleteParcelamentoResponse{}}}, Errors: notFound}, d.Financas.DeleteParcelamento)

	return r, doc, nil
}
