#!/usr/bin/env bash
# Executado UMA vez pelo container oficial do PostgreSQL ao criar o volume.
# Cria:
#   quantodeu_app  -> role sem login que recebe os GRANTs (migration 0002)
#   quantodeu_api  -> usuário de login da API, membro de quantodeu_app,
#                     NÃO superuser, NÃO dono das tabelas => sujeito ao RLS
set -euo pipefail

: "${APP_DB_USER:=quantodeu_api}"
: "${APP_DB_PASSWORD:?defina APP_DB_PASSWORD}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v app_user="$APP_DB_USER" -v app_pass="$APP_DB_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE quantodeu_app NOLOGIN NOSUPERUSER NOBYPASSRLS')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'quantodeu_app') \gexec

SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE IN ROLE quantodeu_app', :'app_user', :'app_pass')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app_user') \gexec

-- Evita criar objetos no schema public com a role da API.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SQL
echo "roles quantodeu_app e ${APP_DB_USER} prontas"
