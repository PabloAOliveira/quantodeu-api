// Package config carrega a configuração a partir de variáveis de ambiente
// (e, opcionalmente, de um arquivo .env em desenvolvimento).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config agrupa toda a configuração da aplicação.
type Config struct {
	Env      string // development | production
	Version  string
	LogLevel string
	HTTP     HTTPConfig
	DB       DBConfig
	Session  SessionConfig
	Redis    RedisConfig
	Security SecurityConfig
	Argon2   Argon2Config
	WhatsApp WhatsAppConfig
	Email    EmailConfig
	Observ   ObservabilityConfig
	Timezone *time.Location
}

// HTTPConfig configura o servidor HTTP.
type HTTPConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxBodyBytes      int64
	AllowedOrigins    []string
	TrustedProxies    []string
	DocsEnabled       bool
}

// DBConfig configura o PostgreSQL.
type DBConfig struct {
	URL             string // role da aplicação (sujeita a RLS)
	MigrationURL    string // dono das tabelas (vazio = usa URL)
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	RunMigrations   bool
	RequireRLS      bool // recusa subir se a role ignorar RLS
}

// SessionConfig configura sessões e cookie.
type SessionConfig struct {
	Store         string // postgres | redis
	CookieName    string
	TTL           time.Duration
	IdleTimeout   time.Duration
	Secrets       [][]byte // o primeiro assina; os demais só verificam (rotação)
	CleanupPeriod time.Duration
}

// RedisConfig configura o Redis.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	TLS      bool
}

// SecurityConfig agrupa rate limit, bloqueio de login e verificação.
type SecurityConfig struct {
	RateLimitStore     string // memory | redis
	RateLimitRPS       float64
	RateLimitBurst     int
	AuthRateLimitRPM   int
	WebhookRPS         float64
	WebhookBurst       int
	LoginAttemptsStore string // postgres | redis
	LockoutFree        int
	LockoutBase        time.Duration
	LockoutMax         time.Duration
	LockoutWindow      time.Duration
	VerificationPepper []byte
}

// Argon2Config parametriza o Argon2id.
type Argon2Config struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// EmailConfig configura a verificação de e-mail e o envio (SMTP).
type EmailConfig struct {
	VerificationRequired bool   // exige e-mail confirmado para usar a API financeira
	Provider             string // smtp | log
	AppName              string
	SMTPHost             string
	SMTPPort             int
	SMTPUsername         string
	SMTPPassword         string
	SMTPFrom             string
	SMTPTLS              string // starttls | tls | none
}

// WhatsAppConfig configura o provedor de WhatsApp.
type WhatsAppConfig struct {
	// BotEnabled liga o bot: webhook de mensagens e verificação de telefone.
	// Desligado (padrão) o v1 sobe sem provedor nenhum — as rotas de WhatsApp
	// simplesmente não existem, em vez de prometer um bot que não está no ar.
	BotEnabled bool

	Provider      string // evolution | zapi | twilio
	WebhookSecret string // token compartilhado exigido no webhook (evolution/zapi)
	ReplyMode     string // off | log | provider
	// RequireVerifiedPhone exige telefone verificado para lançar pelo WhatsApp.
	RequireVerifiedPhone bool

	EvolutionBaseURL  string
	EvolutionAPIKey   string
	EvolutionInstance string

	ZAPIBaseURL     string
	ZAPIInstanceID  string
	ZAPIToken       string
	ZAPIClientToken string

	TwilioAccountSID string
	TwilioAuthToken  string
	TwilioFrom       string // ex.: whatsapp:+14155238886
	TwilioWebhookURL string // URL pública exata configurada no Twilio (validação de assinatura)
}

// ObservabilityConfig configura métricas e tracing.
type ObservabilityConfig struct {
	MetricsAddr     string // vazio = desativado
	MetricsToken    string // Bearer opcional para /metrics
	TracingEnabled  bool
	TracingEndpoint string
	TracingRatio    float64
}

// IsProduction informa se o ambiente é produção.
func (c *Config) IsProduction() bool { return c.Env == "production" }

