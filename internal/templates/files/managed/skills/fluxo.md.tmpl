---
name: fluxo
description: Conduz uma demanda pelo fluxo completo de subagentes, do pm-triage ao git-delivery, respeitando os sinais do contrato de handoff. Use para começar qualquer demanda técnica.
argument-hint: <demanda>
---

# Fluxo de delivery

Demanda: $ARGUMENTS

Você orquestra; quem trabalha são os subagentes. A sessão principal não edita
código protegido (o hook bloqueia) nem arquivos de governança.

## Ordem

```text
pm-triage
  → architecture-guardrails
  → [data-model]
  → test-writer
  → dev-implementation
  → code-reviewer
  → [migration-reviewer] [query-performance] [tenant-isolation]
  → security-review        (SECURITY: sim, ou diff em security_surface_paths)
  → supply-chain-review    (SUPPLY_CHAIN: sim, ou go.mod/go.sum no diff)
  → qa-validation
  → [release-ops]
  → docs-update
  → git-delivery
```

Colchetes são módulos opcionais: só existem se estiverem em `enabled_modules`
no `.pipeline-claude.json` (o subagente correspondente está em
`.claude/agents/`). `agent-auditor` e `context-loader` são acionados sob
demanda.

## Regras

1. Antes de tudo, `git fetch` (a `base_ref` do `.pipeline-claude.json` é, por
   padrão, `origin/main`) e depois a ferramenta MCP `pipeline_index_sync`. Ela
   marca como `merged`, no índice e no report, cada delivery `draft` ou
   `em revisão` cujo commit `[<ID>]` já chegou à `base_ref`, calculado pelo
   git. Se ela falhar, mostre o erro ao usuário e pare; não edite o índice à
   mão.
2. Comece pelo `pm-triage` com a demanda acima. Ele cria a delivery e devolve o
   `DELIVERY_ID`.
3. Cada subagente grava o próprio handoff
   (`docs-ai/agents/08-HANDOFF-CONTRACT.md`) com `pipeline_handoff_put` e
   devolve o caminho, o `STATUS`, o `PROXIMO` e os `SINAIS`. Passe ao próximo o
   `DELIVERY_ID` e o caminho do handoff anterior, não um resumo: ele lê o
   arquivo com `Read` ou `pipeline_handoff_get`. Siga o `PROXIMO`, desde que
   respeite a ordem acima. A sessão principal não escreve nem reescreve handoff:
   o que não estiver no arquivo, peça ao subagente que o gravou.
4. `STATUS: bloqueado` interrompe: leve o `MOTIVO` ao subagente indicado em
   `PROXIMO` ou ao usuário, se for decisão humana.
5. `security-review` roda uma vez, depois do `code-reviewer`, quando o sinal ou
   o diff exigir. Achado de revisor segue o ciclo de correção abaixo; revisor
   não corrige.
6. Não pule `qa-validation` nem `docs-update`. O `git-delivery` só roda com o
   validador aprovado.
7. Ao final, resuma para o usuário: `DELIVERY_ID`, o que mudou, a saída do
   validador e o link do PR/MR.

## Ciclo de correção

Quando um revisor devolve `STATUS: bloqueado` com achado no código:

1. `test-writer` primeiro: um teste que falha e reproduz o achado, quando ele
   for testável (defeito de comportamento). Achado só de estilo ou de nome pula
   este passo.
2. `dev-implementation` corrige até o teste novo e os sensores passarem.
3. O mesmo revisor roda de novo sobre o diff atualizado, com o caminho do
   handoff do bloqueio, e confirma que o bloqueio caiu. Só então o fluxo segue
   para o próximo da ordem.

Os revisores depois dele na ordem ainda não rodaram e veem o diff já
corrigido.

Em paralelo: depois do `code-reviewer` aprovar (e do `query-performance`, que
edita código e roda antes, sozinho), os revisores só leitura que se aplicam
(`migration-reviewer`, `tenant-isolation`, `security-review`,
`supply-chain-review`) podem rodar ao mesmo tempo sobre o mesmo diff: nenhum
edita o projeto, e cada um grava só o próprio handoff (o servidor numera sem
colisão). Passe a todos o caminho do handoff do `code-reviewer`. Bloqueio de
qualquer um deles abre o ciclo acima, e todos rodam de novo sobre o diff
corrigido. `qa-validation` espera todos e recebe o caminho de cada handoff.
