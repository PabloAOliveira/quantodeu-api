-- 0005 — Amortização extraordinária mensal
--
-- Quem paga um valor fixo a mais todo mês abate o saldo devedor mais rápido:
-- o prazo encurta e o total de juros cai. Guardar o aporte no contrato faz as
-- parcelas gravadas refletirem o que é pago de verdade, em vez de deixar o
-- fluxo de caixa sempre defasado do valor real.
--
-- Zero (o padrão) mantém o comportamento anterior: paga-se só a parcela.

ALTER TABLE financiamentos
    ADD COLUMN IF NOT EXISTS pagamento_extra_centavos BIGINT NOT NULL DEFAULT 0
        CHECK (pagamento_extra_centavos >= 0);
