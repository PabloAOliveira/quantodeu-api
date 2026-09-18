// Package observability implementa métricas (Prometheus) e tracing
// (OpenTelemetry). O núcleo conhece apenas a porta ports.Metrics.
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

const namespace = "quantodeu"

// Prometheus implementa ports.Metrics, middlewares.HTTPRecorder e
// middlewares.RateLimitObserver sobre um registry próprio (sem estado global).
type Prometheus struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight prometheus.Gauge
	rateLimited  *prometheus.CounterVec

	transacoes   *prometheus.CounterVec
	webhooks     *prometheus.CounterVec
	logins       *prometheus.CounterVec
	verificacoes *prometheus.CounterVec
	verifEmail   *prometheus.CounterVec
	sessoes      *prometheus.CounterVec
}

var _ ports.Metrics = (*Prometheus)(nil)

// NewPrometheus cria o registry com métricas de runtime Go, processo e negócio.
func NewPrometheus(buildVersion string) *Prometheus {
	reg := prometheus.NewRegistry()
	p := &Prometheus{
		registry: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_total",
			Help: "Total de requisições HTTP por método, rota e status.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help:    "Latência das requisições HTTP.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"method", "route"}),
		httpInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_in_flight",
			Help: "Requisições HTTP em andamento.",
		}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "rate_limited_total",
			Help: "Requisições bloqueadas pelo rate limit, por escopo.",
		}, []string{"scope"}),
		transacoes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "transacoes_registradas_total",
			Help: "Lançamentos registrados por origem e tipo.",
		}, []string{"origem", "tipo"}),
		webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "webhook_mensagens_total",
			Help: "Mensagens de WhatsApp processadas por provedor e desfecho.",
		}, []string{"provider", "status"}),
		logins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "login_tentativas_total",
			Help: "Tentativas de login por resultado (sucesso, credenciais_invalidas, bloqueado, bloqueio_aplicado).",
		}, []string{"resultado"}),
		verificacoes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "verificacao_telefone_eventos_total",
			Help: "Eventos da verificação de telefone.",
		}, []string{"evento"}),
		verifEmail: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "verificacao_email_eventos_total",
			Help: "Eventos da verificação de e-mail (enviado, falha_envio, confirmada, codigo_invalido).",
		}, []string{"evento"}),
		sessoes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "sessoes_revogadas_total",
			Help: "Sessões encerradas em massa, por motivo.",
		}, []string{"motivo"}),
	}
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info", Help: "Versão em execução.",
	}, []string{"version"})
	buildInfo.WithLabelValues(buildVersion).Set(1)

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		p.httpRequests, p.httpDuration, p.httpInFlight, p.rateLimited,
		p.transacoes, p.webhooks, p.logins, p.verificacoes, p.verifEmail, p.sessoes,
	)
	return p
}

// RegisterPool expõe as estatísticas do pool pgx.
func (p *Prometheus) RegisterPool(pool *pgxpool.Pool) {
	p.registry.MustRegister(&poolCollector{pool: pool})
}

// Handler devolve o endpoint /metrics.
func (p *Prometheus) Handler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// ObserveHTTP implementa middlewares.HTTPRecorder.
func (p *Prometheus) ObserveHTTP(method, route string, status int, d time.Duration) {
	p.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	p.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// InFlight implementa middlewares.HTTPRecorder.
func (p *Prometheus) InFlight(delta float64) { p.httpInFlight.Add(delta) }

// RateLimited implementa middlewares.RateLimitObserver.
func (p *Prometheus) RateLimited(scope string) { p.rateLimited.WithLabelValues(scope).Inc() }

// TransacaoRegistrada implementa ports.Metrics.
func (p *Prometheus) TransacaoRegistrada(origem, tipo string) {
	p.transacoes.WithLabelValues(origem, tipo).Inc()
}

// WebhookProcessado implementa ports.Metrics.
func (p *Prometheus) WebhookProcessado(provider, status string) {
	p.webhooks.WithLabelValues(provider, status).Inc()
}

// LoginTentativa implementa ports.Metrics.
func (p *Prometheus) LoginTentativa(resultado string) { p.logins.WithLabelValues(resultado).Inc() }

// VerificacaoTelefone implementa ports.Metrics.
func (p *Prometheus) VerificacaoTelefone(evento string) { p.verificacoes.WithLabelValues(evento).Inc() }

// VerificacaoEmail implementa ports.Metrics.
func (p *Prometheus) VerificacaoEmail(evento string) { p.verifEmail.WithLabelValues(evento).Inc() }

// SessoesRevogadas implementa ports.Metrics.
func (p *Prometheus) SessoesRevogadas(motivo string, total int64) {
	p.sessoes.WithLabelValues(motivo).Add(float64(total))
}

// poolCollector exporta pgxpool.Stat() a cada scrape.
type poolCollector struct{ pool *pgxpool.Pool }

var (
	poolAcquired = prometheus.NewDesc(namespace+"_db_pool_acquired_conns", "Conexões em uso.", nil, nil)
	poolIdle     = prometheus.NewDesc(namespace+"_db_pool_idle_conns", "Conexões ociosas.", nil, nil)
	poolTotal    = prometheus.NewDesc(namespace+"_db_pool_total_conns", "Conexões abertas.", nil, nil)
	poolMax      = prometheus.NewDesc(namespace+"_db_pool_max_conns", "Limite do pool.", nil, nil)
	poolWaits    = prometheus.NewDesc(namespace+"_db_pool_empty_acquire_total", "Aquisições que precisaram esperar.", nil, nil)
	poolWaitDur  = prometheus.NewDesc(namespace+"_db_pool_acquire_wait_seconds_total", "Tempo total esperando conexão.", nil, nil)
)

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{poolAcquired, poolIdle, poolTotal, poolMax, poolWaits, poolWaitDur} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(poolAcquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(poolIdle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(poolTotal, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(poolMax, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolWaits, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolWaitDur, prometheus.CounterValue, s.AcquireDuration().Seconds())
}
