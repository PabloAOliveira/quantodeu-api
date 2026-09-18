// Comando api: ponto de entrada do QuantoDeu. Responsável APENAS por
// carregar configuração, instanciar adaptadores, injetar dependências
// (composition root) e controlar o ciclo de vida dos servidores.
package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	goredis "github.com/redis/go-redis/v9"

	"github.com/cgisoftware/quantodeu-api/config"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/clock"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/db/postgres"
	redisstore "github.com/cgisoftware/quantodeu-api/internal/adapters/db/redis"
	emailadapter "github.com/cgisoftware/quantodeu-api/internal/adapters/email"
	httpadapter "github.com/cgisoftware/quantodeu-api/internal/adapters/http"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/handlers"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/session"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/observability"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/security"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/whatsapp"
	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
	"github.com/cgisoftware/quantodeu-api/internal/core/services"
)

func main() {
	if err := run(); err != nil {
		slog.Error("aplicação encerrada com erro", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	destinoTeste := flag.String("test-email", "",
		"envia um e-mail de teste para o endereço informado, com a configuração atual, e sai (não sobe a API)")
	checarConfig := flag.Bool("check-config", false,
		"valida a configuração, imprime um resumo (sem segredos) e sai — use antes de subir uma versão")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuração inválida:\n%w", err)
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	// Conferir SMTP de produção sem precisar cadastrar um usuário e esperar o
	// código: `quantodeu-api -test-email=voce@dominio.com`.
	if *checarConfig {
		resumirConfig(cfg, log)
		return nil
	}
	if *destinoTeste != "" {
		return enviarEmailDeTeste(cfg, log, *destinoTeste)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---------------------------------------------------------------------
	// Observabilidade
	// ---------------------------------------------------------------------
	shutdownTracing, err := observability.SetupTracing(ctx, observability.TracingConfig{
		Enabled: cfg.Observ.TracingEnabled, ServiceName: "quantodeu-api", Version: cfg.Version,
		Environment: cfg.Env, Endpoint: cfg.Observ.TracingEndpoint, SampleRatio: cfg.Observ.TracingRatio,
	})
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(c)
	}()
	metrics := observability.NewPrometheus(cfg.Version)

	// ---------------------------------------------------------------------
	// PostgreSQL: migrations com o DONO das tabelas; app com role sujeita a RLS
	// ---------------------------------------------------------------------
	if cfg.DB.RunMigrations {
		migURL := cfg.DB.MigrationURL
		if migURL == "" {
			migURL = cfg.DB.URL
		}
		migPool, err := postgres.NewPool(ctx, postgres.PoolConfig{URL: migURL, MaxConns: 2})
		if err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
		err = postgres.Migrate(ctx, migPool, log)
		migPool.Close()
		if err != nil {
			return err
		}
	}

	var tracer pgx.QueryTracer
	if cfg.Observ.TracingEnabled {
		tracer = otelpgx.NewTracer()
	}
	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		URL: cfg.DB.URL, MaxConns: cfg.DB.MaxConns, MinConns: cfg.DB.MinConns,
		MaxConnLifetime: cfg.DB.MaxConnLifetime, Tracer: tracer,
	})
	if err != nil {
		return err
	}
	defer pool.Close()
	metrics.RegisterPool(pool)

	role, err := postgres.InspectRole(ctx, pool)
	if err != nil {
		return fmt.Errorf("inspecionar role do banco: %w", err)
	}
	if !role.RLSEffective() {
		msg := "a role do DATABASE_URL ignora Row-Level Security (superuser, BYPASSRLS ou dona das tabelas); use um usuário membro de quantodeu_app"
		if cfg.DB.RequireRLS {
			return fmt.Errorf("%s (role=%s)", msg, role.Role)
		}
		log.Warn(msg, slog.String("role", role.Role))
	} else {
		log.Info("RLS ativo para a role da aplicação", slog.String("role", role.Role))
	}

	userRepo := postgres.NewUserRepository(pool)
	transacaoRepo := postgres.NewTransacaoRepository(pool)
	parcelamentoRepo := postgres.NewParcelamentoRepository(pool)
	verificationStore := postgres.NewPhoneVerificationStore(pool)
	pgAttempts := postgres.NewLoginAttemptStore(pool)

	healthChecks := map[string]handlers.HealthCheck{"postgres": {Ping: pool.Ping, Critical: true}}

	// ---------------------------------------------------------------------
	// Redis (sessões, rate limit e/ou tentativas de login)
	// ---------------------------------------------------------------------
	var rdb *goredis.Client
	if cfg.UsesRedis() {
		opts := &goredis.Options{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB}
		if cfg.Redis.TLS {
			opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		rdb = goredis.NewClient(opts)
		defer rdb.Close()
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		healthChecks["redis"] = handlers.HealthCheck{
			Ping: func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
			// Só sessões no Redis são críticas: rate limit e bloqueio de login
			// degradam de forma segura (fail-open com log).
			Critical: cfg.Session.Store == "redis",
		}
	}

	var sessionStore ports.SessionStore = postgres.NewSessionStore(pool)
	if cfg.Session.Store == "redis" {
		sessionStore = redisstore.NewSessionStore(rdb)
	}
	var attempts ports.LoginAttemptStore = pgAttempts
	if cfg.Security.LoginAttemptsStore == "redis" {
		attempts = redisstore.NewLoginAttemptStore(rdb)
	}
	sec := cfg.Security
	limiters := httpadapter.Limiters{
		API:     middlewares.NewMemoryRateLimiter(ctx, sec.RateLimitRPS, sec.RateLimitBurst),
		Auth:    middlewares.NewMemoryRateLimiter(ctx, float64(sec.AuthRateLimitRPM)/60, sec.AuthRateLimitRPM),
		Webhook: middlewares.NewMemoryRateLimiter(ctx, sec.WebhookRPS, sec.WebhookBurst),
	}
	if sec.RateLimitStore == "redis" {
		limiters = httpadapter.Limiters{
			API:     redisstore.NewRateLimiter(rdb, "api", sec.RateLimitRPS, sec.RateLimitBurst),
			Auth:    redisstore.NewRateLimiter(rdb, "auth", float64(sec.AuthRateLimitRPM)/60, sec.AuthRateLimitRPM),
			Webhook: redisstore.NewRateLimiter(rdb, "webhook", sec.WebhookRPS, sec.WebhookBurst),
		}
	}

	// ---------------------------------------------------------------------
	// Segurança e WhatsApp
	// ---------------------------------------------------------------------
	hasher := security.NewArgon2idHasher(security.Argon2Params{
		Memory: cfg.Argon2.MemoryKiB, Iterations: cfg.Argon2.Iterations, Parallelism: cfg.Argon2.Parallelism,
	})
	signer, err := security.NewCookieSigner(cfg.Session.Secrets)
	if err != nil {
		return err
	}
	codes, err := security.NewHMACCodeManager(sec.VerificationPepper)
	if err != nil {
		return err
	}
	sysClock := clock.System{}
	converter, sender := buildWhatsApp(cfg, log)
	emailSender, err := buildEmailSender(cfg, log)
	if err != nil {
		return err
	}

	// ---------------------------------------------------------------------
	// Núcleo (casos de uso)
	// ---------------------------------------------------------------------
	emailVerifier := services.NewEmailVerificationService(services.EmailVerificationDeps{
		Users: userRepo, Store: postgres.NewEmailVerificationStore(pool), Codes: codes, Sender: emailSender,
		Clock: sysClock, Metrics: metrics, AppName: cfg.Email.AppName, Log: log,
	}, domain.DefaultEmailVerificationPolicy())
	authSvc, err := services.NewAuthService(services.AuthDeps{
		Users: userRepo, Sessions: sessionStore, Attempts: attempts, Hasher: hasher,
		Tokens: security.SessionTokenManager{}, Clock: sysClock, Metrics: metrics, Log: log,
		EmailVerification: emailVerifier,
	}, services.AuthConfig{
		SessionTTL: cfg.Session.TTL, SessionIdleTimeout: cfg.Session.IdleTimeout,
		Lockout: domain.LoginLockoutPolicy{
			FreeAttempts: sec.LockoutFree, BaseLock: sec.LockoutBase, MaxLock: sec.LockoutMax, Window: sec.LockoutWindow,
		},
	})
	if err != nil {
		return err
	}
	fin := services.NewFinancasServices(userRepo, transacaoRepo, parcelamentoRepo, sysClock, metrics, cfg.Timezone, log)
	verifier := services.NewPhoneVerificationService(services.PhoneVerificationDeps{
		Users: userRepo, Store: verificationStore, Codes: codes, Sender: sender,
		SenderEnabled: cfg.WhatsApp.ReplyMode != "off", Clock: sysClock, Metrics: metrics, Log: log,
	}, domain.DefaultPhoneVerificationPolicy())
	if !cfg.WhatsApp.RequireVerifiedPhone {
		log.Warn("verificação de telefone DESLIGADA: qualquer mensagem de um número cadastrado lança transações",
			slog.Bool("production", cfg.IsProduction()))
	}
	webhookSvc := services.NewWebhookService(services.WebhookDeps{
		Users: userRepo, Transacoes: transacaoRepo, Parser: services.NewRegexParserService(), Sender: sender,
		Verifier: verifier, SkipPhoneVerification: !cfg.WhatsApp.RequireVerifiedPhone, RequireVerifiedEmail: cfg.Email.VerificationRequired, Clock: sysClock, Metrics: metrics, Location: cfg.Timezone, Log: log,
	})

	// ---------------------------------------------------------------------
	// Adaptadores primários (driving)
	// ---------------------------------------------------------------------
	cookies := session.NewCookieManager(cfg.Session.CookieName, signer)
	router, _, err := httpadapter.NewRouter(httpadapter.RouterDeps{
		Production:           cfg.IsProduction(),
		WhatsAppBot:          cfg.WhatsApp.BotEnabled,
		Version:              cfg.Version,
		AllowedOrigins:       cfg.HTTP.AllowedOrigins,
		TrustedProxies:       cfg.HTTP.TrustedProxies,
		MaxBodyBytes:         cfg.HTTP.MaxBodyBytes,
		DocsEnabled:          cfg.HTTP.DocsEnabled,
		TracingEnabled:       cfg.Observ.TracingEnabled,
		Limiters:             limiters,
		Metrics:              metrics,
		Cookies:              cookies,
		Log:                  log,
		Auth:                 authSvc,
		EmailVerification:    emailVerifier,
		RequireVerifiedEmail: cfg.Email.VerificationRequired,
		AuthHandler:          handlers.NewAuthHandler(authSvc, fin.Resumo, verifier, emailVerifier, cookies, log),
		Financas:             handlers.NewFinancasHandler(fin.Transacoes, fin.Resumo, fin.Parcelamentos, log),
		Webhook:              handlers.NewWebhookHandler(converter, webhookSvc, log),
		Health:               handlers.NewHealthHandler(healthChecks),
	})
	if err != nil {
		return err
	}

	go cleanupJob(ctx, cfg.Session.CleanupPeriod, log, func(ctx context.Context, now time.Time) {
		if n, err := sessionStore.DeleteExpired(ctx, now); err != nil {
			log.Warn("limpeza de sessões falhou", slog.Any("err", err))
		} else if n > 0 {
			log.Info("sessões expiradas removidas", slog.Int64("total", n))
		}
		if n, err := pgAttempts.DeleteStale(ctx, now.Add(-sec.LockoutWindow)); err != nil {
			log.Warn("limpeza de tentativas de login falhou", slog.Any("err", err))
		} else if n > 0 {
			log.Info("tentativas de login antigas removidas", slog.Int64("total", n))
		}
	})

	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	servers := []*http.Server{srv}

	if cfg.Observ.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metricsAuth(cfg.Observ.MetricsToken, metrics.Handler()))
		servers = append(servers, &http.Server{
			Addr: cfg.Observ.MetricsAddr, Handler: mux,
			ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second,
		})
	}

	errCh := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *http.Server) {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s: %w", s.Addr, err)
			}
		}(s)
	}
	log.Info("QuantoDeu API iniciada",
		slog.String("addr", cfg.HTTP.Addr), slog.String("metrics", cfg.Observ.MetricsAddr),
		slog.Bool("docs", cfg.HTTP.DocsEnabled), slog.String("env", cfg.Env), slog.String("version", cfg.Version),
		slog.String("session_store", cfg.Session.Store), slog.String("rate_limit_store", sec.RateLimitStore),
		slog.String("login_attempts_store", sec.LoginAttemptsStore),
		slog.String("whatsapp_provider", converter.Name()), slog.String("whatsapp_reply", cfg.WhatsApp.ReplyMode),
		slog.String("email_provider", cfg.Email.Provider), slog.Bool("email_verificacao_obrigatoria", cfg.Email.VerificationRequired),
		slog.Bool("tracing", cfg.Observ.TracingEnabled))

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("encerrando servidores (graceful shutdown)")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown %s: %w", s.Addr, err)
		}
	}
	log.Info("servidores encerrados")
	return nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if !cfg.IsProduction() && strings.EqualFold(os.Getenv("LOG_FORMAT"), "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h).With(slog.String("service", "quantodeu-api"))
}

