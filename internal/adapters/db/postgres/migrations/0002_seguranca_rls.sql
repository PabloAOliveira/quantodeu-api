-- QuantoDeu — 0002: verificação de telefone, bloqueio de login e Row-Level Security
--
-- MODELO DE ROLES
--   * Migrator (dono das tabelas): executa as migrations (DATABASE_MIGRATION_URL).
--   * quantodeu_app (NOLOGIN, sem BYPASSRLS): recebe os GRANTs. O usuário de
--     login da API (DATABASE_URL) deve ser MEMBRO desta role e NÃO pode ser
--     superuser nem dono das tabelas — caso contrário o RLS é ignorado.
--
-- O RLS usa a variável de sessão app.user_id, definida pela aplicação com
-- set_config('app.user_id', <uuid>, true) no início de CADA transação
-- (escopo local à transação: não vaza entre conexões do pool).

-- ---------------------------------------------------------------------------
-- Verificação de telefone
-- ---------------------------------------------------------------------------
ALTER TABLE users ADD COLUMN telefone_verificado_em TIMESTAMPTZ;

CREATE TABLE phone_verifications (
    user_id     UUID        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    telefone    VARCHAR(13) NOT NULL,
    metodo      VARCHAR(10) NOT NULL CHECK (metodo IN ('codigo', 'mensagem')),
    code_hash   CHAR(64)    NOT NULL,
    attempts    INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Tentativas de login (bloqueio progressivo). A chave é SHA-256 do e-mail
-- normalizado: não armazena e-mails de terceiros em claro.
-- ---------------------------------------------------------------------------
CREATE TABLE login_attempts (
    key_hash          CHAR(64)    PRIMARY KEY,
    failures          INTEGER     NOT NULL CHECK (failures >= 0),
    first_failure_at  TIMESTAMPTZ NOT NULL,
    locked_until      TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX login_attempts_updated_at_idx ON login_attempts (updated_at);

-- ---------------------------------------------------------------------------
-- Role da aplicação
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'quantodeu_app') THEN
        CREATE ROLE quantodeu_app NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO quantodeu_app;
GRANT SELECT, INSERT, UPDATE, DELETE
    ON users, sessions, parcelamentos, transacoes, phone_verifications, login_attempts
    TO quantodeu_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO quantodeu_app;

-- ---------------------------------------------------------------------------
-- Row-Level Security
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION app_current_user_id() RETURNS UUID
    LANGUAGE sql STABLE PARALLEL SAFE
AS $$ SELECT NULLIF(current_setting('app.user_id', true), '')::uuid $$;

ALTER TABLE users               ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions            ENABLE ROW LEVEL SECURITY;
ALTER TABLE parcelamentos       ENABLE ROW LEVEL SECURITY;
ALTER TABLE transacoes          ENABLE ROW LEVEL SECURITY;
ALTER TABLE phone_verifications ENABLE ROW LEVEL SECURITY;

-- Sem app.user_id definido, app_current_user_id() é NULL e nenhuma linha
-- passa (NULL = x nunca é verdadeiro): o padrão é NEGAR.
CREATE POLICY users_isolamento ON users
    USING (id = app_current_user_id())
    WITH CHECK (id = app_current_user_id());

CREATE POLICY sessions_isolamento ON sessions
    USING (user_id = app_current_user_id())
    WITH CHECK (user_id = app_current_user_id());

CREATE POLICY parcelamentos_isolamento ON parcelamentos
    USING (user_id = app_current_user_id())
    WITH CHECK (user_id = app_current_user_id());

CREATE POLICY transacoes_isolamento ON transacoes
    USING (user_id = app_current_user_id())
    WITH CHECK (user_id = app_current_user_id());

CREATE POLICY phone_verifications_isolamento ON phone_verifications
    USING (user_id = app_current_user_id())
    WITH CHECK (user_id = app_current_user_id());

-- ---------------------------------------------------------------------------
-- Funções SECURITY DEFINER: as ÚNICAS leituras sem tenant conhecido.
-- Executam com o privilégio do dono (que não está sujeito ao RLS), devolvem o
-- mínimo necessário e fixam search_path contra sequestro de objetos.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION qd_find_user_by_email(p_email TEXT) RETURNS SETOF users
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$ SELECT * FROM users WHERE email = lower(p_email) LIMIT 1 $$;

CREATE OR REPLACE FUNCTION qd_find_user_by_phones(p_phones TEXT[]) RETURNS SETOF users
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
    SELECT * FROM users
    WHERE telefone = ANY (p_phones)
    ORDER BY array_position(p_phones, telefone::text)
    LIMIT 1
$$;

CREATE OR REPLACE FUNCTION qd_find_session(p_hash TEXT) RETURNS SETOF sessions
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$ SELECT * FROM sessions WHERE token_hash = p_hash AND expires_at > now() $$;

CREATE OR REPLACE FUNCTION qd_touch_session(p_hash TEXT, p_seen TIMESTAMPTZ) RETURNS VOID
    LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp
AS $$ UPDATE sessions SET last_seen_at = p_seen WHERE token_hash = p_hash $$;

CREATE OR REPLACE FUNCTION qd_delete_session(p_hash TEXT) RETURNS VOID
    LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp
AS $$ DELETE FROM sessions WHERE token_hash = p_hash $$;

CREATE OR REPLACE FUNCTION qd_delete_expired_sessions(p_now TIMESTAMPTZ) RETURNS BIGINT
    LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp
AS $$ WITH d AS (DELETE FROM sessions WHERE expires_at <= p_now RETURNING 1) SELECT count(*) FROM d $$;

REVOKE ALL ON FUNCTION
    qd_find_user_by_email(TEXT), qd_find_user_by_phones(TEXT[]), qd_find_session(TEXT),
    qd_touch_session(TEXT, TIMESTAMPTZ), qd_delete_session(TEXT), qd_delete_expired_sessions(TIMESTAMPTZ)
    FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
    app_current_user_id(),
    qd_find_user_by_email(TEXT), qd_find_user_by_phones(TEXT[]), qd_find_session(TEXT),
    qd_touch_session(TEXT, TIMESTAMPTZ), qd_delete_session(TEXT), qd_delete_expired_sessions(TIMESTAMPTZ)
    TO quantodeu_app;
