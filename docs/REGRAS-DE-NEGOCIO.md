# QuantoDeu API — Regras de negócio

As decisões de domínio que não são óbvias no código: o que o número significa,
e por que ele é calculado assim. A especificação técnica das camadas está em
[`ARQUITETURA.md`](ARQUITETURA.md).

## Parcelamento que começou antes do cadastro

`POST`/`PUT /parcelamentos` aceitam `parcelas_ja_pagas` (padrão 0): quantas das
primeiras parcelas já foram quitadas **fora do app**.

Um financiamento cadastrado hoje com `data_primeira_parcela` em abril gerava
lançamento para abril, maio, junho… — dinheiro que já saiu da conta antes e que
o saldo informado no cadastro já descontava. O saldo ficava negativo por uma
dívida paga.

Com `parcelas_ja_pagas: 6` a API:

- mantém o cronograma inteiro (é ele que define a parcela base do SAC, o
  `valor_total` e o `data_ultima_parcela`);
- **não grava** as 6 primeiras como transação, então elas não mexem no saldo;
- começa os lançamentos na 7ª, com a numeração do contrato (`(7/360)`);
- soma essas 6 no `progresso` como pagas — `valor_pago` inclui o valor delas,
  tirado do cronograma.

Limite: `0 <= parcelas_ja_pagas < total_parcelas` (com aporte extra, o total
válido é o prazo encurtado). **No PUT, omitir o campo volta para zero** e as
parcelas antigas são lançadas de novo — a mesma pegadinha do bloco
`financiamento`: quem edita reenvia.

## Cartão de crédito

A compra no cartão **não mexe no saldo**: ela é dívida com o banco. O dinheiro
só se move quando a fatura é paga — e é isso que resolve o descompasso entre
comprar num mês e pagar no outro.

```
POST /cartoes/{id}/compras                 -> não altera saldo nem despesas
POST /cartoes/{id}/faturas/2026-09/pagar   -> cria UMA saída na data do pagamento
```

A saída nasce na **data do pagamento**, não no vencimento nem no mês das
compras. Por isso a fatura de setembro paga em outubro aparece nas despesas de
outubro: foi em outubro que o dinheiro saiu da conta.

Três regras que valem a pena conhecer:

- **Vencimento é a próxima ocorrência do dia após o fechamento.** O mesmo par de
  campos cobre "fecha 20, vence 21" (mesmo mês) e "fecha 28, vence 5" (mês
  seguinte), sem perguntar nada a mais ao usuário.
- **Compra depois do fechamento cai na fatura seguinte.** É a regra que mais
  confunde na vida real e a que tem teste dedicado, incluindo um que percorre
  todos os dias de um ano em quatro configurações de cartão para garantir que
  nenhum dia fique fora de um ciclo — nem em dois.
- **A fatura é derivada**, nunca armazenada: ela sai do cartão + das compras,
  como o progresso do parcelamento. O único fato gravado é o pagamento.
- **Quem manda na despesa é o valor pago**, não o total da fatura: o banco cobra
  o que quer, e o `valor_pago` volta na resposta para a tela não anunciar um
  número que não saiu da conta.
- **Pagar menos que o total deixa a fatura `parcial`**, não paga. Ela aceita
  quantos pagamentos precisar — cada um com a sua data e a sua saída, porque
  metade agora e metade no mês que vem são duas despesas de meses diferentes.
  `restante` diz o que falta; sem `valor` no corpo, pagar quita justamente isso.
  `DELETE .../pagar` desfaz só o pagamento mais recente.
- **Cartão arquivado (`ativo=false`) não aceita compra nova** (409
  `cartao_arquivado`). Arquivar é dizer "não uso mais"; as faturas antigas ficam.

As compras ficam em `compras_cartao`, fora de `transacoes`, justamente porque lá
vale "toda linha mexe no saldo" e `SaldoAte` depende disso.

## Agenda de vencimentos

`GET /agenda?meses=6` devolve, numa consulta só, o que ainda vai vencer:
parcelas de parcelamentos/financiamentos (que já existem como lançamentos
futuros) e faturas de cartão com saldo a pagar, ordenadas por data.

Existe para o app agendar as notificações **no aparelho** — sem isso ele
pediria o resumo de cada mês e as faturas de cada cartão só para descobrir as
datas. O horizonte é limitado a 12 meses de propósito: um financiamento de 360
parcelas encheria sozinho o limite de alarmes do Android.

O bloco de cartões é opcional ali dentro: se ele falhar, as parcelas continuam
saindo — um lembrete a menos é melhor que agenda nenhuma.
