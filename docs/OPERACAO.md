# QuantoDeu API — Operação

Como colocar no ar e configurar os serviços externos. O passo a passo do
servidor de casa está em [`../deploy/SERVIDOR.md`](../deploy/SERVIDOR.md).

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
