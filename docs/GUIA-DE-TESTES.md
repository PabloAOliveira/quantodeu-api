# Guia de execução e testes — QuantoDeu API

Este guia leva você de "projeto recém-baixado" até testar todas as rotas pelo Swagger, explicando o que acontece por baixo em cada etapa.

## 1. Pré-requisitos

| Ferramenta | Para quê | Verificar |
|---|---|---|
| Docker Desktop (ou OrbStack) | subir API, PostgreSQL, Redis, Prometheus, Jaeger | `docker compose version` |
| Go 1.24+ | gerar o `go.sum` e rodar os testes | `go version` |
| Chrome ou Firefox | Swagger UI | — |

> **Evite o Safari para o Swagger local.** O cookie de sessão é `Secure` e o Safari pode recusar cookies `Secure` em `http://localhost`. Chrome e Firefox tratam `localhost` como origem segura.

## 2. Subir o ambiente

```bash
cd quandodeu-api
cp .env.example .env
```

No `.env`, troque pelo menos `SESSION_SECRETS` e `WHATSAPP_WEBHOOK_SECRET`. Gere os valores com `openssl rand -base64 48`.

```bash
go mod tidy                                            # 1ª vez: gera o go.sum
docker compose --profile redis --profile observability up --build -d
docker compose logs -f api                             # acompanhe o boot
```

No log de boot, confirme estas três linhas:

```
migration aplicada            version=0001_init
migration aplicada            version=0002_seguranca_rls
RLS ativo para a role da aplicação   role=quantodeu_api
QuantoDeu API iniciada ...
```

**O que aconteceu:**

1. O PostgreSQL criou o banco e executou `deploy/postgres/init/01-roles.sh`, que cria dois usuários:
   - `quantodeu` é o dono das tabelas e roda as migrations.
   - `quantodeu_api` é o usuário da API. Como não é dono das tabelas, ele fica **sujeito ao RLS**.
2. A API aplicou as migrations com o dono e depois conectou como `quantodeu_api`.
3. No boot, a API verifica se o RLS vale para esse usuário. Com `DB_REQUIRE_RLS=true`, ela se recusa a subir se ele puder ignorar o RLS.

| Serviço | URL |
|---|---|
| Swagger UI | http://localhost:8080/docs |
| Especificação OpenAPI | http://localhost:8080/openapi.json |
| Métricas | http://localhost:9091/metrics |
| Prometheus | http://localhost:9090 |
| Jaeger (traces) | http://localhost:16686 (exige `OTEL_ENABLED=true` no `.env`) |

**Teste automático rápido.** Deve terminar com `✅ 44 verificações OK`:

```bash
make smoke
```

## 3. Roteiro pelo Swagger

Abra http://localhost:8080/docs. Os campos de exemplo já vêm preenchidos: basta ajustar e clicar em **Execute**.

### 3.1 Cadastro e login

1. Em **Autenticação → POST /auth/register**, envie o exemplo e ajuste o e-mail e o telefone para os seus. A resposta esperada é **201**, com `email_verificado: false`. Um código de 6 dígitos foi enviado para o e-mail.
2. Em **POST /auth/login**, envie o mesmo e-mail e senha. A resposta é **200**.
   - O navegador guardou o cookie `__Host-quantodeu_session`. Você **não** o vê no Swagger porque ele é `HttpOnly`, ou seja, invisível para JavaScript. É isso que protege a sessão contra XSS.
   - O servidor guardou apenas o **hash SHA-256** do token. O valor do cookie é `token.assinatura_HMAC`.
3. Em **Conta → GET /me**, a resposta é **200**. As rotas com cadeado usam o cookie automaticamente.
4. Tente **GET /transacoes**: a resposta é **403 `email_nao_verificado`**.

### 3.2 Confirmação do e-mail

1. Abra a caixa de e-mails de teste em **http://localhost:8025** (Mailpit) e veja o código no assunto ou no corpo do e-mail. Nenhum e-mail sai de verdade para a internet.
2. Em **Conta → POST /me/email/verificacao/confirmar**, envie `{"codigo":"123456"}` com o seu código. A resposta é **200**.
3. **GET /me** mostra `email_verificado: true`, e as rotas financeiras passam a funcionar.

