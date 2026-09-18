package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// WebhookService implementa ports.WebhookUseCase: identifica o usuário pelo
// telefone, interpreta o texto e grava a transação vinculada a ele.
type WebhookService struct {
	users      ports.UserRepository
	transacoes ports.TransacaoRepository
	parser     ports.MessageParser
	sender     ports.WhatsAppSender
	verifier   whatsAppVerifier
	skipVerif  bool
	needEmail  bool
	clock      ports.Clock
	metrics    ports.Metrics
	loc        *time.Location
	log        *slog.Logger
}

// whatsAppVerifier confirma códigos enviados pelo próprio número do usuário.
type whatsAppVerifier interface {
	ConfirmFromWhatsApp(ctx context.Context, userID, codigo string) error
}

var _ ports.WebhookUseCase = (*WebhookService)(nil)

// WebhookDeps agrupa as dependências do WebhookService.
type WebhookDeps struct {
	Users      ports.UserRepository
	Transacoes ports.TransacaoRepository
	Parser     ports.MessageParser
	Sender     ports.WhatsAppSender // pode ser no-op
	Verifier   *PhoneVerificationService
	// SkipPhoneVerification permite lançar por WhatsApp sem o número estar
	// verificado. Zero value = exige verificação (seguro por padrão).
	SkipPhoneVerification bool
	// RequireVerifiedEmail bloqueia lançamentos de contas com e-mail não confirmado.
	RequireVerifiedEmail bool
	Clock                ports.Clock
	Metrics              ports.Metrics
	Location             *time.Location
	Log                  *slog.Logger
}

// NewWebhookService cria o serviço.
func NewWebhookService(d WebhookDeps) *WebhookService {
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Metrics == nil {
		d.Metrics = NoopMetrics{}
	}
	var v whatsAppVerifier
	if d.Verifier != nil {
		v = d.Verifier
	}
	return &WebhookService{
		users: d.Users, transacoes: d.Transacoes, parser: d.Parser, sender: d.Sender, verifier: v, skipVerif: d.SkipPhoneVerification, needEmail: d.RequireVerifiedEmail,
		clock: d.Clock, metrics: d.Metrics, loc: d.Location, log: d.Log,
	}
}

// MaxIdadeMensagem é a idade máxima aceita para o timestamp do provedor.
const MaxIdadeMensagem = 72 * time.Hour

var reComandoVerificar = regexp.MustCompile(`(?i)^\s*(?:verificar|verifica[çc][aã]o|c[oó]digo)\s*[:\-]?\s*(\d{6})\s*[.!]?\s*$`)

const mensagemNaoVerificado = "🔒 Seu número ainda não foi verificado no QuantoDeu.\n" +
	"No app, inicie a verificação e envie aqui: *verificar <código>*."

var reComandoSaldo = regexp.MustCompile(`(?i)^\s*(?:saldo|quanto\s+deu|resumo)\s*[?!.]*\s*$`)

const mensagemAjuda = "🤔 Não entendi. Envie, por exemplo:\n" +
	"• *Gastei 150,00 mercado*\n" +
	"• *Paguei 80.50 gasolina*\n" +
	"• *Recebi 1500 freela*\n" +
	"• *Saldo* para consultar seu saldo"

// ProcessMessage processa uma mensagem recebida. Erros de negócio esperados
// (usuário desconhecido, texto não interpretável, duplicidade) NÃO são erros:
// viram um Status, para que o webhook responda 200 e o provedor não reenvie.
func (s *WebhookService) ProcessMessage(ctx context.Context, msg domain.MensagemRecebida) (*domain.ResultadoWebhook, error) {
	res, err := s.process(ctx, msg)
	status := "erro"
	if err == nil {
		status = string(res.Status)
	}
	s.metrics.WebhookProcessado(msg.Provider, status)
	if err == nil && res.Status == domain.StatusCriada {
		s.metrics.TransacaoRegistrada(string(domain.OrigemWhatsApp), string(res.Transacao.Tipo))
	}
	return res, err
}

