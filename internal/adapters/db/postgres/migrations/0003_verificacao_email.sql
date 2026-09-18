-- 0003 — Verificação de e-mail
--
-- Contas criadas antes desta migration começam com e-mail NÃO verificado:
-- basta pedir um novo código em POST /api/v1/me/email/verificacao.

ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verificado_em TIMESTAMPTZ;

-- Desafio pendente (no máximo um por usuário). O código nunca é gravado em
-- claro: apenas o HMAC-SHA256 com pepper do servidor.
CREATE TABLE IF NOT EXISTS email_verifications (
    user_id     UUID         PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    email       VARCHAR(254) NOT NULL,
    code_hash   CHAR(64)     NOT NULL,
    attempts    INTEGER      NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at  TIMESTAMPTZ  NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT, UPDATE, DELETE ON email_verifications TO quantodeu_app;

ALTER TABLE email_verifications ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS email_verifications_isolamento ON email_verifications;
CREATE POLICY email_verifications_isolamento ON email_verifications
    USING (user_id = app_current_user_id())
    WITH CHECK (user_id = app_current_user_id());
