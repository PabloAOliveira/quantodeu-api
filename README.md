# QuantoDeu API

API REST do app de finanças pessoais **QuantoDeu**, escrita em Go com **Arquitetura Hexagonal (Ports & Adapters)**. Usa Gin, PostgreSQL com **Row-Level Security**, sessões em cookie seguro, **confirmação de conta por e-mail**, lançamentos por **WhatsApp**, **OpenAPI 3.1 + Swagger**, **Prometheus** e **OpenTelemetry**.

| Documento | Conteúdo |
|---|---|
| [`docs/GUIA-DE-TESTES.md`](docs/GUIA-DE-TESTES.md) | Passo a passo para subir o projeto, testar pelo Swagger e entender cada recurso |
| [`docs/ARQUITETURA.md`](docs/ARQUITETURA.md) | Especificação técnica: camadas, fluxos, segurança, banco e decisões |
| [`docs/api.http`](docs/api.http) | Requisições prontas para o REST Client (VS Code/IntelliJ) |

## Stack

| Camada | Tecnologia |
|---|---|
| Linguagem | Go 1.24+ |
| HTTP (driving) | Gin · OpenAPI 3.1 gerado dos DTOs · Swagger UI embutido |
| Banco (driven) | PostgreSQL 16 · `pgx/v5` · SQL puro · migrations embutidas · **RLS** |
| Sessões | PostgreSQL ou Redis (porta `SessionStore`) |
| Financiamento | Tabela de amortização SAC e Price calculada em centavos |
| Proteção de acesso | Argon2id · rate limit em memória ou Redis · bloqueio progressivo de login |
| E-mail | SMTP (qualquer provedor) · código de 6 dígitos · Mailpit em DEV |
| WhatsApp | Evolution API, Z-API ou Twilio · verificação de posse do número (opcional) |
| Observabilidade | `log/slog` JSON · Prometheus (`/metrics`) · OpenTelemetry (OTLP) |

## Início rápido

```bash
cp .env.example .env         # troque os segredos (openssl rand -base64 48)
go mod tidy                  # gera o go.sum (primeira vez)
docker compose up --build    # API + PostgreSQL + Mailpit
```

Abra **http://localhost:8080/docs** (Chrome ou Firefox). Os e-mails de confirmação chegam na caixa de teste **http://localhost:8025** (Mailpit). Depois rode o smoke test com `make smoke`.

Para subir o ambiente completo, com Redis, Prometheus e Jaeger, rode `make docker-up-full` e defina `OTEL_ENABLED=true` no `.env`.

> **Já rodou a versão 1.0?** A v1.1 cria um usuário de banco próprio para a API (exigência do RLS), mas o script que cria esse usuário só roda num volume novo. Rode `docker compose down -v` e suba de novo. Isso **apaga os dados locais**.

## Endpoints

| Método | Rota | Auth | Descrição |
|---|---|---|---|
| POST | `/api/v1/auth/register` | — | Cadastro |
| POST | `/api/v1/auth/login` | — | Login: cookie `HttpOnly; Secure; SameSite=Strict` (navegador) ou `"modo":"token"` para apps (Bearer), com bloqueio progressivo |
| POST | `/api/v1/auth/logout` | cookie | Encerra a sessão atual |
| GET | `/api/v1/me` | cookie | Perfil, saldo, totais do mês, status do e-mail e do telefone |
| PUT | `/api/v1/me/senha` | cookie | Troca a senha e **encerra todas as sessões** (emite cookie novo) |
| DELETE | `/api/v1/me/sessoes?manter_atual=` | cookie | Encerra todas as sessões ou apenas as outras |
| POST | `/api/v1/me/email/verificacao` | cookie | Reenvia o código de confirmação do e-mail (o cadastro já envia) |
| POST | `/api/v1/me/email/verificacao/confirmar` | cookie | Confirma o e-mail com o código de 6 dígitos |
| POST | `/api/v1/me/telefone/verificacao` | cookie | Inicia a verificação do número (`codigo` ou `mensagem`) |
| POST | `/api/v1/me/telefone/verificacao/confirmar` | cookie | Confirma o código recebido no WhatsApp |
| GET / POST | `/api/v1/transacoes` | cookie + e-mail ✔ | Listar (filtros e paginação) / criar |
| GET / PUT / DELETE | `/api/v1/transacoes/{id}` | cookie + e-mail ✔ | Detalhar / editar / excluir |
| GET | `/api/v1/resumo` | cookie + e-mail ✔ | Consolidado do mês |
| GET / POST | `/api/v1/parcelamentos` | cookie + e-mail ✔ | Listar (com progresso: pagas, restantes, valor amortizado) / criar (parcelado, recorrente ou **financiamento**) |
| POST | `/api/v1/parcelamentos/simular` | cookie + e-mail ✔ | Simular financiamento (SAC ou Price) sem gravar nada |
| GET / PUT / DELETE | `/api/v1/parcelamentos/{id}?manter_pagas=` | cookie + e-mail ✔ | Detalhar / editar (regera parcelas) / excluir |
| POST | `/api/v1/webhook/whatsapp` | token/assinatura | Mensagens do provedor |
| GET | `/healthz` · `/readyz` | — | Liveness / readiness |
| GET | `/docs` · `/openapi.json` | — | Swagger UI / especificação (desligados em produção por padrão) |
| GET | `:9091/metrics` | Bearer opcional | Métricas Prometheus (porta separada) |

