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

## Fase 2

### TD-18 — `fmt-hook` formata com `go/format`, no `PostToolUse`
Só arquivo `.go` dentro da raiz do projeto e fora de `no_agent_paths`, resolvido
como no hook (TD-09). Formata com `go/format` da stdlib, sem depender de `gofmt`
no PATH; grava só se mudou, por arquivo temporário + rename, preservando a
permissão. Payload ilegível ou evento diferente de `PostToolUse` → exit 2 (TD-08).
Erro de sintaxe → exit 2 com a mensagem do parser: no `PostToolUse` o exit 2 não
desfaz a edição, devolve o erro ao agente para ele corrigir.
**Motivo:** formatação determinística sem gastar turno (D-18 do pipeline-ai) e sem
dependência de ferramenta externa no Windows (TD-03).
**Ressalva:** a formatação segue a versão do Go que compilou o binário; o `doctor`
já confere o Go do projeto.

### TD-19 — `sensors` executa argv, sem shell
Cada sensor roda `cmd` como argv na raiz do projeto, na ordem declarada, com
timeout por sensor (`--timeout`, padrão 10 min) e saída capturada com limite.
Todos rodam, mesmo depois de uma falha, para o relatório ficar completo. Comando
ausente, timeout ou exit ≠ 0 reprovam. Nenhum sensor configurado → exit 1
(nada foi verificado; falha fechada, TD-08).
**Motivo:** sem shell não há quoting nem diferença entre sh e cmd.exe (TD-03).

### TD-20 — `delivery new` gera ID sequencial por prefixo e nunca sobrescreve
ID = `<prefixo>-<NNN>`, com prefixo de `delivery_prefixes` e `NNN` = maior número
do prefixo no índice e em `deliveries_dir`, mais um, com três dígitos no mínimo.
O ID gerado tem de casar `delivery_id_pattern`. Diretório e report criados em modo
exclusivo; índice reescrito por arquivo temporário + rename; se o índice falhar,
o diretório criado é removido. Título de uma linha, sem `|` nem caractere de
controle. Escrita confinada ao destino (TD-17).
**Motivo:** dois agentes ou duas pessoas criando delivery ao mesmo tempo não
podem gerar o mesmo ID em silêncio nem corromper o índice.

### TD-21 — MCP: stdio, ferramentas de leitura e `delivery_new` restrita
JSON-RPC 2.0 por linha no stdin/stdout; stdout só carrega protocolo, log vai
para stderr. Ferramentas: `pipeline_doctor`, `pipeline_validate`,
`pipeline_sensors`, `pipeline_delivery_new`. `pipeline_delivery_new` é recusada
quando `deliveries_dir` ou `index_file` está em `protected_paths` (TD-16).
Configuração ausente ou inválida → toda ferramenta responde `isError`. `sync`
não é exposto (TD-16).

## Pontos em aberto

1. Distribuição da camada Claude também como plugin do Claude Code.
2. Padrão de revisor: sem escrita (pipeline-ai) ou `code-reviewer` dono do ajuste
   pós-dev restrito ao diff (C6); possivelmente configurável.
3. Ordem padrão do fluxo: security em dois momentos ou só após review (C8).
4. Esquema padrão de report.
5. `tenant-isolation` como módulo genérico na v1.
6. Pre-push local no `init`.
