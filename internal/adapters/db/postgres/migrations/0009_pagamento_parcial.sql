-- Pagamento parcial da fatura.
--
-- A fatura vinha com no máximo um pagamento (PK em cartao_id+vencimento), então
-- pagar R$ 300 de uma fatura de R$ 500 marcava tudo como pago. Na vida real o
-- pagamento parcial é comum — e cada parcela dele sai da conta num dia
-- diferente, às vezes em meses diferentes. Por regime de caixa, cada uma
-- precisa da sua própria linha e da sua própria transação.

ALTER TABLE pagamentos_fatura DROP CONSTRAINT pagamentos_fatura_pkey;
ALTER TABLE pagamentos_fatura ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE pagamentos_fatura ADD CONSTRAINT pagamentos_fatura_pkey PRIMARY KEY (id);

-- Era a PK que servia de índice para o join com as compras.
CREATE INDEX pagamentos_fatura_fatura_idx
    ON pagamentos_fatura (user_id, cartao_id, vencimento, pago_em DESC);

-- Uma transação de saída lastreia um pagamento e só um.
ALTER TABLE pagamentos_fatura ADD CONSTRAINT pagamentos_fatura_transacao_uq UNIQUE (transacao_id);