**cookie** = cookie de sessão (navegador) **ou** `Authorization: Bearer <token>` (apps nativos, token obtido no login com `"modo":"token"`).

**e-mail ✔** = exige e-mail confirmado quando `EMAIL_VERIFICATION_REQUIRED=true` (senão responde 403 `email_nao_verificado`).

### Parcelamento que começou antes do cadastro

`POST`/`PUT /parcelamentos` aceitam `parcelas_ja_pagas` (padrão 0): quantas das
primeiras parcelas já foram quitadas **fora do app**.

Um financiamento cadastrado hoje com `data_primeira_parcela` em abril gerava
lançamento para abril, maio, junho… — dinheiro que já saiu da conta antes e que
o saldo informado no cadastro já descontava. O saldo ficava negativo por uma
dívida paga.

Com `parcelas_ja_pagas: 6` a API:

- mantém o cronograma inteiro (é ele que define a parcela base do SAC, o
  `valor_total` e o `data_ultima_parcela`);
- **não grava** as 6 primeiras como transação, então elas não mexem no saldo;
- começa os lançamentos na 7ª, com a numeração do contrato (`(7/360)`);
- soma essas 6 no `progresso` como pagas — `valor_pago` inclui o valor delas,
  tirado do cronograma.

Limite: `0 <= parcelas_ja_pagas < total_parcelas` (com aporte extra, o total
válido é o prazo encurtado). **No PUT, omitir o campo volta para zero** e as
parcelas antigas são lançadas de novo — a mesma pegadinha do bloco
`financiamento`: quem edita reenvia.

## Cartão de crédito

A compra no cartão **não mexe no saldo**: ela é dívida com o banco. O dinheiro
só se move quando a fatura é paga — e é isso que resolve o descompasso entre
comprar num mês e pagar no outro.

```
POST /cartoes/{id}/compras                 -> não altera saldo nem despesas
POST /cartoes/{id}/faturas/2026-09/pagar   -> cria UMA saída na data do pagamento
```

A saída nasce na **data do pagamento**, não no vencimento nem no mês das
compras. Por isso a fatura de setembro paga em outubro aparece nas despesas de
outubro: foi em outubro que o dinheiro saiu da conta.

Três regras que valem a pena conhecer:

- **Vencimento é a próxima ocorrência do dia após o fechamento.** O mesmo par de
  campos cobre "fecha 20, vence 21" (mesmo mês) e "fecha 28, vence 5" (mês
  seguinte), sem perguntar nada a mais ao usuário.
- **Compra depois do fechamento cai na fatura seguinte.** É a regra que mais
  confunde na vida real e a que tem teste dedicado, incluindo um que percorre
  todos os dias de um ano em quatro configurações de cartão para garantir que
  nenhum dia fique fora de um ciclo — nem em dois.
- **A fatura é derivada**, nunca armazenada: ela sai do cartão + das compras,
  como o progresso do parcelamento. O único fato gravado é o pagamento.
- **Quem manda na despesa é o valor pago**, não o total da fatura: o banco cobra
  o que quer, e o `valor_pago` volta na resposta para a tela não anunciar um
  número que não saiu da conta.
- **Cartão arquivado (`ativo=false`) não aceita compra nova** (409
  `cartao_arquivado`). Arquivar é dizer "não uso mais"; as faturas antigas ficam.

As compras ficam em `compras_cartao`, fora de `transacoes`, justamente porque lá
vale "toda linha mexe no saldo" e `SaldoAte` depende disso.

## Colocar no ar

