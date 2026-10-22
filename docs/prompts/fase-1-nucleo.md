# Prompt — Fase 1: núcleo de governança

> Para colar no Claude Code com o repositório `pipeline-ai-claude` aberto no WSL.
> Pré-requisitos: PR da Fase 0 mesclada; fixtures em `testdata/hooks/claude/<versão>/`
> (sem elas, o pacote `hook` não começa); clone de leitura do pipeline-ai em
> `~/workspace/pipeline-ai` no commit `0eac73a`.

---

Você vai implementar a **Fase 1** do toolkit: os pacotes `config`, `gitdiff`,
`report`, `hook`, `validate` e `doctor`, e ligar `hook`, `validate` e `doctor` na
CLI. Leia antes, nesta ordem: `CLAUDE.md`, `docs/DECISOES.md`,
`testdata/hooks/claude/README.md`.

## Regras de execução

1. **Uma PR por pacote**, branch `task/TASK-NNN`, na ordem do grafo abaixo. Não
   misture pacotes numa PR.
2. **Teste primeiro.** Cada PR começa com testes de tabela que falham; depois o
   código. Parsers (`config`, `report`, payload do hook) ganham alvo de fuzz
   (`FuzzXxx`) que roda no `go test` comum pelos seeds.
3. **Portar, não copiar decisão.** Use `~/workspace/pipeline-ai` (somente leitura:
   não edite, não commite, não abra PR lá) como ponto de partida para
   `internal/config`, `internal/hook`, `internal/report`, `internal/validate` e
   `internal/doctor`. Cada arquivo portado leva o cabeçalho de origem do `CLAUDE.md`
   e uma linha no `NOTICE`. Onde o comportamento muda, o teste correspondente
   cita a decisão (`TD-xx`).
4. `make check` verde antes de abrir cada PR; cole comandos e saídas na descrição.
5. Se uma regra deste prompt conflitar com o `CLAUDE.md` ou o `DECISOES.md`,
   **pare e pergunte**. Não resolva sozinho.

## Grafo de execução

```
config ─┬─> hook
        ├─> validate <── report
        └─> doctor        gitdiff ──> validate
```

`config`, `gitdiff` e `report` são independentes: podem ir em paralelo, cada um
numa worktree (`git worktree add ../pac-<pacote> -b task/TASK-NNN`) com um
subagente. `hook`, `validate` e `doctor` só depois das dependências mescladas.

## 1. `internal/config`

Carrega `.pipeline-claude.json` subindo a partir de um diretório inicial até a raiz
do filesystem (`Find`), e `Load` devolve `*Config` com `Root()`.

Campos (todos documentados no godoc do struct):
`version`, `project_type`, `database`, `git_provider`, `base_ref`, `deliveries_dir`,
`index_file`, `report_template`, `delivery_id_pattern`, `delivery_prefixes`,
`invariants_files`, `protected_paths`, `no_agent_paths`, `editor_agents`,
`enabled_modules`, `security_surface_paths`, `auto_flags` (map flag → padrões),
`report_schema` (ver §3), `sensors` (lista; só parse e validação nesta fase).

Validação (erro descreve o campo e o motivo):
- `json.Decoder.DisallowUnknownFields` (TD-14); JSON inválido é erro.
- `version == 1`; `project_type == "api"`; `database == "postgres"`;
  `git_provider ∈ {github, gitlab}` (TD-07).
- `protected_paths` não vazio; `no_agent_paths ⊆ protected_paths` (por casamento de
  padrão, não por string).
- Todo caminho: relativo, separador `/`, sem `..` após `path.Clean`, sem prefixo `/`.
- `editor_agents` e `enabled_modules`: `^[a-z][a-z0-9-]*$`; `enabled_modules` contido
  no catálogo (`data-model`, `migration-reviewer`, `query-performance`,
  `release-ops`, `agent-auditor`, `context-loader`, `tenant-isolation`).
- `delivery_id_pattern` compila e é ancorado (`^...$`).
- Chaves de `auto_flags`: `^require_[a-z_]+$`.
- `sensors`: nomes únicos `^[a-z][a-z0-9_]*$`, `cmd` não vazio.
- Defaults: `base_ref = origin/main`, `deliveries_dir = docs-ai/deliveries`,
  `index_file = <deliveries_dir>/INDEX.md`, `report_schema` padrão (§3).

API de casamento: `IsProtected(rel)`, `IsNoAgent(rel)`, `IsSecuritySurface(rel)`,
`FlagsTriggeredBy(rel) []string`, `IsEditorAgent(name)`. Regras de padrão: termina
em `/` casa o diretório e abaixo; sem `/` final casa o caminho exato ou como
diretório. **Comparação sem diferenciar caixa** (TD-09).

