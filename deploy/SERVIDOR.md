# Servidor de casa (Linux Mint 22 + Docker)

Guia para deixar o QuantoDeu rodando num PC próprio, com o app chegando nele
por um túnel da Cloudflare — sem abrir porta no roteador.

As partes 1 e 2 podem ser feitas **hoje**, sem domínio. O túnel (parte 3) entra
quando o domínio existir.

---

## 1. Preparar a máquina

### 1.1 Atualizar

```bash
sudo apt update && sudo apt upgrade -y
```

### 1.2 Instalar o Docker

O script oficial do Docker reconhece distros derivadas: ele consulta a distro
*upstream* (`lsb_release -u`), e no Mint 22 isso devolve `ubuntu/noble`
sozinho. São duas linhas:

```bash
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh
```

<details>
<summary>Prefere o repositório na mão?</summary>

> **Cuidado com a pegadinha do Mint.** O comando do site do Docker lê o
> *codename* do sistema, e o Mint responde `wilma` — que não existe no
> repositório. Use `$UBUNTU_CODENAME`, que resolve para `noble`.

```bash
sudo apt install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
```

```bash
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$UBUNTU_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list
```

```bash
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

</details>

### 1.3 Usar o Docker sem `sudo`

```bash
sudo usermod -aG docker $USER
```

Saia da sessão e entre de novo (ou reinicie). Depois confirme:

```bash
docker run --rm hello-world
```

### 1.4 Impedir que a máquina durma

Servidor que suspende deixa o app fora do ar. O Mint suspende por padrão.

```bash
sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
```

Também vá em **Configurações > Gerenciador de energia** e deixe tudo como
"nunca". Se for notebook, ajuste "quando a tampa for fechada" para "não fazer
nada".

### 1.5 Voltar sozinho depois de queda de energia

No setup da BIOS procure algo como *Restore on AC Power Loss* / *After Power
Failure* e deixe em **Power On**. O Docker já sobe junto com o sistema
(`sudo systemctl enable docker` garante), e os containers têm
`restart: unless-stopped`.

### 1.6 Firewall

Com o túnel, **nada** precisa ficar aberto:

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw enable
```

Se mais tarde quiser entrar por SSH da sua própria rede:
`sudo ufw allow from 192.168.0.0/16 to any port 22 proto tcp`.

---

## 2. Subir a aplicação

### 2.1 Levar o código

Copie a pasta `quandodeu-api` (pendrive serve) para a máquina, em
`~/quantodeu-api`. Depois, quando houver um repositório Git, atualizar vira
`git pull`.

### 2.2 Criar o `.env`

```bash
cd ~/quantodeu-api
cp .env.production.example .env
```

Gere um segredo por vez e cole no arquivo (`SESSION_SECRETS`,
`VERIFICATION_PEPPER`, `POSTGRES_PASSWORD`, `APP_DB_PASSWORD`):

```bash
openssl rand -base64 48
```

Preencha também `SMTP_USERNAME`, `SMTP_PASSWORD` (senha de app do Google) e
`SMTP_FROM`.

```bash
nano .env
chmod 600 .env
```

### 2.3 Construir e subir

```bash
docker compose -f docker-compose.prod.yml build
docker compose -f docker-compose.prod.yml up -d
```

O `build` compila o Go dentro do container e, em PC antigo, pode levar vários
minutos na primeira vez. **Se a máquina tiver menos de 2 GB de RAM** e o build
morrer, veja "Construir a imagem em outra máquina", no fim.

### 2.4 Conferir

```bash
docker compose -f docker-compose.prod.yml ps
docker compose -f docker-compose.prod.yml logs -f api
curl -fsS http://127.0.0.1:8080/readyz && echo OK
```

A API não tem Go instalado na máquina, então os comandos de conferência rodam
pelo próprio container:

```bash
docker compose -f docker-compose.prod.yml run --rm api -check-config
docker compose -f docker-compose.prod.yml run --rm api -test-email=voce@gmail.com
```

O primeiro valida a configuração e aponta riscos; o segundo manda um e-mail de
verdade — é o teste que prova que o Gmail está aceitando a senha de app.

> **Testar pelo celular ainda não funciona aqui.** A API só escuta em
> `127.0.0.1` e o cookie de sessão é `Secure`: sem HTTPS o navegador (e o app)
> nem guardam o cookie. A validação nesta etapa é por `curl` na própria máquina;
> o app entra depois do túnel.

---

## 3. Domínio e túnel da Cloudflare

Domínio em uso: **pabloantonio.online**, registrado na Hostinger. A API vai
ficar em `api.pabloantonio.online`.

