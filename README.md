# QuantoDeu API

API REST do aplicativo de finanças pessoais **QuantoDeu**: fluxo de caixa,
despesas recorrentes e parceladas, financiamentos e cartão de crédito com
fatura.

Escrita em Go com **arquitetura hexagonal**, PostgreSQL com **Row-Level
Security** e sessão em cookie `HttpOnly`.

## Arquitetura

Ports & Adapters: o núcleo não conhece Gin, pgx nem Redis — conhece apenas
interfaces. Trocar o banco ou o provedor de e-mail é escrever um adaptador
novo, sem tocar em regra de negócio.

```
        driving                    núcleo                    driven
   ┌───────────────┐        ┌─────────────────┐        ┌────────────────┐
   │ HTTP (Gin)    │───────▶│ ports/inputs    │        │ ports/outputs  │
   │ Webhook       │        │   services/     │───────▶│   PostgreSQL   │
   │ CLI / flags   │        │   domain/       │        │   Redis        │
   └───────────────┘        └─────────────────┘        │   SMTP         │
                                                       │   WhatsApp     │
                                                       └────────────────┘
```

| Camada | Responsabilidade | Depende de |
|---|---|---|
| `internal/core/domain` | Entidades e regras. Dinheiro em centavos (`int64`), amortização SAC/Price, ciclo da fatura | nada além da biblioteca padrão |
| `internal/core/ports` | Interfaces: casos de uso (entrada) e repositórios (saída) | `domain` |
| `internal/core/services` | Orquestração dos casos de uso | `domain`, `ports` |
| `internal/adapters` | HTTP, PostgreSQL, Redis, SMTP, WhatsApp, observabilidade | `ports` |
| `cmd/api` | Composition root: monta as dependências e o ciclo de vida | tudo |

Duas decisões moldam o resto: **todo valor monetário é inteiro em centavos**,
nunca `float`; e o **isolamento por usuário é do banco**, via RLS, não do
`WHERE` — a API conecta com um papel sem `BYPASSRLS`.

## Stack

| Camada | Tecnologia |
|---|---|
| Linguagem | Go 1.24 |
| HTTP | Gin · OpenAPI 3.1 gerado dos DTOs · Swagger UI embutido |
| Banco | PostgreSQL 16 · `pgx/v5` · SQL puro · migrations embutidas · Row-Level Security |
| Sessões e cache | PostgreSQL ou Redis (porta `SessionStore`) |
| Segurança | Argon2id · cookie assinado com HMAC · rate limit · bloqueio progressivo de login |
| E-mail | SMTP · Mailpit em desenvolvimento |
| WhatsApp | Evolution API, Z-API ou Twilio (desligado por padrão) |
| Observabilidade | `log/slog` em JSON · Prometheus · OpenTelemetry |
| Infra | Docker Compose · imagem distroless sem root · Cloudflare Tunnel |
| Testes | `go test` · integração com RLS real · smoke test em `curl` |

## Estrutura

```
cmd/api/main.go             composition root (injeção de dependências)
config/                     carregamento e validação das variáveis de ambiente
internal/core/
  domain/                   entidades e regras — só biblioteca padrão
  ports/                    inputs.go (casos de uso) · outputs.go (repositórios)
  services/                 orquestração + parser das mensagens do WhatsApp
internal/adapters/
  http/                     router, handlers, middlewares, DTOs, OpenAPI
  db/postgres/              repositórios pgx, tenant por transação, migrations
  db/redis/                 sessões, rate limit distribuído, tentativas de login
  email/ security/          SMTP · Argon2id, tokens, HMAC
  whatsapp/ observability/  provedores · Prometheus e OpenTelemetry
deploy/ scripts/            init do banco, backup, smoke test
```

## Rodando

```bash
cp .env.example .env         # troque os segredos (openssl rand -base64 48)
docker compose up --build    # API + PostgreSQL + Mailpit
```

Swagger em **http://localhost:8080/docs**, e-mails de teste em
**http://localhost:8025**. As rotas cobrem autenticação e conta, transações,
resumo, parcelamentos e financiamentos, cartões e faturas, e a agenda de
vencimentos — a especificação viva é o próprio Swagger.

```bash
make test              # testes unitários
make test-integration  # RLS, repositórios e Redis (com o compose no ar)
make smoke             # ponta a ponta contra a API rodando
make help              # todos os alvos
```

## Documentação

| Documento | Conteúdo |
|---|---|
| [`docs/ARQUITETURA.md`](docs/ARQUITETURA.md) | Especificação técnica: camadas, fluxos, segurança e banco |
| [`docs/REGRAS-DE-NEGOCIO.md`](docs/REGRAS-DE-NEGOCIO.md) | Por que os números são calculados assim |
| [`docs/OPERACAO.md`](docs/OPERACAO.md) | Colocar no ar, e-mail em produção, WhatsApp |
| [`docs/GUIA-DE-TESTES.md`](docs/GUIA-DE-TESTES.md) | Subir o projeto e testar cada recurso pelo Swagger |
| [`deploy/SERVIDOR.md`](deploy/SERVIDOR.md) | Servidor próprio com Docker e túnel da Cloudflare |
