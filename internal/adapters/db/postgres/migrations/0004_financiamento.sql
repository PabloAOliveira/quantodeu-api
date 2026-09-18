-- 0004 — Financiamento (projeção de parcelas por juros)
--
-- 1:1 opcional com parcelamentos. Guardar o contrato permite recalcular as
-- parcelas quando o usuário edita prazo, taxa ou sistema de amortização.

CREATE TABLE IF NOT EXISTS financiamentos (
    parcelamento_id           UUID         PRIMARY KEY,
    user_id                   UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    banco                     VARCHAR(120) NOT NULL DEFAULT '',
    sistema                   VARCHAR(10)  NOT NULL CHECK (sistema IN ('price', 'sac')),
    valor_financiado_centavos BIGINT       NOT NULL CHECK (valor_financiado_centavos > 0),
    taxa_juros_anual          NUMERIC(7,4) NOT NULL CHECK (taxa_juros_anual >= 0 AND taxa_juros_anual <= 100),
    tipo_taxa                 VARCHAR(10)  NOT NULL CHECK (tipo_taxa IN ('efetiva', 'nominal')),
    created_at                TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT financiamentos_parcelamento_fk FOREIGN KEY (parcelamento_id, user_id)
        REFERENCES parcelamentos (id, user_id) ON DELETE CASCADE
);

GRANT SELECT, INSERT, UPDATE, DELETE ON financiamentos TO quantodeu_app;

ALTER TABLE financiamentos ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS financiamentos_isolamento ON financiamentos;
CREATE POLICY financiamentos_isolamento ON financiamentos
    USING (user_id = app_current_user_id())
    WITH CHECK (user_id = app_current_user_id());