func (s *WebhookService) process(ctx context.Context, msg domain.MensagemRecebida) (*domain.ResultadoWebhook, error) {
	logger := s.log.With(slog.String("provider", msg.Provider), slog.String("message_id", msg.MessageID))

	if msg.FromMe || msg.IsGroup || strings.TrimSpace(msg.Texto) == "" {
		return &domain.ResultadoWebhook{Status: domain.StatusIgnorada}, nil
	}

	candidates, err := domain.PhoneLookupCandidates(msg.Telefone)
	if err != nil {
		logger.InfoContext(ctx, "webhook: telefone inválido")
		return &domain.ResultadoWebhook{Status: domain.StatusIgnorada}, nil
	}

	user, err := s.users.FindByPhones(ctx, candidates)
	if errors.Is(err, domain.ErrNotFound) {
		// Não respondemos números desconhecidos (evita spam e enumeração).
		logger.InfoContext(ctx, "webhook: telefone não cadastrado")
		return &domain.ResultadoWebhook{Status: domain.StatusUsuarioNaoAchado}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("webhook: buscar usuário: %w", err)
	}
	logger = logger.With(slog.String("user_id", user.ID))
	// Responde exatamente no número que escreveu (o WhatsApp pode entregar
	// celulares sem o nono dígito; responder no formato cadastrado pode falhar).
	replyTo := candidates[0]

	// Verificação pelo próprio WhatsApp ("verificar 123456").
	if m := reComandoVerificar.FindStringSubmatch(msg.Texto); m != nil && s.verifier != nil {
		if user.TelefoneVerificado() {
			s.reply(ctx, logger, replyTo, "✅ Seu número já está verificado.")
			return &domain.ResultadoWebhook{Status: domain.StatusIgnorada}, nil
		}
		err := s.verifier.ConfirmFromWhatsApp(ctx, user.ID, m[1])
		switch {
		case err == nil:
			s.reply(ctx, logger, replyTo, "✅ Número verificado! Agora é só mandar: *Gastei 50 mercado*.")
			return &domain.ResultadoWebhook{Status: domain.StatusVerificacaoConfirmada}, nil
		case errors.Is(err, domain.ErrVerificationInvalid):
			s.reply(ctx, logger, replyTo, "❌ Código inválido ou expirado. Gere um novo código no app.")
			return &domain.ResultadoWebhook{Status: domain.StatusVerificacaoInvalida}, nil
		default:
			return nil, fmt.Errorf("webhook: verificar telefone: %w", err)
		}
	}

	// Números não verificados não podem lançar nem consultar dados.
	// (Desligável por configuração enquanto a verificação não estiver em uso.)
	if !user.TelefoneVerificado() && !s.skipVerif {
		logger.InfoContext(ctx, "webhook: telefone não verificado")
		s.reply(ctx, logger, replyTo, mensagemNaoVerificado)
		return &domain.ResultadoWebhook{Status: domain.StatusTelefoneNaoVerificado}, nil
	}

	if s.needEmail && !user.EmailVerificado() {
		logger.InfoContext(ctx, "webhook: e-mail não verificado")
		s.reply(ctx, logger, replyTo, "📧 Confirme seu e-mail no app do QuantoDeu antes de usar o WhatsApp.")
		return &domain.ResultadoWebhook{Status: domain.StatusEmailNaoVerificado}, nil
	}

	if reComandoSaldo.MatchString(msg.Texto) {
		return s.responderSaldo(ctx, logger, user, replyTo)
	}

	lanc, err := s.parser.Parse(msg.Texto)
	if err != nil {
		logger.InfoContext(ctx, "webhook: mensagem não interpretada")
		s.reply(ctx, logger, replyTo, mensagemAjuda)
		return &domain.ResultadoWebhook{Status: domain.StatusNaoInterpretada}, nil
	}

	// Usa o horário do provedor (entregas atrasadas caem no dia certo), mas
	// descarta timestamps no futuro ou antigos demais (relógio incorreto,
	// reprocessamento de fila).
	now := s.clock.Now()
	recebida := msg.RecebidaEm
	if recebida.IsZero() || recebida.After(now.Add(5*time.Minute)) || now.Sub(recebida) > MaxIdadeMensagem {
		recebida = now
	}
	externalID := ""
	if msg.MessageID != "" {
		externalID = msg.Provider + ":" + msg.MessageID
	}

	t, err := domain.NewTransacao(domain.NewTransacaoInput{
		UserID:     user.ID,
		Tipo:       lanc.Tipo,
		Valor:      lanc.Valor,
		Categoria:  lanc.Categoria,
		Descricao:  lanc.Descricao,
		Data:       recebida.In(s.loc),
		Origem:     domain.OrigemWhatsApp,
		ExternalID: externalID,
	})
	if err != nil {
		logger.InfoContext(ctx, "webhook: lançamento inválido", slog.Any("err", err))
		s.reply(ctx, logger, replyTo, "⚠️ Não consegui registrar: "+err.Error())
		return &domain.ResultadoWebhook{Status: domain.StatusNaoInterpretada}, nil
	}

	// O user_id vem do usuário identificado pelo telefone — nunca do payload.
	if err := s.transacoes.Create(ctx, user.ID, t); err != nil {
		if errors.Is(err, domain.ErrDuplicateMessage) {
			logger.InfoContext(ctx, "webhook: mensagem duplicada ignorada")
			return &domain.ResultadoWebhook{Status: domain.StatusDuplicada}, nil
		}
		return nil, fmt.Errorf("webhook: gravar transação: %w", err)
	}

	logger.InfoContext(ctx, "webhook: transação criada", slog.String("transacao_id", t.ID))

	emoji, rotulo := "🔴", "Saída"
	if t.Tipo == domain.TipoEntrada {
		emoji, rotulo = "🟢", "Entrada"
	}
	s.reply(ctx, logger, replyTo,
		fmt.Sprintf("%s %s de *%s* registrada em *%s*.", emoji, rotulo, t.Valor.BRL(), t.Categoria))

	return &domain.ResultadoWebhook{Status: domain.StatusCriada, Transacao: t}, nil
}