### 3.1 Levar o DNS para a Cloudflare

O túnel precisa que a **Cloudflare** seja a dona do DNS do domínio. O domínio
continua registrado (e pago) na Hostinger — muda só quem responde pelo DNS.

1. Crie a conta em `dash.cloudflare.com` (plano **Free**).
2. **Add a domain** > `pabloantonio.online` > selecione **Free** > *Continue*.
3. A Cloudflare importa os registros existentes e mostra **dois nameservers**,
   algo como `ana.ns.cloudflare.com` e `bob.ns.cloudflare.com`. Anote os dois.
4. No hPanel da Hostinger: **Domínios > pabloantonio.online > DNS / Nameservers
   > Alterar nameservers > Usar nameservers personalizados**. Apague os da
   Hostinger, cole os dois da Cloudflare e salve.
5. Volte na Cloudflare e clique em *Check nameservers*. Costuma levar de
   15 minutos a algumas horas; chega um e-mail quando ativa.

> Se algum dia você usar o domínio para e-mail ou site na Hostinger, confira
> antes se aqueles registros (MX, TXT) vieram junto na importação da Cloudflare
> — o que não estiver lá para de funcionar depois da troca.

### 3.3 Criar o túnel

1. No painel: **Zero Trust > Networks > Tunnels > Create a tunnel**.
2. Tipo **Cloudflared**, dê um nome (ex.: `casa`).
3. A tela mostra um comando de instalação — **ignore o comando** e copie só o
   **token** (o texto longo depois de `--token`). Ele é uma credencial: quem
   tiver o token consegue publicar no seu domínio.
4. Cole no `.env` da máquina:

```bash
nano .env      # CLOUDFLARE_TUNNEL_TOKEN=<cole aqui>
```

5. Ainda no painel, aba **Public Hostnames > Add a public hostname**:

| Campo | Valor |
|---|---|
| Subdomain | `api` |
| Domain | `pabloantonio.online` |
| Type | `HTTP` |
| URL | `api:8080` |

`api:8080` é o nome do serviço na rede do compose — não use `localhost`, que
dentro do container do túnel apontaria para ele mesmo.

### 3.4 Ligar

```bash
docker compose -f docker-compose.prod.yml --profile tunnel up -d
docker compose -f docker-compose.prod.yml logs -f cloudflared
```

Teste de fora (pode ser do celular, na rede móvel — desligue o Wi-Fi para ter
certeza de que não está passando pela rede de casa):

```
https://api.pabloantonio.online/readyz
```

Deve responder `{"status":"ok","checks":{"postgres":"ok"}}`.

O certificado HTTPS é emitido e renovado pela Cloudflare, sem nada a fazer.

---

## 4. Apontar o app

```bash
flutter build apk --release --dart-define=API_BASE_URL=https://api.pabloantonio.online/api/v1
```

---

## 5. Rotina

### Backup diário às 3h

```bash
crontab -e
```

```
0 3 * * * cd $HOME/quantodeu-api && ./deploy/backup.sh >> $HOME/backup.log 2>&1
```

Guarda 14 dias em `~/quantodeu-api/backups`. **Copie esses arquivos para fora da
máquina** (nuvem, outro HD) — backup que mora no mesmo disco não é backup.

Restaurar:

```bash
gunzip -c backups/quantodeu-2026-01-01-0300.sql.gz | docker exec -i quantodeu-db psql -U quantodeu -d quantodeu
```

### Atualizar a aplicação

```bash
cd ~/quantodeu-api
docker compose -f docker-compose.prod.yml up -d --build
```

As migrations rodam sozinhas no boot da API.

### Comandos do dia a dia

```bash
docker compose -f docker-compose.prod.yml ps        # o que está de pé
docker compose -f docker-compose.prod.yml logs -f api
docker compose -f docker-compose.prod.yml restart api
docker system df                                    # espaço ocupado
docker image prune -f                               # limpa imagens órfãs
```

---

## Construir a imagem em outra máquina

Se o PC não der conta de compilar, construa no Mac (que é ARM) mirando o
processador do PC (x86-64) e leve a imagem pronta:

```bash
# no Mac, dentro de quandodeu-api
docker buildx build --platform linux/amd64 -t quantodeu-api:prod --load .
docker save quantodeu-api:prod | gzip > quantodeu-api.tar.gz
```

Copie o arquivo para a máquina e:

```bash
gunzip -c quantodeu-api.tar.gz | docker load
```

Depois troque, no `docker-compose.prod.yml`, a linha `build: .` por
`image: quantodeu-api:prod`.
