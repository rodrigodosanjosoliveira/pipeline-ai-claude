---
name: validar
description: Valida o report de uma delivery (esquema, índice, ID e flags exigidas pelo diff) e roda os sensores do projeto. Use antes de commitar ou abrir PR/MR.
argument-hint: <DELIVERY_ID>
allowed-tools: mcp__pipeline-claude__pipeline_validate, mcp__pipeline-claude__pipeline_sensors
---

# Validar delivery

Delivery: $ARGUMENTS

1. Se o ID estiver vazio, consulte `docs-ai/deliveries/INDEX.md` e pergunte ao
   usuário qual delivery validar.
2. Chame a ferramenta MCP `pipeline_sensors` e mostre o resultado de cada
   sensor.
3. Chame a ferramenta MCP `pipeline_validate` com `target` igual ao ID. Não use
   `no_git` sem pedido explícito do usuário: sem o diff, as flags exigidas por
   `auto_flags` e `security_surface_paths` não são conferidas.
4. Mostre cada achado. Reprovado não segue para commit: o report é corrigido
   pelo `docs-update`, o código pelo `dev-implementation`.
5. Sem as ferramentas MCP, peça ao usuário para rodar
   `bin/pipeline-claude sensors` e `bin/pipeline-claude validate <ID>`.