Casos mínimos de teste: arquivo ausente (`ErrNotFound`), JSON inválido, campo
desconhecido, cada regra de validação acima, defaults, casamento com caixa
diferente, `..` rejeitado, `no_agent_paths` fora de `protected_paths` rejeitado.

## 2. `internal/gitdiff`

`ChangedFiles(ctx, root, base string) ([]string, error)`: união ordenada e sem
repetição de
- `git diff --name-only --no-renames -z <base>...HEAD`
- `git diff --name-only --no-renames -z HEAD` (working tree + staged)
- `git ls-files --others --exclude-standard -z`

Executa com `exec.CommandContext`, `Dir = root`, `-c core.quotepath=off`. Qualquer
falha é erro com o comando e o stderr (TD-11). Repositório sem commits: erro claro.

Testes com repositório temporário criado no próprio teste (`git init`, commits,
branch base): arquivo commitado na branch, modificado não commitado, staged, novo
não rastreado, renomeado (as duas pontas aparecem), nome com espaço e acento,
`base` inexistente (erro). Pule o teste com `t.Skip` só se `git` não estiver no
PATH, e faça o CI falhar nesse caso (`PIPELINE_CLAUDE_REQUIRE_GIT=1` no workflow).

## 3. `internal/report`

Parser de relatório Markdown com front matter.

- Front matter entre `---` iniciais; pares `chave: valor`; aspas removidas; chave
  em minúsculas. Front matter ausente ou não fechado é erro.
