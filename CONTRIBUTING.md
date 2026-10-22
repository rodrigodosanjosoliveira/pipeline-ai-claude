# Como contribuir

1. Abra uma issue descrevendo o problema ou a proposta antes de mudanças grandes.
2. Leia `CLAUDE.md` (regras invioláveis) e `docs/DECISOES.md`.
3. Branch `task/TASK-NNN`; commit `{type}({scope}): {resumo} [TASK-NNN]`.
4. Teste primeiro. `make check` precisa passar antes da PR.
5. Nada de dependência externa no núcleo (`make stdlib-only`).
6. Fixtures de hook só pelo fluxo de `docs/fase-0-captura-fixtures.md`.
7. Preencha o template da PR com comandos e saídas reais.
