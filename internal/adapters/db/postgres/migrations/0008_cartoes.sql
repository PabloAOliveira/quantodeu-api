-- 0008 — Cartão de crédito
--
-- A compra no cartão não sai da conta: ela vira dívida com o banco e só mexe no
-- saldo quando a fatura é paga. Por isso as compras ficam FORA de `transacoes`
-- — lá vale a regra "toda linha mexe no saldo", e o cálculo de saldo depende
-- dela. Misturar as duas obrigaria a filtrar em toda consulta de saldo, o tipo
-- de exceção que escapa num canto e vira erro de dinheiro.
--
-- Quem entra em `transacoes` é o PAGAMENTO da fatura: uma saída na data em que
-- o dinheiro saiu, ligada aqui por `pagamentos_fatura`.

-- A FK composta de pagamentos_fatura precisa disto. Mesma defesa que
-- parcelamentos já usa: garante que a transação do pagamento é do MESMO dono
-- do cartão, sem depender de o código acertar o WHERE.
ALTER TABLE transacoes ADD CONSTRAINT transacoes_id_user_uq UNIQUE (id, user_id);

CREATE TABLE cartoes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    nome            VARCHAR(60) NOT NULL,
    banco           VARCHAR(60) NOT NULL DEFAULT '',
    dia_fechamento  SMALLINT    NOT NULL CHECK (dia_fechamento BETWEEN 1 AND 31),
    dia_vencimento  SMALLINT    NOT NULL CHECK (dia_vencimento BETWEEN 1 AND 31),
    limite_centavos BIGINT      NOT NULL DEFAULT 0 CHECK (limite_centavos >= 0),
    ativo           BOOLEAN     NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- permite a FK composta em compras_cartao: a compra é do MESMO dono do
    -- cartão (defesa em profundidade, como já se faz em parcelamentos).
    CONSTRAINT cartoes_id_user_uq UNIQUE (id, user_id)
);
CREATE INDEX cartoes_user_id_idx ON cartoes (user_id, created_at DESC);

-- Uma linha por PARCELA da compra. Compra em 3x = 3 linhas com o mesmo
-- grupo_id. Assim o total da fatura é uma soma por período, sem expandir
-- compras a cada consulta.
CREATE TABLE compras_cartao (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    cartao_id         UUID         NOT NULL,
    grupo_id          UUID         NOT NULL,
    valor_centavos    BIGINT       NOT NULL CHECK (valor_centavos > 0),
    categoria         VARCHAR(60)  NOT NULL,
    descricao         VARCHAR(255) NOT NULL DEFAULT '',
    data_compra       DATE         NOT NULL,
    numero_parcela    SMALLINT     NOT NULL CHECK (numero_parcela >= 1),
    total_parcelas    SMALLINT     NOT NULL CHECK (total_parcelas BETWEEN 1 AND 36),
    -- Em qual fatura esta parcela caiu. Gravado, não derivado: é fato
    -- consumado — mudar o dia de fechamento amanhã não remaneja o que já caiu
    -- numa fatura fechada.
    fatura_vencimento DATE         NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT compras_cartao_fk FOREIGN KEY (cartao_id, user_id)
        REFERENCES cartoes (id, user_id) ON DELETE CASCADE
);
CREATE INDEX compras_cartao_fatura_idx ON compras_cartao (user_id, cartao_id, fatura_vencimento);
CREATE INDEX compras_cartao_grupo_idx  ON compras_cartao (user_id, grupo_id);

-- O único fato da fatura que precisa ser gravado: o resto é derivado das
-- compras. Uma fatura por cartão por vencimento.
CREATE TABLE pagamentos_fatura (
    user_id        UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    cartao_id      UUID        NOT NULL,
    vencimento     DATE        NOT NULL,
    transacao_id   UUID        NOT NULL,
    valor_centavos BIGINT      NOT NULL CHECK (valor_centavos > 0),
    pago_em        DATE        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (cartao_id, vencimento),
    CONSTRAINT pagamentos_fatura_cartao_fk FOREIGN KEY (cartao_id, user_id)
        REFERENCES cartoes (id, user_id) ON DELETE CASCADE,
    CONSTRAINT pagamentos_fatura_transacao_fk FOREIGN KEY (transacao_id, user_id)
        REFERENCES transacoes (id, user_id) ON DELETE CASCADE
);

GRANT SELECT, INSERT, UPDATE, DELETE ON cartoes, compras_cartao, pagamentos_fatura TO quantodeu_app;

ALTER TABLE cartoes            ENABLE ROW LEVEL SECURITY;
ALTER TABLE compras_cartao     ENABLE ROW LEVEL SECURITY;
ALTER TABLE pagamentos_fatura  ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS cartoes_isolamento ON cartoes;
CREATE POLICY cartoes_isolamento ON cartoes
    USING (user_id = app_current_user_id()) WITH CHECK (user_id = app_current_user_id());

DROP POLICY IF EXISTS compras_cartao_isolamento ON compras_cartao;
CREATE POLICY compras_cartao_isolamento ON compras_cartao
    USING (user_id = app_current_user_id()) WITH CHECK (user_id = app_current_user_id());

DROP POLICY IF EXISTS pagamentos_fatura_isolamento ON pagamentos_fatura;
CREATE POLICY pagamentos_fatura_isolamento ON pagamentos_fatura
    USING (user_id = app_current_user_id()) WITH CHECK (user_id = app_current_user_id());