Para reenviar, use **POST /me/email/verificacao** (uma vez por minuto; 409 se já estiver confirmado). O código vale por 30 min e 5 erros o invalidam.

**Enviar e-mails de verdade** (para testar no celular, por exemplo), troque no `.env` e rode `docker compose up -d api`:

```env
EMAIL_PROVIDER=smtp
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_TLS=starttls
SMTP_USERNAME=seu@gmail.com
SMTP_PASSWORD=senha-de-app-de-16-letras
SMTP_FROM=QuantoDeu <seu@gmail.com>
```

No Gmail a senha precisa ser uma **senha de app** (Conta Google → Segurança → Verificação em duas etapas → Senhas de app); a senha normal é recusada.

### 3.3 Verificação do telefone (opcional)

Com `WHATSAPP_REQUIRE_VERIFIED_PHONE=true`, enquanto o número não for verificado o WhatsApp **não lança nada**. Com `false`, esta etapa pode ser pulada. Isso impede que alguém cadastre o número de outra pessoa e lance em nome dela. Há dois métodos:

**Método `mensagem`** (não depende de enviar mensagens):

1. Chame **POST /me/telefone/verificacao** com `{"metodo":"mensagem"}`. A resposta é **202** e traz `codigo_para_enviar`, por exemplo `482913`.
2. Simule a mensagem do seu WhatsApp em **WhatsApp → POST /webhook/whatsapp**:
   - Clique no cadeado e informe o `WHATSAPP_WEBHOOK_SECRET` do `.env`.
   - Envie o corpo abaixo, trocando o telefone e o código:
   ```json
   {"event":"messages.upsert","data":{"key":{"remoteJid":"5511987654321@s.whatsapp.net","fromMe":false,"id":"teste-1"},"message":{"conversation":"verificar 482913"}}}
   ```
3. Chame **GET /me** de novo: agora vem `telefone_verificado: true`.

**Método `codigo`** (a API envia o código e você digita no app):

1. Chame **POST /me/telefone/verificacao** com `{"metodo":"codigo"}`.
2. Com `WHATSAPP_REPLY_MODE=log`, nada é enviado de verdade: o código vai para o log.
   ```bash
   docker compose logs api | grep "\[DEV\]"
   ```
3. Envie o código em **POST /me/telefone/verificacao/confirmar**.

**Regras de segurança da verificação:**

- O código tem 6 dígitos, expira em 10 min e só pode ser regenerado a cada 60 s (429 com `Retry-After`).
- Com 5 erros, o código é invalidado.
- O banco guarda só um HMAC do código, com um pepper que fica apenas no servidor.
- No método `mensagem`, o código exibido no app **não** pode ser confirmado pelo próprio app, só pelo WhatsApp do número cadastrado.

### 3.4 Lançamentos pelo WhatsApp

Com o e-mail confirmado (e o telefone verificado, se exigido), envie pelo webhook, trocando o `id` a cada envio:

| `conversation` | Resultado |
|---|---|
| `Gastei 150,00 mercado` | saída de R$ 150,00, categoria `mercado` |
| `Recebi 1.500 do freela` | entrada de R$ 1.500,00 |
| `Gastei no mercado 80.50` | valor no final também funciona |
| `Rendeu 12,34 poupança` | entrada na categoria `rendimentos` |
| `Saldo` | responde o saldo (veja no log `[DEV]`) |
| `bom dia` | não lança; responde uma mensagem de ajuda |

Reenvie exatamente o mesmo `id`: a API responde 200, mas **não duplica** o lançamento. Isso é a idempotência por `external_id`.

Dica: use o número **sem o nono dígito** no `remoteJid` (`551187654321`). O WhatsApp costuma entregar assim, e a API encontra o cadastro nas duas formas.

### 3.5 Transações (CRUD)