func buildWhatsApp(cfg *config.Config, log *slog.Logger) (whatsapp.Converter, ports.WhatsAppSender) {
	wa := cfg.WhatsApp
	client := &http.Client{Timeout: 10 * time.Second}
	var conv whatsapp.Converter
	var provider ports.WhatsAppSender

	switch wa.Provider {
	case "zapi":
		conv = &whatsapp.ZAPIConverter{Secret: wa.WebhookSecret}
		provider = &whatsapp.ZAPISender{BaseURL: wa.ZAPIBaseURL, InstanceID: wa.ZAPIInstanceID, Token: wa.ZAPIToken, ClientToken: wa.ZAPIClientToken, Client: client}
	case "twilio":
		conv = &whatsapp.TwilioConverter{AuthToken: wa.TwilioAuthToken, WebhookURL: wa.TwilioWebhookURL}
		provider = &whatsapp.TwilioSender{AccountSID: wa.TwilioAccountSID, AuthToken: wa.TwilioAuthToken, From: wa.TwilioFrom, Client: client}
	default:
		conv = &whatsapp.EvolutionConverter{Secret: wa.WebhookSecret}
		provider = &whatsapp.EvolutionSender{BaseURL: wa.EvolutionBaseURL, APIKey: wa.EvolutionAPIKey, Instance: wa.EvolutionInstance, Client: client}
	}

	switch wa.ReplyMode {
	case "provider":
		return conv, provider
	case "log":
		return conv, whatsapp.LogSender{Log: log}
	default:
		return conv, whatsapp.NoopSender{}
	}
}

func metricsAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func cleanupJob(ctx context.Context, every time.Duration, log *slog.Logger, fn func(context.Context, time.Time)) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Error("panic no job de limpeza", slog.Any("panic", r))
					}
				}()
				fn(ctx, now)
			}()
		}
	}
}

// buildEmailSender escolhe o envio de e-mails: SMTP real ou log (DEV).
func buildEmailSender(cfg *config.Config, log *slog.Logger) (ports.EmailSender, error) {
	if cfg.Email.Provider == "log" {
		log.Warn("EMAIL_PROVIDER=log: e-mails (e códigos) só aparecem no log — use apenas em desenvolvimento")
		return emailadapter.LogSender{Log: log}, nil
	}
	// O Gmail só deixa enviar como a conta autenticada ou como um alias
	// confirmado nela ("Enviar e-mail como"). Com um From de fora disso ele
	// reescreve o remetente em silêncio — ou recusa com 5.7.0.
	if cfg.Email.SMTPUsername != "" && !mesmoEndereco(cfg.Email.SMTPFrom, cfg.Email.SMTPUsername) {
		log.Warn("SMTP_FROM diferente de SMTP_USERNAME: no Gmail isso só funciona se o endereço for um alias confirmado em \"Enviar e-mail como\"",
			slog.String("from", cfg.Email.SMTPFrom), slog.String("username", cfg.Email.SMTPUsername))
	}
	return emailadapter.NewSMTPSender(emailadapter.SMTPConfig{
		Host: cfg.Email.SMTPHost, Port: cfg.Email.SMTPPort,
		Username: cfg.Email.SMTPUsername, Password: cfg.Email.SMTPPassword,
		From: cfg.Email.SMTPFrom, TLSMode: cfg.Email.SMTPTLS,
	})
}