func (s *WebhookService) responderSaldo(ctx context.Context, logger *slog.Logger, user *domain.User, replyTo string) (*domain.ResultadoWebhook, error) {
	hoje := domain.DateOnly(s.clock.Now().In(s.loc))
	periodo := domain.PeriodoDe(hoje)
	saldo, err := s.transacoes.SaldoAte(ctx, user.ID, hoje.AddDate(0, 0, 1))
	if err != nil {
		return nil, fmt.Errorf("webhook: saldo: %w", err)
	}
	totais, err := s.transacoes.Totais(ctx, user.ID, periodo.Inicio, periodo.Fim)
	if err != nil {
		return nil, fmt.Errorf("webhook: totais: %w", err)
	}
	s.reply(ctx, logger, replyTo, fmt.Sprintf(
		"💰 *Saldo atual:* %s\n📅 %02d/%d\n🟢 Entradas: %s\n🔴 Saídas: %s",
		(user.SaldoInicial+saldo).BRL(), int(periodo.Mes), periodo.Ano,
		totais.Entradas.BRL(), totais.Saidas.BRL()))
	return &domain.ResultadoWebhook{Status: domain.StatusConsulta}, nil
}

// reply envia a resposta de forma assíncrona para não atrasar o ACK do
// webhook. Falhas de envio são apenas registradas.
func (s *WebhookService) reply(ctx context.Context, logger *slog.Logger, telefone, texto string) {
	if s.sender == nil {
		return
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(bg, 15*time.Second)
		defer cancel()
		if err := s.sender.SendText(ctx, telefone, texto); err != nil {
			logger.WarnContext(ctx, "webhook: falha ao responder no WhatsApp", slog.Any("err", err))
		}
	}()
}