- Seções por título de nível `report_schema.section_level` (padrão 2, `## `).
  Títulos de outros níveis **não** abrem seção. Linhas dentro de blocos de código
  cercados (```` ``` ````) nunca são títulos.
- Comparação de títulos normalizada (minúsculas, sem acento, sem `:`/`.`).
- Campo: linha `- <Nome>: valor` na seção; se o valor estiver vazio na linha, ele é
  a junção dos sub-itens indentados logo abaixo, até o próximo item de topo, linha
  em branco ou fim da seção.
- Booleano estrito: `true`/`false`; qualquer outro valor numa flag é erro (TD-12).

`report_schema` (na config) e padrão:

```json
{
  "section_level": 2,
  "front_required": ["delivery_id", "status"],
  "sections_required": ["PM/Triage", "Architecture/Guardrails", "Dev", "QA", "Docs", "Final Checklist"],
  "fields_required": {
    "PM/Triage": ["Escopo fechado", "Criterios de aceite", "Riscos"],
    "Architecture/Guardrails": ["Invariantes aplicaveis", "Arquivos provaveis de impacto"],
    "Dev": ["Arquivos alterados", "Resumo tecnico", "Testes criados/ajustados", "Evidencias (codigo/teste)"],
    "QA": ["Resultado", "Cenarios executados"],
    "Docs": ["Docs atualizados"]
  },
  "flag_sections": {
    "require_security": {"section": "Security", "fields": ["Resultado"]},
    "require_release_ops": {"section": "Release/Ops", "fields": ["Plano de rollback"]}
  },
  "checklist_section": "Final Checklist",
  "placeholder_values": ["", "todo", "tbd", "pendente", "n/a", "<delivery_id>", "<modulo>"],
  "forbid_markers": ["TODO", "TBD", "FIXME", "XXX"]
}
```

Checagens: front obrigatório presente e não vazio; seções obrigatórias presentes e
não vazias; campos obrigatórios com valor fora de `placeholder_values` (comparação
sem caixa e sem espaços); para cada flag `true` em `flag_sections`, seção e campos
exigidos; item `- [ ]` na seção de checklist reprova; marcadores de
`forbid_markers` como palavra inteira fora de blocos de código reprovam; valor de
placeholder no formato `<...>` reprova em campo obrigatório.

Testes: um caso por checagem, report válido completo, títulos com acento, `###`
dentro de seção, `## ` dentro de bloco de código, campo multilinha, flag inválida,
CRLF. Fuzz: `FuzzParse` não entra em pânico e é determinístico.

## 4. `internal/hook`

`Run(stdin io.Reader, stderr io.Writer, getwd func() (string, error)) int` — só
protocolo do **Claude Code**, saída por código (0 libera, 2 bloqueia) e mensagem em
stderr.

Ordem de decisão (TD-08, TD-09, TD-10):
1. Leitura do stdin falha, JSON inválido ou não objeto → **2**.
2. `tool_name ∉ {Edit, Write, MultiEdit, NotebookEdit}` → 0.
3. Caminho: `tool_input.file_path`, ou `tool_input.notebook_path` no
   `NotebookEdit`. Ausente ou vazio → **2**.
4. Configuração: `config.Load` a partir de `cwd` do payload (ou `getwd`). Ausente
   ou inválida → **2**, com mensagem apontando `pipeline-claude doctor`.
5. Resolução: relativo → junta com a **raiz do projeto**; `filepath.Clean`;
   `EvalSymlinks` no maior prefixo existente (o arquivo pode ainda não existir) e
   também na raiz. Fora da raiz após resolver → 0 (outro repositório).
6. `IsNoAgent(rel)` → **2** para qualquer origem.
7. `IsProtected(rel)` → 0 se `agent_type ∈ editor_agents`; senão **2**.
8. Caso contrário → 0.

Mensagem de bloqueio: arquivo relativo à raiz, origem (`sessão principal` ou
`agente "<x>"`), motivo (`caminho sem edição por agentes` × `agente sem permissão`),
lista de editores e próximo passo. Nunca imprimir conteúdo do payload além do
caminho e do agente.

Testes: **todas** as fixtures de `testdata/hooks/claude/**` passam pelo `Run` com a
decisão esperada declarada numa tabela (fixture → código), usando uma config de
teste em diretório temporário cuja raiz substitui `/workspace/exemplo` (reescreva o
payload no teste). Mais: JSON inválido, stdin com erro, ferramenta de leitura,
caminho ausente, `NotebookEdit` com `notebook_path`, relativo, symlink de fora para
dentro de caminho protegido, caixa diferente, `..` escapando da raiz, config
ausente, config inválida. Fuzz: `FuzzRun` nunca devolve 0 para payload que não seja
JSON objeto válido com ferramenta de edição e caminho resolvível.

## 5. `internal/validate`

`Run(opts Options, stdout, stderr io.Writer) int` com `Options{Target, BaseRef,
NoGit, JSON, Getwd}`. `Target` é o ID da delivery (resolve
`<deliveries_dir>/<ID>/report.md`) ou o caminho do report.

Checagens, além das do `report`:
- `delivery_id` do front casa `delivery_id_pattern` e é igual ao diretório.
- Se `index_file` existir na config, o ID aparece numa linha da tabela do índice.
- Com git (padrão): `gitdiff.ChangedFiles`; para cada arquivo, `FlagsTriggeredBy`
  e `IsSecuritySurface` (→ `require_security`). Flag disparada e `false` no front →
  erro citando os arquivos (TD-13). Falha do git → erro (exit 1), com dica de
  `--no-git`.
- `--no-git`: pula o diff e emite um aviso na saída.

Saída humana (`ERRO`/`AVISO` por linha, resumo final) ou `--json`
(`{"report":..., "ok":bool, "findings":[{"level","message"}]}`). Exit 0 ok, 1
reprovado, 2 uso inválido (alvo inexistente, config inválida).

Testes: repositório temporário com config, índice e reports; cada checagem; JSON.
**Paridade** (`TestParityCorpus`): se `PIPELINE_CLAUDE_PARITY_CORPUS` apontar para
um diretório com `.pipeline-claude.json`, `<deliveries_dir>/**/report.md` e
`expected.json` (`{"<ID>": "ok"|"fail"}`), roda `validate --no-git` em cada ID e
compara; sem a variável, `t.Skip`. O corpus nunca entra no repositório (TD-05).

## 6. `internal/doctor`

`Collect(root) []Check` e `Run(root, json bool, stdout) int`. Checagens: `go`
presente e ≥ a versão do `go.mod` do projeto; `git`; `gh` ou `glab` conforme
`git_provider`; configuração válida; hook `pipeline-claude hook` registrado no
`.claude/settings.json` para `Edit|Write|MultiEdit|NotebookEdit`; binário
`bin/pipeline-claude` presente e com a versão do `Makefile` do projeto, se
declarada; aviso de filesystem case-insensitive (crie dois arquivos que diferem
só na caixa num diretório temporário dentro da raiz e veja se colidem). Cada
check: `ok`, `aviso` ou `erro`, com ação corretiva. Exit 1 se houver `erro`.

## 7. CLI

Ligue `hook`, `validate [--base <ref>] [--no-git] [--json] <ID|caminho>` e
`doctor [--json]` no `cmd/pipeline-claude`, removendo-os de `planned`. Teste de
ponta a ponta com o binário compilado em `TestMain` (`go build` para diretório
temporário): payload de fixture no stdin do `hook`, report válido e inválido no
`validate`.

## Critérios de aceite da Fase 1

- `make check` e CI verdes em todas as PRs; núcleo continua só stdlib.
- Todas as fixtures de hook com decisão declarada e coberta.
- `TestParityCorpus` passa localmente contra o corpus privado do primeiro projeto
  adotante (evidência na PR do `validate`: quantos reports, quantos `ok`/`fail`,
  sem nomes nem conteúdo).
- `NOTICE` lista todo arquivo portado.
- Nenhum arquivo do pipeline-ai foi alterado (`git -C ~/workspace/pipeline-ai status --short` vazio, colado na última PR).