// mesmoEndereco compara o e-mail de "Nome <a@b>" com um endereço cru.
func mesmoEndereco(from, username string) bool {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return false
	}
	return strings.EqualFold(addr.Address, strings.TrimSpace(username))
}

// enviarEmailDeTeste manda uma mensagem pelo remetente configurado e devolve o
// erro do provedor sem enfeite — é ele que diz se a senha de app está certa, se
// o remetente foi recusado ou se a porta está bloqueada pelo firewall.
func enviarEmailDeTeste(cfg *config.Config, log *slog.Logger, destino string) error {
	if _, err := mail.ParseAddress(destino); err != nil {
		return fmt.Errorf("-test-email: endereço inválido: %w", err)
	}
	sender, err := buildEmailSender(cfg, log)
	if err != nil {
		return err
	}
	log.Info("enviando e-mail de teste",
		slog.String("para", destino), slog.String("provider", cfg.Email.Provider),
		slog.String("host", cfg.Email.SMTPHost), slog.Int("porta", cfg.Email.SMTPPort),
		slog.String("tls", cfg.Email.SMTPTLS), slog.String("from", cfg.Email.SMTPFrom))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agora := time.Now().In(cfg.Timezone).Format("02/01/2006 15:04:05")
	err = sender.Send(ctx, domain.EmailMessage{
		To:      destino,
		Subject: fmt.Sprintf("Teste de envio do %s", cfg.Email.AppName),
		Text: fmt.Sprintf("Se você está lendo isto, o envio de e-mail do %s está funcionando.\n\n"+
			"Ambiente: %s\nServidor: %s:%d (%s)\nRemetente: %s\nEnviado em: %s\n",
			cfg.Email.AppName, cfg.Env, cfg.Email.SMTPHost, cfg.Email.SMTPPort, cfg.Email.SMTPTLS, cfg.Email.SMTPFrom, agora),
	})
	if err != nil {
		return fmt.Errorf("falha no envio: %w", err)
	}
	log.Info("e-mail de teste enviado — confira a caixa de entrada (e o spam)")
	return nil
}