Servidor próprio (PC de casa com Linux Mint + Docker + túnel da Cloudflare):
**[deploy/SERVIDOR.md](deploy/SERVIDOR.md)** — passo a passo, do `apt install`
ao app apontando para o domínio.

```bash
cp .env.production.example .env            # preencha os segredos
docker compose -f docker-compose.prod.yml up -d --build
docker compose -f docker-compose.prod.yml run --rm api -check-config
```

## E-mail em produção (Gmail / Workspace)

O código de confirmação sai pelo `SMTPSender` — o mesmo adaptador do Mailpit em
desenvolvimento. Para produção com Gmail ou Google Workspace:

```bash
EMAIL_PROVIDER=smtp
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_TLS=starttls
SMTP_USERNAME=suaconta@seudominio.com.br
SMTP_PASSWORD=<senha de APP, 16 letras>
SMTP_FROM=QuantoDeu <suaconta@seudominio.com.br>
```

Três detalhes que derrubam o envio, e que a config agora recusa no boot em vez
de deixar falhar só na hora de cadastrar um usuário:

1. **Senha de app, não a senha da conta.** Ative a verificação em duas etapas e
   gere em `myaccount.google.com/apppasswords`. A senha normal é sempre
   recusada.
2. **O remetente tem que ser a conta autenticada** — ou um alias confirmado nela
   (Gmail > Configurações > Contas > "Enviar e-mail como"). Com outro endereço o
   Gmail reescreve o remetente sem avisar; a API registra um aviso no boot
   quando `SMTP_FROM` difere de `SMTP_USERNAME`.
3. **Porta e TLS combinam**: 587 com `starttls`, 465 com `tls`.

Confira antes de abrir para usuários. O primeiro comando só valida (não envia
nada, não sobe a API); o segundo manda um e-mail de verdade e devolve o erro do
provedor sem enfeite:

```bash
make check-config
make mail-test TO=voce@exemplo.com
```

`check-config` imprime a configuração efetiva (sem segredos) e avisa sobre o que
sobe mas costuma estar errado: remetente que não é a conta autenticada, Swagger
público em produção, `TRUSTED_PROXIES` vazio atrás de proxy e
`SESSION_IDLE_TIMEOUT` maior que o `SESSION_TTL`.

Limite do Gmail: ~500 destinatários/dia (2.000 no Workspace); passar disso
suspende a conta por 24 h. Para volume maior, troque o host por um serviço
transacional (Resend, Brevo, SES) — as variáveis são as mesmas.

## WhatsApp: desligado no v1

`WHATSAPP_BOT_ENABLED` (padrão **false**) controla o bot inteiro. Desligado:

- `POST /webhook/whatsapp` e as rotas `/me/telefone/verificacao*` **não são
  registradas** — não existem nem no `/openapi.json`;
- nenhuma credencial de provedor é exigida, nem `WHATSAPP_WEBHOOK_SECRET` em
  produção. Sem isso, subir o v1 exigiria inventar segredos para um bot que não
  está no ar.

O app tem o par disso em `kWhatsAppEnabled`. Quando o bot entrar (v2), ligue os
dois juntos.

## Comandos

```bash
make help              # lista tudo
make test              # testes unitários
make test-integration  # RLS, repositórios e Redis (com o compose no ar)
make smoke             # teste ponta a ponta contra a API rodando
make mail              # abre a caixa de e-mails de teste (Mailpit)
make docker-up-full    # ambiente completo
```

## Estrutura

```
cmd/api/main.go                     composition root (DI + ciclo de vida)
config/                             carregamento/validação de env
internal/core/                      NÚCLEO — só biblioteca padrão
  domain/                           entidades e regras (User, Transacao, Parcelamento, Session, lockout, verificação)
  ports/inputs.go · outputs.go      portas primárias e secundárias
  services/                         casos de uso + RegexParserService
internal/adapters/
  http/                             router, handlers, middlewares, DTOs, cookie, openapi (+ Swagger UI embutido)
  db/postgres/                      repositórios pgx com tenant por transação + migrations (0002 = RLS, 0003 = e-mail, 0004 = financiamento)
  db/redis/                         sessões, rate limit distribuído (Lua) e tentativas de login
  email/                            envio SMTP (STARTTLS/TLS) e log para DEV
  security/                         Argon2id, tokens, HMAC do cookie e dos códigos
  whatsapp/                         converters e senders (Evolution, Z-API, Twilio, log)
  observability/                    Prometheus e OpenTelemetry
deploy/                             init do PostgreSQL (roles) e config do Prometheus
scripts/smoke-test.sh               teste ponta a ponta com curl
```