1. **POST /transacoes** com `{"tipo":"saida","valor":"89,90","categoria":"Farmácia"}` responde **201**. Copie o `id`.
2. **GET /transacoes/{id}** responde **200**. **PUT /transacoes/{id}** substitui os campos e responde **200**.
3. **DELETE /transacoes/{id}** responde **204**. Um novo GET no mesmo id responde **404**.
4. **GET /transacoes?mes=9&ano=2026&categoria=mercado&page_size=5** lista com filtros e paginação.

**Isolamento:** abra uma janela anônima, crie outro usuário e tente ler o `id` do primeiro. A resposta é **404**, não 403, para não revelar que o registro existe.

### 3.6 Financiamento (parcela calculada por juros)

1. Em **Parcelamentos → POST /parcelamentos/simular**, envie:
   ```json
   {"banco":"Caixa Econômica Federal","sistema":"sac","valor_financiado":"200000,00","taxa_juros_anual":8.66,"tipo_taxa":"efetiva","total_parcelas":360,"data_primeira_parcela":"2026-10-10"}
   ```
   A resposta traz a tabela de amortização: 1ª parcela de R$ 1.946,58, última de R$ 559,41 e R$ 250.716,93 de juros.
2. Troque para `"sistema":"price"` e veja a parcela ficar constante.
3. Para cadastrar de verdade, mande o mesmo bloco dentro de **POST /parcelamentos**, no campo `financiamento` (sem `valor_total`). Cada parcela é gravada com o seu próprio valor.
4. **GET /parcelamentos** mostra o bloco `financiamento` com a taxa mensal e o total de juros projetado.
5. No **PUT**, mudar prazo, taxa ou sistema regera as parcelas; omitir `financiamento` transforma o registro num parcelamento comum.

### 3.7 Parcelamentos

1. **POST /parcelamentos**, com `valor_total` dividido em N parcelas mensais:
   ```json
   {"tipo":"parcelado","descricao":"Notebook","categoria":"eletronicos","valor_total":"3000","total_parcelas":6,"data_primeira_parcela":"2026-07-15"}
   ```
   A resposta traz as 6 parcelas. Os centavos que sobram da divisão ficam na 1ª parcela.
2. **DELETE /transacoes/{id_de_uma_parcela}** responde **409**: parcelas são geridas pelo parcelamento. Já o **PUT** numa parcela é permitido, por exemplo para ajustar o valor com juros.
3. **PUT /parcelamentos/{id}** recalcula e **regera todas** as parcelas numa transação única.
4. **DELETE /parcelamentos/{id}?manter_pagas=true** remove o parcelamento, mas mantém como lançamentos avulsos as parcelas com data até hoje. Elas representam dinheiro que já saiu.
5. **GET /resumo** mostra `custos_fixos_parcelados` e `parcelas_do_mes`.

### 3.8 Sessões e senha

1. Faça login também em outro navegador.
2. **DELETE /me/sessoes?manter_atual=true** encerra só as outras sessões: o outro navegador passa a receber 401.
3. **PUT /me/senha** com `{"senha_atual":"...","nova_senha":"..."}` encerra **todas** as sessões e emite um cookie novo para este navegador, que continua logado.
4. **DELETE /me/sessoes** sem parâmetro encerra tudo, inclusive a sessão atual.

### 3.9 Bloqueio progressivo e rate limit

1. Faça **POST /auth/login** com a senha errada 6 vezes seguidas.
   - As tentativas 1 a 5 retornam **401**.
   - A 6ª retorna **429** `account_locked` com `Retry-After: 60`.
   - A 7ª falha, depois do bloqueio, bloqueia por 2 min; depois 4 min, e assim por diante até 30 min.
2. Durante o bloqueio, **nem a senha correta entra**. Um login bem-sucedido zera o contador.
3. E-mails inexistentes também são bloqueados, para não revelar quem tem cadastro.
4. Além disso, cada IP tem limite de 10 logins/cadastros por minuto (429 `rate_limited`).

Para liberar uma conta durante os testes:

