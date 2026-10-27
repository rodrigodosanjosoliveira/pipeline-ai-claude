---
name: nova-delivery
description: Cria a próxima delivery (ID sequencial, report a partir do template e linha no índice). Use quando uma demanda nova precisar de delivery antes do triage.
argument-hint: <título da delivery>
allowed-tools: mcp__pipeline-claude__pipeline_delivery_new
---

# Nova delivery

Crie a delivery com o título: $ARGUMENTS

1. Se o título estiver vazio, pergunte ao usuário antes de seguir. O título tem
   uma linha, sem `|`.
2. Chame a ferramenta MCP `pipeline_delivery_new` com `title` igual ao título.
   Ela gera o próximo ID da sequência, cria
   `docs-ai/deliveries/<ID>/report.md` a partir do template e registra a linha
   em `docs-ai/deliveries/INDEX.md`. Nunca crie o diretório, o report ou a
   linha do índice à mão.
3. Se a ferramenta não estiver disponível ou recusar, mostre o erro e peça ao
   usuário para rodar `bin/pipeline-claude delivery new --title "<título>"` no
   terminal (e `bin/pipeline-claude doctor` se o erro for de ambiente).
4. Informe o ID criado e o caminho do report. O próximo passo é o subagente
   `pm-triage` com a demanda e esse `DELIVERY_ID`.
