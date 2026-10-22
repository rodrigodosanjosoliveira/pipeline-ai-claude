# Fixtures de hook do Claude Code

Payloads reais de `PreToolUse` e `PostToolUse`, **sanitizados**, organizados por
versão do Claude Code: `testdata/hooks/claude/<versão>/<evento>-<ferramenta>-<agente>.json`.

- Origem: `scripts/capture-hook.sh` numa worktree descartável, depois
  `go run ./tools/fixture-sanitize` (ver `docs/fase-0-captura-fixtures.md`).
- Regras verificadas por `TestFixturesVersionadas` (`internal/fixtures`): caminhos
  absolutos só sob `/workspace/exemplo` ou `/home/exemplo`; `session_id`,
  `transcript_path`, `agent_id`, `tool_use_id` com valores fixos; conteúdo
  (`content`, `old_string`, `new_string`, `new_source`, `tool_response`,
  `prompt`, `message`) como `<removido>`.
- Nunca editar à mão para "consertar" uma fixture reprovada: recapture.