```bash
make psql
DELETE FROM login_attempts;            -- com LOGIN_ATTEMPTS_STORE=postgres
```

Com Redis, use `docker compose exec redis redis-cli --scan --pattern 'qd:login:*' | xargs docker compose exec -T redis redis-cli del`.

## 4. Vendo o Row-Level Security funcionar

```bash
docker compose exec db psql -U quantodeu_api -d quantodeu    # senha: APP_DB_PASSWORD
```

```sql
-- Sem tenant definido: o banco não devolve NADA, mesmo sem WHERE.
SELECT count(*) FROM transacoes;                 -- 0

-- Com o tenant de um usuário: só as linhas dele.
BEGIN;
SELECT set_config('app.user_id', (SELECT id::text FROM qd_find_user_by_email('maria@exemplo.com')), true);
SELECT count(*), sum(valor_centavos) FROM transacoes;
COMMIT;

-- Tentar gravar em nome de outro usuário viola a política (WITH CHECK).
```

Agora compare com o dono das tabelas (`make psql`): `SELECT count(*) FROM transacoes;` mostra tudo. Por isso a API **nunca** conecta como dono.

## 5. Observabilidade

**Métricas** (http://localhost:9091/metrics ou Prometheus em http://localhost:9090):

| Consulta PromQL | O que mostra |
|---|---|
| `sum by (route,status) (rate(quantodeu_http_requests_total[5m]))` | tráfego por rota e status |
| `histogram_quantile(0.95, sum by (le,route) (rate(quantodeu_http_request_duration_seconds_bucket[5m])))` | latência p95 |
| `quantodeu_login_tentativas_total` | sucessos, falhas e bloqueios |
| `quantodeu_webhook_mensagens_total` | desfechos do WhatsApp |
| `quantodeu_transacoes_registradas_total` | lançamentos por origem |
| `quantodeu_http_rate_limited_total` | bloqueios de rate limit |
| `quantodeu_db_pool_acquired_conns` | uso do pool do banco |

**Traces:**

1. Ponha `OTEL_ENABLED=true` no `.env` e rode `docker compose up -d api`.
2. Abra o Jaeger, escolha o serviço `quantodeu-api` e veja cada requisição com os spans das queries SQL.
3. O `trace_id` também aparece nos logs JSON, para correlacionar log e trace.

## 6. Testes automatizados

```bash
make test               # unitários: domínio, parser, serviços, segurança, converters, OpenAPI
make test-integration   # com o compose no ar: RLS, repositórios, Redis
make smoke              # ponta a ponta via HTTP
```

## 7. Problemas comuns

| Sintoma | Causa e solução |
|---|---|
| `a role do DATABASE_URL ignora Row-Level Security` | A API está conectando como superuser ou dono. Use o usuário `quantodeu_api`. Em ambiente antigo (v1.0), rode `docker compose down -v` |
| `password authentication failed for user "quantodeu_api"` | O volume foi criado antes do script de roles. Rode `docker compose down -v` ou execute `deploy/postgres/init/01-roles.sh` manualmente |
| Login dá 200, mas as rotas seguintes dão 401 no Swagger | Navegador recusando o cookie `Secure` (Safari) ou acesso por IP. Use Chrome/Firefox em `http://localhost:8080` |
| 403 `origem não permitida` | Front-end em outra origem. Adicione-a em `CORS_ALLOWED_ORIGINS` |
| 429 `rate_limited` / `account_locked` | Proteções funcionando. Aguarde o `Retry-After` ou limpe `login_attempts` |
| `missing go.sum entry` | Rode `go mod tidy` |
| 403 `email_nao_verificado` | Confirme o e-mail com o código do Mailpit (http://localhost:8025) ou desligue com `EMAIL_VERIFICATION_REQUIRED=false` |
| `email_delivery_failed` (503) | SMTP inacessível ou credenciais erradas. Veja `docker compose logs api | grep smtp`. No Gmail use senha de app |
| `/readyz` com `"status":"degradado"` | Redis fora do ar, mas usado só para rate limit/bloqueio (fail-open). A API segue atendendo |
