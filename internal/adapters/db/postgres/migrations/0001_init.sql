-- QuantoDeu — schema inicial
-- Valores monetários em CENTAVOS (BIGINT). Datas de competência em DATE.

CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid() (nativo no PG13+, mantido por compatibilidade)

-- ---------------------------------------------------------------------------
-- Usuários (tenant)
-- ---------------------------------------------------------------------------
CREATE TABLE users (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    nome                    VARCHAR(120) NOT NULL,
    email                   VARCHAR(254) NOT NULL,
    telefone                VARCHAR(13)  NOT NULL,
    password_hash           TEXT         NOT NULL,
    saldo_inicial_centavos  BIGINT       NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT users_email_lower_chk CHECK (email = lower(email)),
    CONSTRAINT users_telefone_chk    CHECK (telefone ~ '^55[1-9]{2}[0-9]{8,9}$')
);
CREATE UNIQUE INDEX users_email_uq    ON users (email);
CREATE UNIQUE INDEX users_telefone_uq ON users (telefone);

-- ---------------------------------------------------------------------------
-- Sessões (token armazenado apenas como SHA-256)
-- ---------------------------------------------------------------------------
CREATE TABLE sessions (
    token_hash    CHAR(64)     PRIMARY KEY,
    user_id       UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ  NOT NULL,
    last_seen_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    user_agent    VARCHAR(255) NOT NULL DEFAULT '',
    ip            VARCHAR(64)  NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- ---------------------------------------------------------------------------
-- Parcelamentos / despesas recorrentes
-- ---------------------------------------------------------------------------
CREATE TABLE parcelamentos (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tipo                    VARCHAR(20)  NOT NULL CHECK (tipo IN ('parcelado', 'recorrente')),
    descricao               VARCHAR(255) NOT NULL,
    categoria               VARCHAR(60)  NOT NULL,
    valor_total_centavos    BIGINT       NOT NULL CHECK (valor_total_centavos > 0),
    valor_parcela_centavos  BIGINT       NOT NULL CHECK (valor_parcela_centavos > 0),
    total_parcelas          INTEGER      NOT NULL CHECK (total_parcelas BETWEEN 1 AND 420),
    data_primeira_parcela   DATE         NOT NULL,
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- permite FK composta (id, user_id) em transacoes: garante que a parcela
    -- pertence ao MESMO usuário do parcelamento (defesa em profundidade).
    CONSTRAINT parcelamentos_id_user_uq UNIQUE (id, user_id)
);
CREATE INDEX parcelamentos_user_id_idx ON parcelamentos (user_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Transações
-- ---------------------------------------------------------------------------
CREATE TABLE transacoes (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tipo             VARCHAR(10)  NOT NULL CHECK (tipo IN ('entrada', 'saida')),
    valor_centavos   BIGINT       NOT NULL CHECK (valor_centavos > 0),
    categoria        VARCHAR(60)  NOT NULL,
    descricao        VARCHAR(255) NOT NULL DEFAULT '',
    data_transacao   DATE         NOT NULL,
    origem           VARCHAR(20)  NOT NULL CHECK (origem IN ('manual', 'whatsapp', 'parcelamento')),
    external_id      VARCHAR(128),
    parcelamento_id  UUID,
    numero_parcela   INTEGER,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT transacoes_parcelamento_fk FOREIGN KEY (parcelamento_id, user_id)
        REFERENCES parcelamentos (id, user_id) ON DELETE CASCADE,
    CONSTRAINT transacoes_parcela_chk CHECK (
        (parcelamento_id IS NULL AND numero_parcela IS NULL) OR
        (parcelamento_id IS NOT NULL AND numero_parcela >= 1)
    )
);

-- Consultas sempre começam por user_id (isolamento multi-tenant).
CREATE INDEX transacoes_user_data_idx      ON transacoes (user_id, data_transacao DESC, created_at DESC);
CREATE INDEX transacoes_user_categoria_idx ON transacoes (user_id, categoria, data_transacao);
CREATE INDEX transacoes_parcelamento_idx   ON transacoes (user_id, parcelamento_id) WHERE parcelamento_id IS NOT NULL;

-- Idempotência do webhook: a mesma mensagem não gera dois lançamentos.
CREATE UNIQUE INDEX transacoes_external_id_uq ON transacoes (user_id, origem, external_id)
    WHERE external_id IS NOT NULL;
