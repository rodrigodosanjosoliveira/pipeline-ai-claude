# Decisões do toolkit pipeline-ai-claude

Registro das decisões que definem o toolkit, com o motivo de cada uma. Marcações
`C-xx` identificam divergências deliberadas em relação ao pipeline-ai; a
divergência está descrita na própria decisão.

**Referência de leitura:** pipeline-ai `main` em `0eac73a` (v0.5.0 + tipo `cli`).

## Escopo e distribuição

### TD-01 — Projeto apartado do pipeline-ai
O toolkit não altera nem depende do pipeline-ai, que segue fixado no Antigravity.
Código pode ser portado (MIT) com registro de origem.
**Motivo:** os dois runtimes evoluem em ritmos e protocolos diferentes; uma base
compartilhada obrigaria cada mudança a quebrar ou contornar o outro lado.

### TD-02 — Runtime alvo: Claude Code
Subagentes em `.claude/agents/` com `tools:`, commands em `.claude/commands/`,
hooks no `.claude/settings.json`, MCP em `.mcp.json`.
**Motivo:** no Claude Code a identidade do agente (`agent_type`) e a restrição de
ferramentas por agente são aplicadas pelo runtime, não só por instrução de prompt.

### TD-03 — Go, núcleo só stdlib
Sem dependência externa no `go.mod`; gate `make stdlib-only` no CI.
**Motivo:** binário estático sem `python3`/`jq`/bash no Windows; mesma motivação do
D-16 e D-19 do pipeline-ai.

### TD-04 — Público, MIT, GitHub
`github.com/rodrigodosanjosoliveira/pipeline-ai-claude`. Binário `pipeline-claude`.

### TD-05 — Nada de projeto real no repositório
Fixtures sanitizadas por `tools/fixture-sanitize` e verificadas por
`TestFixturesVersionadas`; lista privada de termos via
`PIPELINE_CLAUDE_FIXTURE_BLOCKLIST`. Testes de paridade contra reports reais leem
um corpus fora do repositório (`PIPELINE_CLAUDE_PARITY_CORPUS`) e são ignorados no
CI. Templates e exemplos usam domínio fictício.

### TD-06 — Instalação fora do `go.mod` do projeto
Projetos instalam com `go install .../cmd/pipeline-claude@<versão>` fixada no
`Makefile` deles, não com a diretiva `tool`.
**Motivo:** a ferramenta de governança não deve entrar no grafo de dependências do
produto, que o `govulncheck` e o `go mod tidy` do projeto verificam (C16).

### TD-07 — Escopo da v1: `api` + PostgreSQL; provedores GitHub e GitLab
`cli` e `desktop` ficam para depois.

## Governança

### TD-08 — Falha fechada em todo o caminho do hook
Payload ilegível, ferramenta de edição sem caminho, configuração ausente ou
inválida: exit 2.
**Diverge de:** pipeline-ai libera com JSON inválido (teste "JSON inválido não
trava a sessão") (C1).

### TD-09 — Resolução de caminho
Caminho relativo resolvido contra a raiz do projeto (não contra o cwd do processo);
symlinks seguidos (`EvalSymlinks` no maior prefixo existente); comparação de
caminhos protegidos sem diferenciar caixa (errar para o lado do bloqueio); campo
`notebook_path` no `NotebookEdit` (C2, P3).

### TD-10 — Modelo de autorização combinado
`protected_paths` editáveis só por `agent_type ∈ editor_agents`; `no_agent_paths`
não editáveis por nenhum agente (aplicação fora da sessão). Sessão principal
nunca edita caminho protegido (C3).

### TD-11 — Diff completo no validador
União de `<base>...HEAD`, working tree, staged e não rastreados, com renomeação
desligada (`--no-renames`). Falha do git reprova, salvo `--no-git` explícito (C9).

### TD-12 — Esquema de report declarado na configuração
Front matter, seções, campos obrigatórios, flag → seção/campos, nível de título,
valores-placeholder. Booleanos estritos (`true`/`false`) (C10, C12).

### TD-13 — Flags derivadas do diff
`auto_flags` (padrão de caminho → flag) generaliza o D-14 do pipeline-ai; o
validador reprova quando o diff toca o padrão e a flag está `false`.

### TD-14 — Configuração própria
`.pipeline-claude.json` (não colide com `.pipeline-ai.json`), `DisallowUnknownFields`,
`version: 1`.

### TD-15 — Arquivos gerenciados × arquivos do projeto
O toolkit só atualiza o que gerou e o projeto não editou, conferido por sha256 em
`.pipeline-claude.lock`. `CLAUDE.md`, `docs-ai/`, permissões e configuração são do
projeto (C17).

### TD-16 — MCP só grava fora de caminho protegido
`sync` não é exposto via MCP (C17).

### TD-17 — Escrita confinada ao destino
`init`, `sync`, `adopt` e `delivery new` validam nomes (`^[a-z][a-z0-9-]*$` para
projeto) e conferem que todo caminho gravado fica dentro do destino após
`filepath.Clean` (C18).

## Pontos em aberto

1. Distribuição da camada Claude também como plugin do Claude Code.
2. Padrão de revisor: sem escrita (pipeline-ai) ou `code-reviewer` dono do ajuste
   pós-dev restrito ao diff (C6); possivelmente configurável.
3. Ordem padrão do fluxo: security em dois momentos ou só após review (C8).
4. Esquema padrão de report.
5. `tenant-isolation` como módulo genérico na v1.
6. Pre-push local no `init`.
