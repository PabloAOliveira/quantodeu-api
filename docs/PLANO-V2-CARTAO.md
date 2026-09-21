# v2 — Cartão de crédito

> **Status: pronto** (21/09/2026). API — domínio, migration `0008_cartoes`,
> repositório, serviço, endpoints e bloco no `/resumo`. App — aba Cartões,
> cadastro, lançamento de compra, tela da fatura e bloco na Home.

## A regra que manda em tudo

**A fatura vira despesa no dia em que você paga.** Não no mês das compras.

Nas palavras do Pablo:

> "Dia 20 desse mês eu tenho minha fatura, vence dia 21, então vou pagar dia 20.
> Depois de pagar eu quero ir no app e marcar que a fatura foi paga. Deu R$ 600,
> então o gasto de R$ 600 vai pro histórico de gasto desse mês, mesmo que a
> fatura seja do mês passado, porque o dinheiro saiu da minha conta esse mês."

Isso é regime de **caixa**, que é o mesmo do resto do app: o saldo só muda
quando o dinheiro se move. Consequências:

- compra no cartão **não** mexe no saldo nem nas despesas do mês;
- marcar a fatura como paga cria **uma** saída na data do pagamento — é ela
  que aparece no histórico (`-R$ 600,00 · Fatura cartão de crédito`);
- o que você gasta no cartão este mês só entra nos custos do mês que vem,
  quando aquela fatura for paga.

## Cadastro do cartão

| campo | por quê |
|---|---|
| `nome` | "Nubank", "Inter Black" — o que você reconhece na lista |
| `banco` | opcional, informativo |
| `dia_fechamento` | 1–31: fecha o ciclo de compras |
| `dia_vencimento` | 1–31: prazo de pagamento |
| `limite` | opcional, habilita o "limite disponível" |
| `ativo` | arquivar sem perder histórico |

Dias 29–31 em meses curtos caem no último dia do mês — a mesma regra que as
parcelas já usam (`AddMonthsClamped`).

**Vencimento é sempre a próxima ocorrência do dia depois do fechamento.** Isso
cobre os dois formatos que existem no mercado sem campo extra: fechou dia 20 e
vence dia 21 → mesmo mês; fechou dia 28 e vence dia 5 → mês seguinte. O
intervalo típico é de 7 a 10 dias, mas varia por banco.

### Dois números que saem de graça

- **Limite disponível** = limite − (faturas não pagas + compras da fatura em
  aberto). É o que todo app de cartão mostra e o banco não deixa claro.
- **Melhor dia de compra** = o dia seguinte ao fechamento. Comprando nele você
  ganha o maior prazo possível (até ~40 dias) para pagar. É a pergunta que todo
  mundo faz e ninguém lembra a resposta.

## Modelo de dados

### Compra no cartão — tabela própria

`compras_cartao`, **não** `transacoes`. Hoje vale a regra "toda transação mexe
no saldo", e `SaldoAte` depende dela. Enfiar compra de cartão ali obrigaria a
filtrar em toda consulta de saldo — o tipo de exceção que escapa num canto e
vira erro de dinheiro.

Campos: valor, categoria, descrição, data da compra, cartão, nº de parcelas.
Compra em 3x gera 3 linhas, uma por fatura consecutiva.

**Compra depois do fechamento cai na fatura seguinte** — é a regra que mais
confunde na vida real, e a que precisa de teste dedicado.

### Fatura — derivada, não armazenada

Calculada do cartão + compras, como o progresso do parcelamento já é hoje.
Assim nunca fica dessincronizada.

- competência: `2026-10` (o mês do vencimento)
- período: `(fechamento anterior, fechamento atual]`
- total: soma das parcelas que caem no período
- estado: **aberta** (ainda acumulando) · **fechada** (passou o fechamento,
  aguarda pagamento) · **paga**

O pagamento é o único fato que precisa ser gravado: uma linha ligando
`(cartao_id, competência)` à `transacao` de saída criada.

## Endpoints

```
GET    /cartoes                    lista + fatura aberta, fatura a pagar, limite disponível
POST   /cartoes
GET    /cartoes/{id}
PUT    /cartoes/{id}
DELETE /cartoes/{id}               arquiva se houver histórico

GET    /cartoes/{id}/faturas       histórico por competência
GET    /cartoes/{id}/faturas/{ref} detalhe com as compras (ref = 2026-10)
POST   /cartoes/{id}/faturas/{ref}/pagar   { data, valor } -> cria a saída
DELETE /cartoes/{id}/faturas/{ref}/pagar   desfaz (remove a saída)

GET    /cartoes/{id}/compras
POST   /cartoes/{id}/compras       { valor, categoria, descricao, data, parcelas }
PUT    /compras/{id}
DELETE /compras/{id}
```

