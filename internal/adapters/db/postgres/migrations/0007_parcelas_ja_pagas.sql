-- 0007 — Parcelas já pagas antes do cadastro
--
-- Quem cadastra um financiamento que começou meses atrás informa a data da 1ª
-- parcela real (ex.: abril), mas as parcelas de abril até hoje já saíram da
-- conta dele fora do app — o saldo informado no cadastro já está descontado
-- delas. Gerar essas parcelas como lançamento cobrava tudo de novo e deixava o
-- saldo negativo.
--
-- O cronograma continua inteiro (é ele que define a parcela do SAC e o total do
-- contrato); o que muda é que as N primeiras não viram transação: entram só no
-- progresso, como pagas.
--
-- Zero (o padrão) mantém o comportamento anterior.
ALTER TABLE parcelamentos
    ADD COLUMN IF NOT EXISTS parcelas_ja_pagas INT NOT NULL DEFAULT 0
        CHECK (parcelas_ja_pagas >= 0);
