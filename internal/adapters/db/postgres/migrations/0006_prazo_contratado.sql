-- 0006 — Prazo contratado do financiamento
--
-- Prazo do contrato. Com aporte, o parcelamento passa a ter menos parcelas que
-- isto (quita antes), mas o cronograma precisa do prazo original: é ele que
-- define a parcela base do SAC (valor financiado ÷ prazo contratado).
-- Zero mantém o comportamento antigo (usa o total de parcelas do parcelamento).
ALTER TABLE financiamentos
    ADD COLUMN IF NOT EXISTS prazo_contratado INT NOT NULL DEFAULT 0
        CHECK (prazo_contratado >= 0);
