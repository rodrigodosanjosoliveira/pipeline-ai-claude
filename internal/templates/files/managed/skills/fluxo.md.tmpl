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

1. Antes de tudo, confira quais deliveries do índice (`index_file` no
   `.pipeline-claude.json`) com status `draft` ou `em revisão` já chegaram à
   base: `git fetch`, e para cada ID
   `git log --fixed-strings --grep "[<ID>]" --oneline <base_ref>`. Commit
   encontrado é delivery mesclada. Guarde a lista como `MESCLADAS` para o
   `docs-update`; não edite o índice.
2. Comece pelo `pm-triage` com a demanda acima. Ele cria a delivery e devolve o
   `DELIVERY_ID`.
3. Passe a cada subagente o handoff do anterior
   (`docs-ai/agents/08-HANDOFF-CONTRACT.md`) e siga o `PROXIMO` dele, desde que
   respeite a ordem acima.
4. `STATUS: bloqueado` interrompe: leve o `MOTIVO` ao subagente indicado em
   `PROXIMO` ou ao usuário, se for decisão humana.
5. `security-review` roda uma vez, depois do `code-reviewer`, quando o sinal ou
   o diff exigir. Achado de revisor segue o ciclo de correção abaixo; revisor
   não corrige.
6. Não pule `qa-validation` nem `docs-update`. O `git-delivery` só roda com o
   validador aprovado. Passe `MESCLADAS` ao `docs-update`.
7. Ao final, resuma para o usuário: `DELIVERY_ID`, o que mudou, a saída do
   validador e o link do PR/MR.

## Ciclo de correção

Quando um revisor devolve `STATUS: bloqueado` com achado no código:

1. `test-writer` primeiro: um teste que falha e reproduz o achado, quando ele
   for testável (defeito de comportamento). Achado só de estilo ou de nome pula
   este passo.
2. `dev-implementation` corrige até o teste novo e os sensores passarem.
3. O mesmo revisor roda de novo sobre o diff atualizado, com o achado anterior
   no handoff, e confirma que o bloqueio caiu. Só então o fluxo segue para o
   próximo da ordem.

Os revisores depois dele na ordem ainda não rodaram e veem o diff já
corrigido.

Em paralelo: depois do `code-reviewer` aprovar (e do `query-performance`, que
edita código e roda antes, sozinho), os revisores só leitura que se aplicam
(`migration-reviewer`, `tenant-isolation`, `security-review`,
`supply-chain-review`) podem rodar ao mesmo tempo sobre o mesmo diff: nenhum
grava nada. Bloqueio de qualquer um deles abre o ciclo acima, e todos rodam de
novo sobre o diff corrigido. `qa-validation` espera todos.