// UsesRedis informa se algum componente depende do Redis.
func (c *Config) UsesRedis() bool {
	return c.Session.Store == "redis" || c.Security.RateLimitStore == "redis" || c.Security.LoginAttemptsStore == "redis"
}

// Load lê as variáveis de ambiente e valida a configuração.
func Load() (*Config, error) {
	loadDotEnv(".env")

	var errs []error
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok {
			v = strings.TrimSpace(v)
			// Ignora comentários que alguns carregadores de .env (ex.: docker
			// compose com valor vazio) repassam como parte do valor.
			if strings.HasPrefix(v, "#") {
				v = ""
			} else if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			if v != "" {
				return v
			}
		}
		return def
	}
	dur := func(key string, def time.Duration) time.Duration {
		v := get(key, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return d
	}
	integer := func(key string, def int) int {
		v := get(key, "")
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return n
	}
	float := func(key string, def float64) float64 {
		v := get(key, "")
		if v == "" {
			return def
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return n
	}
	boolean := func(key string, def bool) bool {
		v := get(key, "")
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return b
	}
	list := func(key, def string) []string {
		var out []string
		for _, p := range strings.Split(get(key, def), ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	env := get("APP_ENV", "development")
	prod := env == "production"

	defaultReply := "log"
	if prod {
		defaultReply = "off"
	}
	replyMode := strings.ToLower(get("WHATSAPP_REPLY_MODE", defaultReply))
	if boolean("WHATSAPP_REPLY_ENABLED", false) { // compatibilidade com a v1
		replyMode = "provider"
	}

	cfg := &Config{
		Env:      env,
		Version:  get("APP_VERSION", "1.2.0"),
		LogLevel: get("LOG_LEVEL", "info"),
		HTTP: HTTPConfig{
			Addr:              get("HTTP_ADDR", ":8080"),
			ReadHeaderTimeout: dur("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       dur("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      dur("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:       dur("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout:   dur("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
			MaxBodyBytes:      int64(integer("HTTP_MAX_BODY_BYTES", 1<<20)),
			AllowedOrigins:    list("CORS_ALLOWED_ORIGINS", "http://localhost:3000"),
			TrustedProxies:    list("TRUSTED_PROXIES", ""),
			DocsEnabled:       boolean("DOCS_ENABLED", !prod),
		},
		DB: DBConfig{
			URL:             get("DATABASE_URL", ""),
			MigrationURL:    get("DATABASE_MIGRATION_URL", ""),
			MaxConns:        int32(integer("DB_MAX_CONNS", 20)),
			MinConns:        int32(integer("DB_MIN_CONNS", 2)),
			MaxConnLifetime: dur("DB_MAX_CONN_LIFETIME", time.Hour),
			RunMigrations:   boolean("DB_RUN_MIGRATIONS", true),
			RequireRLS:      boolean("DB_REQUIRE_RLS", prod),
		},
		Session: SessionConfig{
			Store:         strings.ToLower(get("SESSION_STORE", "postgres")),
			CookieName:    get("SESSION_COOKIE_NAME", "__Host-quantodeu_session"),
			TTL:           dur("SESSION_TTL", 7*24*time.Hour),
			IdleTimeout:   dur("SESSION_IDLE_TIMEOUT", 72*time.Hour),
			CleanupPeriod: dur("SESSION_CLEANUP_PERIOD", time.Hour),
		},
		Redis: RedisConfig{
			Addr:     get("REDIS_ADDR", "localhost:6379"),
			Password: get("REDIS_PASSWORD", ""),
			DB:       integer("REDIS_DB", 0),
			TLS:      boolean("REDIS_TLS", false),
		},
		Security: SecurityConfig{
			RateLimitStore:     strings.ToLower(get("RATE_LIMIT_STORE", "memory")),
			RateLimitRPS:       float("RATE_LIMIT_RPS", 10),
			RateLimitBurst:     integer("RATE_LIMIT_BURST", 40),
			AuthRateLimitRPM:   integer("AUTH_RATE_LIMIT_PER_MINUTE", 10),
			WebhookRPS:         float("WEBHOOK_RATE_LIMIT_RPS", 50),
			WebhookBurst:       integer("WEBHOOK_RATE_LIMIT_BURST", 200),
			LoginAttemptsStore: strings.ToLower(get("LOGIN_ATTEMPTS_STORE", "postgres")),
			LockoutFree:        integer("LOGIN_LOCKOUT_FREE_ATTEMPTS", 5),
			LockoutBase:        dur("LOGIN_LOCKOUT_BASE", time.Minute),
			LockoutMax:         dur("LOGIN_LOCKOUT_MAX", 30*time.Minute),
			LockoutWindow:      dur("LOGIN_LOCKOUT_WINDOW", 24*time.Hour),
		},
		Argon2: Argon2Config{
			MemoryKiB:   uint32(integer("ARGON2_MEMORY_KIB", 64*1024)),
			Iterations:  uint32(integer("ARGON2_ITERATIONS", 3)),
			Parallelism: uint8(integer("ARGON2_PARALLELISM", 2)),
		},
		WhatsApp: WhatsAppConfig{
			BotEnabled:           boolean("WHATSAPP_BOT_ENABLED", false),
			Provider:             strings.ToLower(get("WHATSAPP_PROVIDER", "evolution")),
			WebhookSecret:        get("WHATSAPP_WEBHOOK_SECRET", ""),
			ReplyMode:            replyMode,
			RequireVerifiedPhone: boolean("WHATSAPP_REQUIRE_VERIFIED_PHONE", true),
			EvolutionBaseURL:     get("EVOLUTION_BASE_URL", ""),
			EvolutionAPIKey:      get("EVOLUTION_API_KEY", ""),
			EvolutionInstance:    get("EVOLUTION_INSTANCE", ""),
			ZAPIBaseURL:          get("ZAPI_BASE_URL", "https://api.z-api.io"),
			ZAPIInstanceID:       get("ZAPI_INSTANCE_ID", ""),
			ZAPIToken:            get("ZAPI_TOKEN", ""),
			ZAPIClientToken:      get("ZAPI_CLIENT_TOKEN", ""),
			TwilioAccountSID:     get("TWILIO_ACCOUNT_SID", ""),
			TwilioAuthToken:      get("TWILIO_AUTH_TOKEN", ""),
			TwilioFrom:           get("TWILIO_WHATSAPP_FROM", ""),
			TwilioWebhookURL:     get("TWILIO_WEBHOOK_URL", ""),
		},
		Email: EmailConfig{
			VerificationRequired: boolean("EMAIL_VERIFICATION_REQUIRED", true),
			Provider:             strings.ToLower(get("EMAIL_PROVIDER", "log")),
			AppName:              get("APP_NAME", "QuantoDeu"),
			SMTPHost:             get("SMTP_HOST", ""),
			SMTPPort:             integer("SMTP_PORT", 587),
			SMTPUsername:         get("SMTP_USERNAME", ""),
			SMTPPassword:         get("SMTP_PASSWORD", ""),
			SMTPFrom:             get("SMTP_FROM", ""),
			SMTPTLS:              strings.ToLower(get("SMTP_TLS", "starttls")),
		},
		Observ: ObservabilityConfig{
			MetricsAddr:     get("METRICS_ADDR", ":9091"),
			MetricsToken:    get("METRICS_TOKEN", ""),
			TracingEnabled:  boolean("OTEL_ENABLED", false),
			TracingEndpoint: get("OTEL_ENDPOINT", ""),
			TracingRatio:    float("OTEL_SAMPLE_RATIO", 1),
		},
	}
	if strings.EqualFold(get("METRICS_ADDR", ":9091"), "off") {
		cfg.Observ.MetricsAddr = ""
	}

	loc, err := time.LoadLocation(get("APP_TIMEZONE", "America/Sao_Paulo"))
	if err != nil {
		errs = append(errs, fmt.Errorf("APP_TIMEZONE: %w", err))
		loc = time.UTC
	}
	cfg.Timezone = loc

	for _, s := range list("SESSION_SECRETS", "") {
		cfg.Session.Secrets = append(cfg.Session.Secrets, []byte(s))
	}
	if p := get("VERIFICATION_PEPPER", ""); p != "" {
		cfg.Security.VerificationPepper = []byte(p)
	} else if len(cfg.Session.Secrets) > 0 {
		cfg.Security.VerificationPepper = cfg.Session.Secrets[0]
	}

	errs = append(errs, cfg.validate()...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func oneOf(v string, opts ...string) bool {
	for _, o := range opts {
		if v == o {
			return true
		}
	}
	return false
}

// isGmailSMTP reconhece o SMTP do Gmail e do Google Workspace.
func isGmailSMTP(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "smtp.gmail.com" || h == "smtp-relay.gmail.com" || h == "smtp.googlemail.com"
}

func (c *Config) validate() []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.DB.URL == "" {
		add("DATABASE_URL é obrigatório")
	} else if u, err := url.Parse(c.DB.URL); err != nil || u.Scheme == "" {
		add("DATABASE_URL inválida")
	}
	if len(c.Session.Secrets) == 0 {
		add("SESSION_SECRETS é obrigatório (mínimo 32 caracteres)")
	}
	for i, s := range c.Session.Secrets {
		if len(s) < 32 {
			add("SESSION_SECRETS[%d] deve ter ao menos 32 caracteres", i)
		}
	}
	if len(c.Security.VerificationPepper) > 0 && len(c.Security.VerificationPepper) < 32 {
		add("VERIFICATION_PEPPER deve ter ao menos 32 caracteres")
	}
	if c.Session.TTL <= 0 {
		add("SESSION_TTL deve ser positivo")
	}
	if !oneOf(c.Session.Store, "postgres", "redis") {
		add("SESSION_STORE deve ser 'postgres' ou 'redis'")
	}
	if !oneOf(c.Security.RateLimitStore, "memory", "redis") {
		add("RATE_LIMIT_STORE deve ser 'memory' ou 'redis'")
	}
	if !oneOf(c.Security.LoginAttemptsStore, "postgres", "redis") {
		add("LOGIN_ATTEMPTS_STORE deve ser 'postgres' ou 'redis'")
	}
	if c.Security.RateLimitRPS <= 0 || c.Security.RateLimitBurst <= 0 || c.Security.AuthRateLimitRPM <= 0 {
		add("limites de rate limit devem ser positivos")
	}
	if c.Security.LockoutFree < 1 || c.Security.LockoutBase <= 0 || c.Security.LockoutMax < c.Security.LockoutBase || c.Security.LockoutWindow <= 0 {
		add("parâmetros LOGIN_LOCKOUT_* inválidos")
	}
	if !strings.HasPrefix(c.Session.CookieName, "__Host-") && c.IsProduction() {
		add("em produção SESSION_COOKIE_NAME deve usar o prefixo __Host-")
	}
	if c.Argon2.MemoryKiB < 19*1024 || c.Argon2.Iterations < 2 || c.Argon2.Parallelism < 1 {
		add("parâmetros Argon2id abaixo do mínimo recomendado pela OWASP (19 MiB, t=2, p=1)")
	}
	if !oneOf(c.WhatsApp.ReplyMode, "off", "log", "provider") {
		add("WHATSAPP_REPLY_MODE deve ser 'off', 'log' ou 'provider'")
	}
	if c.WhatsApp.ReplyMode == "log" && c.IsProduction() {
		add("WHATSAPP_REPLY_MODE=log é proibido em produção (grava códigos de verificação no log)")
	}
	// Sem bot não há webhook nem verificação de telefone: cobrar credenciais de
	// provedor aqui só travaria o deploy de quem não usa WhatsApp.
	if c.WhatsApp.BotEnabled {
		switch c.WhatsApp.Provider {
		case "evolution", "zapi":
			if c.WhatsApp.WebhookSecret == "" && c.IsProduction() {
				add("WHATSAPP_WEBHOOK_SECRET é obrigatório em produção")
			}
		case "twilio":
			if c.WhatsApp.TwilioAuthToken == "" || c.WhatsApp.TwilioWebhookURL == "" {
				add("TWILIO_AUTH_TOKEN e TWILIO_WEBHOOK_URL são obrigatórios para o provedor twilio")
			}
		default:
			add("WHATSAPP_PROVIDER deve ser 'evolution', 'zapi' ou 'twilio'")
		}
	}
	if c.WhatsApp.BotEnabled && c.WhatsApp.ReplyMode == "provider" {
		switch c.WhatsApp.Provider {
		case "evolution":
			if c.WhatsApp.EvolutionBaseURL == "" || c.WhatsApp.EvolutionAPIKey == "" || c.WhatsApp.EvolutionInstance == "" {
				add("EVOLUTION_BASE_URL, EVOLUTION_API_KEY e EVOLUTION_INSTANCE são obrigatórios com WHATSAPP_REPLY_MODE=provider")
			}
		case "zapi":
			if c.WhatsApp.ZAPIInstanceID == "" || c.WhatsApp.ZAPIToken == "" {
				add("ZAPI_INSTANCE_ID e ZAPI_TOKEN são obrigatórios com WHATSAPP_REPLY_MODE=provider")
			}
		case "twilio":
			if c.WhatsApp.TwilioAccountSID == "" || c.WhatsApp.TwilioFrom == "" {
				add("TWILIO_ACCOUNT_SID e TWILIO_WHATSAPP_FROM são obrigatórios com WHATSAPP_REPLY_MODE=provider")
			}
		}
	}
	switch c.Email.Provider {
	case "log":
		if c.IsProduction() {
			add("EMAIL_PROVIDER=log é proibido em produção (grava códigos de verificação no log)")
		}
	case "smtp":
		if c.Email.SMTPHost == "" || c.Email.SMTPFrom == "" {
			add("SMTP_HOST e SMTP_FROM são obrigatórios com EMAIL_PROVIDER=smtp")
		}
		if !oneOf(c.Email.SMTPTLS, "starttls", "tls", "none") {
			add("SMTP_TLS deve ser 'starttls', 'tls' ou 'none'")
		}
		if c.Email.SMTPTLS == "none" && c.IsProduction() {
			add("SMTP_TLS=none é proibido em produção")
		}
		if (c.Email.SMTPUsername == "") != (c.Email.SMTPPassword == "") {
			add("defina SMTP_USERNAME e SMTP_PASSWORD juntos (ou nenhum dos dois)")
		}
		// O Gmail/Workspace não aceita envio anônimo nem a senha normal da
		// conta: é 2FA + senha de app de 16 caracteres.
		if isGmailSMTP(c.Email.SMTPHost) {
			switch {
			case c.Email.SMTPUsername == "" || c.Email.SMTPPassword == "":
				add("SMTP_USERNAME e SMTP_PASSWORD são obrigatórios no SMTP do Gmail/Workspace (use uma senha de app)")
			case len(strings.ReplaceAll(c.Email.SMTPPassword, " ", "")) != 16:
				add("SMTP_PASSWORD do Gmail deve ser a senha de APP (16 caracteres) — a senha da conta é sempre recusada")
			}
			if c.Email.SMTPPort == 465 && c.Email.SMTPTLS != "tls" {
				add("no Gmail a porta 465 exige SMTP_TLS=tls (587 usa starttls)")
			}
		}
	default:
		add("EMAIL_PROVIDER deve ser 'smtp' ou 'log'")
	}
	if c.IsProduction() {
		for _, o := range c.HTTP.AllowedOrigins {
			if o == "*" {
				add("CORS_ALLOWED_ORIGINS não pode ser '*' com cookies de sessão")
			}
		}
		if c.Observ.MetricsAddr != "" && c.Observ.MetricsToken == "" && !strings.HasPrefix(c.Observ.MetricsAddr, "127.0.0.1:") {
			add("em produção defina METRICS_TOKEN ou restrinja METRICS_ADDR a 127.0.0.1")
		}
	}
	if c.Observ.TracingRatio < 0 || c.Observ.TracingRatio > 1 {
		add("OTEL_SAMPLE_RATIO deve estar entre 0 e 1")
	}
	return errs
}

// loadDotEnv carrega KEY=VALUE de um arquivo, sem sobrescrever variáveis já
// definidas no ambiente. Silencioso se o arquivo não existir.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// remove comentário no fim da linha (" # ...") fora de aspas
		if !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, `'`) {
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		v = strings.Trim(v, `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
