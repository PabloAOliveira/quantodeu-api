#!/usr/bin/env bash
# Smoke test ponta a ponta da QuantoDeu API (só precisa de bash + curl).
#
#   API_URL=http://localhost:8080 WEBHOOK_SECRET=<WHATSAPP_WEBHOOK_SECRET> ./scripts/smoke-test.sh
#
# O código de confirmação de e-mail é lido do Mailpit (MAILPIT_URL, padrão
# http://localhost:8025) ou, com EMAIL_PROVIDER=log, do log do container.
#
# Cria um usuário novo a cada execução, então pode rodar quantas vezes quiser.
set -uo pipefail

API="${API_URL:-http://localhost:8080}"
B="$API/api/v1"
SECRET="${WEBHOOK_SECRET:-}"
MAILPIT="${MAILPIT_URL:-http://localhost:8025}"
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT

PASS=0; FAIL=0
green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }

# call MÉTODO ROTA [JSON] -> define $STATUS e $BODY
call() {
  local method=$1 path=$2 data=${3:-}
  local args=(-s -o /tmp/qd_body -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$method" "$B$path")
  [[ -n $data ]] && args+=(-H 'Content-Type: application/json' -d "$data")
  STATUS=$(curl "${args[@]}")
  BODY=$(cat /tmp/qd_body)
  # O smoke faz muitas chamadas seguidas: se bater no rate limit por IP,
  # espera e repete uma vez (não é falha da API).
  if [[ $STATUS == 429 && $BODY == *rate_limited* ]]; then
    sleep 2
    STATUS=$(curl "${args[@]}")
    BODY=$(cat /tmp/qd_body)
  fi
}

expect() { # expect DESCRIÇÃO STATUS_ESPERADO
  if [[ $STATUS == "$2" ]]; then green "  ✔ $1 ($STATUS)"; PASS=$((PASS+1))
  else red "  ✘ $1: esperado $2, obtido $STATUS — $BODY"; FAIL=$((FAIL+1)); fi
}

field() { # field NOME -> primeiro valor string/numérico do campo no $BODY
  printf '%s' "$BODY" | grep -o "\"$1\":\"\?[^\",}]*" | head -1 | sed -E "s/\"$1\":\"?//"
}

webhook() { # webhook TELEFONE ID TEXTO
  STATUS=$(curl -s -o /tmp/qd_body -w '%{http_code}' -X POST "$B/webhook/whatsapp" \
    -H 'Content-Type: application/json' -H "X-Webhook-Token: $SECRET" \
    -d "{\"event\":\"messages.upsert\",\"data\":{\"key\":{\"remoteJid\":\"$1@s.whatsapp.net\",\"fromMe\":false,\"id\":\"$2\"},\"message\":{\"conversation\":\"$3\"}}}")
  BODY=$(cat /tmp/qd_body)
}

email_code() { # email_code EMAIL -> código de 6 dígitos do último e-mail enviado
  local code="" i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    code=$(curl -s "$MAILPIT/api/v1/search?query=to:$1" 2>/dev/null | grep -o '"Subject":"[0-9]\{6\}' | head -1 | grep -o '[0-9]\{6\}')
    [[ -z $code ]] && code=$(docker compose logs api --since 5m 2>/dev/null | grep "$1" | grep -o '"assunto":"[0-9]\{6\}' | tail -1 | grep -o '[0-9]\{6\}')
    [[ -n $code ]] && break
    sleep 1
  done
  printf '%s' "$code"
}

N=$(( (RANDOM * 32768 + RANDOM) % 90000000 + 10000000 ))
EMAIL="smoke$N@exemplo.com"
FONE="11 9$N"
FONE_WA="5511$N"   # como o WhatsApp entrega: sem o nono dígito

echo "▶ API: $API"
# Aguarda até 30 s: logo após o "docker compose up" a API ainda roda as migrations.
for i in $(seq 1 30); do curl -sf "$API/readyz" >/dev/null && break; sleep 1; done
curl -sf "$API/readyz" >/dev/null && green "  ✔ /readyz" || { red "  ✘ API indisponível em $API — veja: docker compose logs api --tail 30"; exit 1; }

echo "▶ Autenticação"
call POST /auth/register "{\"nome\":\"Smoke Test\",\"email\":\"$EMAIL\",\"telefone\":\"$FONE\",\"senha\":\"senhaForte123\",\"saldo_inicial\":\"1.000,00\"}"
expect "cadastro" 201
call GET /me; expect "rota protegida sem login" 401
call POST /auth/login "{\"email\":\"$EMAIL\",\"senha\":\"errada123\"}"; expect "login com senha errada" 401
call POST /auth/login "{\"email\":\"$EMAIL\",\"senha\":\"senhaForte123\"}"; expect "login" 200
call GET /me; expect "perfil" 200

echo "▶ Confirmação de e-mail"
call GET /transacoes; T_STATUS=$STATUS
if [[ $T_STATUS == 403 ]]; then
  expect "rotas financeiras bloqueadas até confirmar o e-mail" 403
  CODE=$(email_code "$EMAIL")
  if [[ -z $CODE ]]; then
    red "  ✘ código não encontrado (Mailpit em $MAILPIT ou log do container)"; FAIL=$((FAIL+1))
  else
    green "  ✔ código recebido por e-mail ($CODE)"; PASS=$((PASS+1))
    call POST /me/email/verificacao/confirmar '{"codigo":"000000"}'; expect "código errado" 422
    call POST /me/email/verificacao/confirmar "{\"codigo\":\"$CODE\"}"; expect "confirmar e-mail" 200
    call POST /me/email/verificacao; expect "reenviar após confirmado" 409
  fi
else
  red "  ⚠ EMAIL_VERIFICATION_REQUIRED=false: pulando confirmação de e-mail"
fi

echo "▶ WhatsApp (verificação do telefone pelo método mensagem)"
# Com WHATSAPP_BOT_ENABLED=false (padrão do v1) o webhook nem existe.
BOT_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/webhook/whatsapp" -H 'Content-Type: application/json' -d '{}')
if [[ $BOT_STATUS == 404 ]]; then
  red "  ⚠ bot do WhatsApp desligado (WHATSAPP_BOT_ENABLED=false): pulando"
elif [[ -z $SECRET ]]; then
  red "  ⚠ WEBHOOK_SECRET não informado: pulando testes de WhatsApp"
else
  webhook "$FONE_WA" "smoke-$N-0" "Saldo"; expect "webhook aceito" 200
  call POST /me/telefone/verificacao '{"metodo":"mensagem"}'; expect "iniciar verificação" 202
  CODE=$(field codigo_para_enviar)
  webhook "$FONE_WA" "smoke-$N-1" "verificar $CODE"; expect "enviar 'verificar $CODE' pelo WhatsApp" 200
  call GET /me
  [[ $(field telefone_verificado) == "true" ]] && { green "  ✔ telefone verificado"; PASS=$((PASS+1)); } || { red "  ✘ telefone não verificado: $BODY"; FAIL=$((FAIL+1)); }
  webhook "$FONE_WA" "smoke-$N-2" "Gastei 150,00 mercado"; expect "lançamento pelo WhatsApp" 200
  webhook "$FONE_WA" "smoke-$N-2" "Gastei 150,00 mercado"; expect "reentrega idempotente" 200
fi

echo "▶ Transações"
call POST /transacoes '{"tipo":"entrada","valor":"3.000","categoria":"salario"}'; expect "criar entrada" 201
call POST /transacoes '{"tipo":"saida","valor":89.9,"categoria":"Farmácia","descricao":"remédios"}'; expect "criar saída" 201
TID=$(field id)
call PUT "/transacoes/$TID" '{"tipo":"saida","valor":"99,90","categoria":"farmacia","descricao":"remédios e vitaminas"}'; expect "editar" 200
call GET "/transacoes/$TID"; expect "detalhar" 200
call GET "/transacoes?page_size=5"; expect "listar" 200
call DELETE "/transacoes/$TID"; expect "excluir" 204
call GET "/transacoes/$TID"; expect "excluída não existe mais" 404

echo "▶ Parcelamentos"
call POST /parcelamentos '{"tipo":"parcelado","descricao":"Notebook","categoria":"eletronicos","valor_total":"3000","total_parcelas":6}'; expect "criar parcelado" 201
PID=$(field id)
call PUT "/parcelamentos/$PID" '{"tipo":"parcelado","descricao":"Notebook Pro","categoria":"eletronicos","valor_total":"3600","total_parcelas":12}'; expect "editar (regera parcelas)" 200
call GET "/parcelamentos/$PID"; expect "detalhar" 200
call GET /resumo; expect "resumo do mês" 200
call DELETE "/parcelamentos/$PID?manter_pagas=true"; expect "excluir mantendo pagas" 200

echo "▶ Financiamento (projeção de parcelas)"
call POST /parcelamentos/simular '{"banco":"Caixa","sistema":"sac","valor_financiado":"200000.00","taxa_juros_anual":8.66,"tipo_taxa":"efetiva","total_parcelas":360}'; expect "simular SAC" 200
call POST /parcelamentos/simular '{"sistema":"price","valor_financiado":100000,"taxa_juros_anual":12,"tipo_taxa":"nominal","total_parcelas":12}'
[[ $(field valor_primeira_parcela) == *"8.884,88"* || $BODY == *"8.884,88"* ]] && { green "  ✔ Price: parcela de R$ 8.884,88"; PASS=$((PASS+1)); } || { red "  ✘ Price com valor inesperado"; FAIL=$((FAIL+1)); }
call POST /parcelamentos/simular '{"sistema":"sac","valor_financiado":"200000.00","taxa_juros_anual":150,"total_parcelas":360}'; expect "taxa fora da faixa" 422
call POST /parcelamentos '{"tipo":"parcelado","descricao":"Apartamento","categoria":"moradia","total_parcelas":360,"financiamento":{"banco":"Caixa","sistema":"sac","valor_financiado":"200000.00","taxa_juros_anual":8.66}}'; expect "cadastrar financiamento" 201
FID=$(field id)
call GET "/parcelamentos/$FID"; expect "detalhar financiamento" 200
call DELETE "/parcelamentos/$FID"; expect "excluir financiamento" 200

echo "▶ Sessões e senha"
call PUT /me/senha '{"senha_atual":"senhaForte123","nova_senha":"novaSenha456"}'; expect "trocar senha" 200
call GET /me; expect "continua logado com o cookie novo" 200
call DELETE /me/sessoes; expect "encerrar todas as sessões" 200
call GET /me; expect "sessão encerrada" 401

echo "▶ Modo token (apps nativos)"
call POST /auth/login "{\"email\":\"$EMAIL\",\"senha\":\"novaSenha456\",\"modo\":\"token\"}"; expect "login modo token" 200
TOKEN=$(field token)
tcall() { STATUS=$(curl -s -o /tmp/qd_body -w '%{http_code}' -H "Authorization: Bearer $TOKEN" -X "$1" "$B$2"); BODY=$(cat /tmp/qd_body); }
tcall GET /resumo; expect "rota protegida com Bearer (sem cookie)" 200
TOKEN_OK=$TOKEN; TOKEN="${TOKEN}x"; tcall GET /me; expect "Bearer adulterado" 401; TOKEN=$TOKEN_OK
tcall POST /auth/logout; expect "logout com Bearer" 204
tcall GET /me; expect "token invalidado após logout" 401

echo "▶ Bloqueio progressivo de login"
for i in 1 2 3 4 5; do call POST /auth/login "{\"email\":\"$EMAIL\",\"senha\":\"errada$i$i$i\"}"; done
call POST /auth/login "{\"email\":\"$EMAIL\",\"senha\":\"errada666\"}"; expect "6ª falha bloqueia a conta" 429

echo
if (( FAIL == 0 )); then green "✅ $PASS verificações OK"; else red "❌ $FAIL falha(s), $PASS OK"; exit 1; fi