Bloco novo no `/resumo`:

```json
"cartoes": [{
  "cartao": "Nubank",
  "fatura_a_pagar":   { "competencia": "2026-10", "vencimento": "2026-10-21",
                        "total": {...}, "paga": false },
  "fatura_em_aberto": { "competencia": "2026-11", "vencimento": "2026-11-21",
                        "total": {...} },
  "limite_disponivel": {...}
}]
```

## Fora da v2

- **Pagamento parcial / rotativo.** Pagar o mínimo e rolar o resto puxa juros
  compostos e virada de saldo devedor entre faturas: é outro projeto.
- **Importar fatura do banco** (Open Finance, OFX).
- **Alertas de vencimento** — depende de push, que o app ainda não tem.

## Ordem de execução

1. ✅ **API** — migration (`cartoes`, `compras_cartao`, `pagamentos_fatura`),
   domínio da fatura com testes de virada de mês e compra pós-fechamento,
   endpoints, bloco no resumo.
2. ✅ **App** — aba Cartões, cadastro, lançar compra, tela da fatura com botão
   "Marcar como paga", bloco na Home.
3. ⬜ **v3** — bot do WhatsApp.

## O que a revisão encontrou (e consertou)

**Duas faturas colapsando numa.** A regra intuitiva — "o vencimento é a próxima
ocorrência do dia depois do fechamento" — parece equivalente a um deslocamento
fixo de meses, mas não é: quando os dias grampeiam em meses curtos, dois
fechamentos diferentes podem cair no mesmo vencimento, e aí um mês inteiro de
compras soma no outro. Acontecia em cartões de fim de mês (fecha 31 vence 30,
fecha 30 vence 31).

Agora o deslocamento é decidido uma vez pelos dias configurados (mesmo mês se o
vencimento vem depois do fechamento; mês seguinte caso contrário), o que torna a
correspondência fechamento ↔ fatura injetiva por construção. Há teste varrendo
**as 961 combinações** de dias, conferindo que cada fechamento gera uma fatura
distinta e que o caminho de volta devolve o mesmo fechamento.

**N+1 na Home.** O resumo e o histórico faziam duas consultas por fatura — com
um ano de cartão, ~24 idas ao banco a cada abertura. Viraram uma consulta
agregada só (`ResumoDasFaturas`).

**Compras mexendo em fatura paga.** Dava para lançar ou apagar compra numa
fatura já paga, mudando um total que já tinha virado dinheiro na conta. Agora
devolve 409 `fatura_fechada_para_compra`: o caminho é desfazer o pagamento,
ajustar e pagar de novo.

## Limitação conhecida

Mudar o dia de fechamento ou de vencimento de um cartão **não remaneja** as
compras já lançadas (isso é proposital: fatura passada é fato consumado), mas o
intervalo do ciclo exibido nas faturas antigas passa a ser calculado com os dias
novos. O total e as compras continuam certos; só as datas de início e fim do
ciclo antigo ficam deslocadas.

## Detalhes decididos na implementação

- **`fatura_em_aberto` some quando é a mesma que a `fatura_a_pagar`.** Antes do
  fechamento as duas são o mesmo ciclo, e repetir o número em dois campos só
  confundiria.
- **Excluir cartão com histórico é bloqueado** (409 `cartao_com_historico`): o
  caminho é arquivar (`PUT` com `ativo: false`), para não apagar o passado.
- **Desfazer pagamento** remove a saída do saldo junto — as duas coisas vivem na
  mesma transação de banco, nos dois sentidos.
- **`POST .../pagar` aceita requisição sem corpo**, usando hoje e o total da
  fatura como padrão.

## Fontes consultadas

- [Nubank — data de vencimento e fechamento](https://blog.nubank.com.br/data-de-vencimento-data-fechamento-cartao-de-credito/)
- [Serasa — fechamento da fatura](https://www.serasa.com.br/minhas-contas/blog/fechamento-fatura/)
- [Mobills — melhor dia de compra](https://www.mobills.com.br/blog/cartao-de-credito/melhor-dia-de-compra-cartao-de-credito/)
- [Organizze — controle de gastos no cartão](https://www.organizze.com.br/blog/controle-de-gastos/app-controle-de-gastos-cartao-de-credito)