// resumirConfig imprime o que a API vai usar e aponta o que costuma passar
// despercebido num deploy. O que é erro de verdade já barrou em config.Load();
// aqui ficam os "isto sobe, mas provavelmente não é o que você queria".
func resumirConfig(cfg *config.Config, log *slog.Logger) {
	senha := "não definida"
	if n := len(strings.ReplaceAll(cfg.Email.SMTPPassword, " ", "")); n > 0 {
		senha = fmt.Sprintf("definida (%d caracteres)", n)
	}
	log.Info("configuração válida",
		slog.String("ambiente", cfg.Env),
		slog.String("http", cfg.HTTP.Addr),
		slog.Bool("docs_publicos", cfg.HTTP.DocsEnabled),
		slog.String("cookie", cfg.Session.CookieName),
		slog.String("sessao_ttl", cfg.Session.TTL.String()),
		slog.String("sessao_inatividade", cfg.Session.IdleTimeout.String()),
		slog.String("sessao_store", cfg.Session.Store),
		slog.Any("cors", cfg.HTTP.AllowedOrigins),
		slog.Any("proxies_confiaveis", cfg.HTTP.TrustedProxies),
		slog.String("metrics", cfg.Observ.MetricsAddr),
		slog.String("email_provider", cfg.Email.Provider),
		slog.String("smtp", fmt.Sprintf("%s:%d (%s)", cfg.Email.SMTPHost, cfg.Email.SMTPPort, cfg.Email.SMTPTLS)),
		slog.String("smtp_from", cfg.Email.SMTPFrom),
		slog.String("smtp_username", cfg.Email.SMTPUsername),
		slog.String("smtp_password", senha),
		slog.Bool("whatsapp_bot", cfg.WhatsApp.BotEnabled),
	)

	var avisos []string
	if cfg.Email.SMTPUsername != "" && !mesmoEndereco(cfg.Email.SMTPFrom, cfg.Email.SMTPUsername) {
		avisos = append(avisos, fmt.Sprintf(
			"SMTP_FROM (%s) não é a conta autenticada (%s): no Gmail o remetente só passa se for um alias confirmado em \"Enviar e-mail como\" — senão ele reescreve em silêncio",
			cfg.Email.SMTPFrom, cfg.Email.SMTPUsername))
	}
	if cfg.IsProduction() && cfg.HTTP.DocsEnabled {
		avisos = append(avisos, "DOCS_ENABLED=true em produção: o Swagger fica público em /docs")
	}
	if cfg.IsProduction() && len(cfg.HTTP.TrustedProxies) == 0 {
		avisos = append(avisos, "TRUSTED_PROXIES vazio: atrás de um proxy/CDN todo mundo vira o mesmo IP, e o rate limit e o bloqueio de login passam a valer para todos juntos")
	}
	if cfg.Session.IdleTimeout > 0 && cfg.Session.IdleTimeout >= cfg.Session.TTL {
		avisos = append(avisos, "SESSION_IDLE_TIMEOUT >= SESSION_TTL: a expiração por inatividade nunca vai acontecer")
	}
	if !cfg.IsProduction() {
		avisos = append(avisos, "APP_ENV não é 'production': as travas de produção (cookie __Host-, docs, CORS, métricas) não estão sendo aplicadas")
	}
	for _, a := range avisos {
		log.Warn(a)
	}
	if len(avisos) == 0 {
		log.Info("nenhum alerta")
	}
}
