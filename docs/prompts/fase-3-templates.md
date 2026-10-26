# Prompt — Fase 3: templates, `init` e `sync`

> Para colar no Claude Code com o repositório `pipeline-ai-claude` aberto no WSL.
> Pré-requisitos: Fase 2 mesclada (PRs #13 a #20); decisões TD-22 a TD-27 aceitas
> em `docs/DECISOES.md`; clone de leitura do pipeline-ai em `~/workspace/pipeline-ai`
> no commit `0eac73a`. A paridade da Fase 1 (`TestParityCorpus`) pendente não
> bloqueia esta fase.

---

Você vai implementar a **Fase 3** do toolkit: os pacotes `manifest`, `templates`,
`scaffold` e `syncer`, uma checagem nova no `doctor`, e ligar `init` e `sync` na
CLI. Leia antes, nesta ordem: `CLAUDE.md`, `docs/DECISOES.md` (em especial TD-02,
TD-06, TD-07, TD-10, TD-15 a TD-17 e TD-22 a TD-27) e os pacotes que você vai
usar (`internal/config`, `internal/doctor`, `internal/delivery`,
`internal/validate`).

## Regras de execução

1. **Uma PR por pacote**, branch `task/TASK-NNN`, na ordem do grafo abaixo.
2. **Teste primeiro.** Cada PR começa com testes de tabela que falham; depois o
   código. O parser do lock ganha `FuzzRead`; o plano do `sync` ganha
   `FuzzPlan` (ver §4). Rodam no `go test` comum pelos seeds.
3. **Portar, não copiar decisão.** O conteúdo dos subagentes parte de
   `pipeline-ai/internal/scaffold/templates/agents/skills/*/SKILL.md`; os
   documentos de `docs-ai/` partem de `templates/docs-ai/`; o `CLAUDE.md` gerado
   parte de `templates/AGENTS.md.tmpl`; o código de `init` parte de
   `internal/scaffold/scaffold.go` e o de `sync`, de `sync.go` (somente leitura:
   não edite, não commite, não abra PR lá). Arquivo Go portado leva o cabeçalho
   do `CLAUDE.md`; template portado registra a origem só no `NOTICE` (TD-22).
   Todo texto que fala de Antigravity, `.agents/`, `GEMINI.md`, `go tool
   pipeline-*`, `wails`, desktop, MongoDB ou DynamoDB sai ou é reescrito para o
   Claude Code.
4. **Domínio fictício** em templates, exemplos e testes (regra 4 do
   `CLAUDE.md`). Nome de projeto nos testes: `biblioteca-bairro` ou parecido.
5. `make check` verde antes de abrir cada PR; cole comandos e saídas reais na
   descrição, inclusive a falha inicial dos testes.
6. Se uma regra deste prompt conflitar com o `CLAUDE.md` ou o `DECISOES.md`,
   **pare e pergunte**. Não resolva sozinho. Escolhas que o prompt não cobre vão
   numa seção "Pontos que decidi sem especificação explícita" da PR.

## Grafo de execução

```
manifest ──────┬─> scaffold ─┐
templates ─────┤             ├─> CLI
               └─> syncer ───┤
                     └─> doctor (checagem sync)
```

`manifest` e `templates` são independentes: podem ir em paralelo, cada um numa
worktree (`git worktree add ../pac-<pacote> -b task/TASK-NNN origin/main`) com um
subagente. `scaffold` e `syncer` depois dos dois mesclados, também em paralelo;
`doctor` depois de `syncer`; CLI por último.

O pacote do `sync` se chama `syncer` para não colidir com o `sync` da stdlib;
o comando continua `pipeline-claude sync`.

## 1. `internal/manifest` (lock)

`.pipeline-claude.lock` (TD-23).

```go
const FileName = ".pipeline-claude.lock"
type Lock struct {
    Version        int               // sempre 1
    ToolkitVersion string
    Files          map[string]string // caminho → sha256 hex
    Released       []string
}
func Read(root string) (*Lock, error)   // ErrNotFound se não existir
func Write(root string, l *Lock) error   // temporário + rename
func Hash(content []byte) string         // sha256 com CRLF → LF
func ManagedArea(rel string) bool        // .claude/agents/, .claude/skills/, .githooks/
```

- Leitura estrita: `DisallowUnknownFields`, conteúdo depois do objeto é erro,
  `version` ausente ou ≠ 1 é erro, `toolkit_version` vazio é erro, hash que não
  seja 64 hex minúsculos é erro, caminho repetido entre `files` e `released` é
  erro.
- Caminho: relativo, `/`, igual ao próprio `path.Clean`, sem `..`, sem `/`
  inicial nem letra de drive, e `ManagedArea` verdadeiro. Fora disso é erro.
- Escrita determinística: JSON indentado com duas posições, chaves ordenadas,
  `released` ordenado e sem repetição, LF, quebra de linha final. `Write`
  valida antes de gravar; lock inválido nunca vai para o disco.
- Não é arquivo regular (diretório, symlink) → erro.

Testes: ida e volta (`Write` → `Read` igual; `Write` duas vezes, mesmos bytes),
cada erro acima, `Hash` igual para LF e CRLF e diferente para conteúdo diferente,
`ManagedArea` com caixa diferente (`.Claude/agents/` não é área gerenciada:
o lock é gerado pelo toolkit, sempre em minúsculas). `FuzzRead`: nunca entra em
pânico; lock aceito sempre passa em `Write` e volta igual.

## 2. `internal/templates`

Catálogo e renderização (TD-22, TD-26).

```go
type Class int // Managed, Project
type Data struct {
    Name, Module, Summary, Provider, ToolkitVersion string
    Modules []string // enabled_modules, ordenado
}
type File struct {
    Path   string // destino relativo, com "/"
    Class  Class
    Module string // "" = sempre; senão, só com o módulo ligado
    Mode   fs.FileMode
}
func Catalog() []File
func Render(f File, d Data) ([]byte, error)
func Expected(class Class, modules []string) []File
```

Arquivos (`internal/templates/files/...`, embutidos com `embed.FS`):

| Destino | Classe | Origem |
|---|---|---|
| `.claude/agents/<agente>.md` (10 do núcleo, TD-26) | gerenciado | `SKILL.md` correspondente |
| `.claude/agents/<módulo>.md` (um por `config.Modules`) | gerenciado, condicionado ao módulo | `SKILL.md`; `tenant-isolation` é novo |
| `.claude/skills/{nova-delivery,fluxo,validar}/SKILL.md` | gerenciado | fluxo do `AGENTS.md.tmpl`; novo |
| `.githooks/pre-push` (0755) | gerenciado | `pre-push.tmpl` |
| `CLAUDE.md` | projeto | `AGENTS.md.tmpl` |
| `.claude/settings.json` | projeto | novo (TD-26) |
| `.mcp.json` | projeto | novo (TD-26) |
| `.pipeline-claude.json` | projeto | `pipeline-ai.api.json` |
| `Makefile` | projeto | `Taskfile.api.yml` |
| `.gitignore` | projeto | `gitignore.tmpl` |
| `go.mod`, `cmd/<nome>/main.go`, `cmd/<nome>/main_test.go` | projeto | `main.api.go.tmpl` |
| `docs-ai/01-INVARIANTES-GLOBAIS.md`, `03-MAPA-RAPIDO-MODULOS.md`, `DECISOES.md`, `agents/08-HANDOFF-CONTRACT.md` | projeto | `templates/docs-ai/` |
| `docs-ai/deliveries/INDEX.md`, `docs-ai/deliveries/_template/report.md` | projeto | `templates/docs-ai/deliveries/` |

Conteúdo:
- **Subagente**: front matter com `name` (= nome do arquivo), `description`
  (quando usar, para o roteamento automático) e `tools` (string separada por
  vírgula). Revisores sem `Edit`, `Write`, `MultiEdit`, `NotebookEdit` (TD-26).
  O corpo cita `pipeline-claude validate`, `sensors` e as ferramentas MCP onde o
  `SKILL.md` original citava `go tool pipeline-*`.
- **`.pipeline-claude.json`**: `api`, `postgres`, `git_provider` de `Data`,
  `delivery_prefixes: ["DEL"]` e `delivery_id_pattern: "^DEL-[0-9]{3,}$"`,
  `deliveries_dir`, `index_file`, `report_template`, `invariants_files`,
  `protected_paths` (`cmd/`, `internal/`, `migrations/`, `go.mod`, `go.sum`,
  `Makefile` e os quatro de `no_agent_paths`, que a config exige cobertos),
  `no_agent_paths` (os quatro de TD-26), `editor_agents`
  (`dev-implementation`, `test-writer`, `query-performance`, `docs-update`),
  `enabled_modules` de `Data`, `security_surface_paths`, `auto_flags` (go.mod e
  go.sum → `require_security`) e `sensors` (`go vet ./...`, `go test ./...`,
  `go build ./...`). **Sem** `report_schema` (TD-26: vale o padrão).
- **`Makefile`**: `PIPELINE_CLAUDE_VERSION := [[.ToolkitVersion]]` (é o que o
  `doctor` lê, TD-06), alvo `tools` com `GOBIN=$(CURDIR)/bin go install
  github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@$(PIPELINE_CLAUDE_VERSION)`,
  alvos `build`, `test`, `vet`, `check` (`bin/pipeline-claude sensors`).
- **`.gitignore`**: inclui `bin/`.
- **`go.mod`**: `module [[.Module]]`, `go 1.24`, sem `tool` nem `require` (TD-06).
- **Código inicial**: servidor HTTP só com stdlib e `GET /healthz`, com teste de
  tabela; `go vet` e `go test` passam sem rede.
- **Report e índice**: o `report.md` de `_template` segue o
  `config.DefaultReportSchema` com os marcadores `<delivery_id>`, `<titulo>` e
  `<data>` (os mesmos de `delivery.New`). O `INDEX.md` tem a tabela com
  `ID | Título | Status | Data` e nenhuma linha de exemplo que pareça ID.

Testes:
- Todo arquivo do catálogo renderiza com um `Data` de exemplo; saída sem `[[`
  nem `]]`; `Data` incompleto falha (`missingkey=error`).
- **Gerenciado não depende do projeto**: renderizar cada gerenciado com dois
  `Data` de nome, módulo e resumo diferentes dá os mesmos bytes.
- Todo arquivo embutido está no catálogo e vice-versa (nada órfão).
- Todo `config.Modules` tem subagente, e todo subagente de módulo é um
  `config.Modules` (teste falha se o catálogo da config mudar).
- Front matter de cada subagente: `name` = arquivo, `description` não vazia,
  `tools` só com nomes conhecidos; tabela explícita agente → tem/não tem escrita;
  todo `editor_agents` da config gerada tem `Edit` ou `Write`.
- Config gerada passa em `config.Load` para combinações de módulos (nenhum,
  todos) e de provedor; `settings.json` gerado passa nas checagens `hook` e
  `fmt-hook` do `doctor` (chame a função interna por um teste no `doctor`, ou
  exporte o mínimo; registre a escolha na PR).
- **Sincronia com `delivery` e `validate`**: num projeto renderizado em
  diretório temporário, `delivery.New` cria `DEL-001`; preenchido, passa em
  `validate.Run` com `--no-git`; sem preencher, reprova só por placeholder ou
  checklist.
- Nenhum termo de `PIPELINE_CLAUDE_FIXTURE_BLOCKLIST` aparece nos templates
  (ignorado sem a variável, como em `TestFixturesVersionadas`), e nenhum texto de
  Antigravity da regra 3.

## 3. `internal/scaffold` (`init`)

`Init(opts Options) (Result, error)`; `Options{Name, Dir, Module, Summary,
Provider, ToolkitVersion string; Modules []string; Git bool; Stdout, Stderr
io.Writer}`; `Result{Root string; Files []string}` (TD-24).

- Validação antes de tocar no disco: nome pelo `^[a-z][a-z0-9-]*$`; módulo Go
  (padrão: o nome) sem espaço, sem `..`, sem `/` inicial ou final; provedor
  `github|gitlab` (padrão `github`); cada módulo em `config.Modules`, sem
  repetição; `ToolkitVersion` casando
  `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`; resumo de uma linha.
- Destino (`Dir`, padrão `./<nome>`): inexistente, ou diretório vazio. Arquivo,
  symlink ou diretório com conteúdo → erro, nada criado.
- Staging: `os.MkdirTemp(filepath.Dir(destino), ".pipeline-claude-init-*")`,
  renderiza `Expected(Project)` e `Expected(Managed, Modules)`, grava o lock com o
  hash de cada gerenciado, e move: rename do staging para o destino inexistente,
  ou de cada entrada de primeiro nível para o destino vazio. Falha em qualquer
  passo → remove o staging (e o que já tiver sido movido para o destino vazio).
- Todo caminho gravado conferido dentro do staging após `filepath.Clean` (TD-17).
- `Git`: depois do move, `git init -b main` e `git config core.hooksPath
  .githooks` (sem shell). Falha do git → erro, arquivos ficam (o projeto é
  válido sem git; a mensagem diz o que rodar).

Testes: projeto gerado tem exatamente os arquivos do catálogo esperado mais o
lock; lock confere com o disco; `config.Load` ok; `go vet ./...` e `go test
./...` no projeto gerado passam (pule com `t.Skip` se `go` não estiver no PATH,
como o `doctor` faz); cada validação de entrada; destino com arquivo (nada
muda); destino vazio; destino é symlink; falha no meio (staging em diretório
somente leitura, ou gravador injetado) sem resto no disco; `Git` com e sem git no
PATH; nomes perigosos (`../x`, `a/b`, `A`, vazio).

## 4. `internal/syncer` (`sync`)

`Plan(root string, cfg *config.Config, lock *manifest.Lock) ([]Item, error)` e
`Apply(root string, items []Item, lock *manifest.Lock, version string) error`,
mais `Release(root string, paths []string) error` (TD-25).

- `Item{Path, Action, Reason}`; `Action` ∈ `nada`, `criar`, `atualizar`,
  `recriar`, `apagar`, `editado`, `nao-gerenciado`, `liberado`. A tabela do
  TD-25 é o teste: uma linha por situação.
- Esperado = `templates.Expected(Managed, cfg.EnabledModules)`.
- `Apply` grava só `criar`, `atualizar`, `recriar` e `apagar`; antes de cada
  escrita, confere de novo o hash do disco (alguém pode ter editado entre o plano
  e a escrita) e transforma em `editado` se mudou. Grava por temporário + rename
  com o `Mode` do catálogo; recusa symlink em qualquer componente do caminho
  (`os.Lstat` em cada prefixo) e confere o destino dentro da raiz (TD-17).
  Diretório que ficou vazio depois de `apagar` é removido, só dentro da área
  gerenciada.
- O lock é reescrito no fim com `toolkit_version = version` e os hashes do que
  foi aplicado; `editado` e `nao-gerenciado` mantêm a entrada antiga (ou nenhuma).
- `Release`: só caminho presente em `files` do lock; move para `released`; não
  toca no arquivo.
- Versão: o chamador (CLI) compara a versão do binário com a do `Makefile`
  (mesma regex do `doctor`); diferentes → exit 2, nada gravado.

Testes: tabela do TD-25 completa; projeto recém-criado pelo `scaffold` → plano
todo `nada`; template "novo" simulado (catálogo injetável ou lock com hash
antigo) → `atualizar`; módulo ligado na config → `criar`; módulo desligado →
`apagar` se intacto, `editado` se não; arquivo editado entre plano e `Apply`;
symlink em `.claude/agents/` apontando para fora (nada gravado fora); lock com
caminho fora da área (erro, nada gravado); CRLF no disco não vira `editado`;
`Release` e depois `Plan` dá `liberado`; idempotência (`Apply` duas vezes, a
segunda não muda nada). `FuzzPlan`: para lock e disco arbitrários dentro de uma
raiz temporária, `Plan` nunca entra em pânico e nunca propõe caminho fora da
área gerenciada.

## 5. `internal/doctor` (checagem `sync`)

Checagem nova `sync` (TD-27), depois de `fmt-hook`: usa `manifest.Read` e
`syncer.Plan`, nunca `Apply`.

| Situação | Status |
|---|---|
| config ausente ou inválida | aviso "não verificado" (o erro sai na checagem configuração) |
| lock ausente | aviso, ação `pipeline-claude init` ou `adopt` (Fase 4) |
| lock inválido | erro com a mensagem |
| plano com `editado`/`nao-gerenciado` | aviso listando os caminhos, ação `sync --release` ou restaurar |
| plano com `criar`/`atualizar`/`recriar`/`apagar` | aviso, ação `pipeline-claude sync` |
| tudo `nada`/`liberado` | ok |

Testes numa tabela `TestCheckSync`; o projeto gerado pelo `scaffold` dá ok.
Atualize a linha do `NOTICE` do `doctor.go`.

## 6. CLI

Ligue no `cmd/pipeline-claude`, removendo `init` e `sync` de `planned` (fica só
`adopt`):

- `init <nome> [--dir <d>] [--module <m>] [--summary <s>] [--provider
  github|gitlab] [--modules a,b] [--toolkit-version <v>] [--git] [--json]`.
  Versão padrão: `buildinfo.Version()` se casar a regex do §3; senão, sem
  `--toolkit-version` é uso inválido (2) com mensagem dizendo como passar.
  Saída: raiz e próximos passos (`make tools`, `bin/pipeline-claude doctor`);
  JSON `{"root", "files"}`. Erro de validação → 2; falha de escrita → 1.
- `sync [--dry-run] [--json] [--release <caminho>]...`. Config ou lock ausente/
  inválido → 2 com dica do `doctor`; versão ≠ `Makefile` → 2; `--release` com
  `--dry-run` → 2. Saída humana: uma linha por item que não seja `nada`, e
  resumo. JSON `{"ok", "items": [...]}`. Exit do TD-25.

Teste de ponta a ponta com o binário compilado em `TestMain` (com `-ldflags -X`
fixando uma versão de teste, ex. `v0.0.0-test`): `init` gera o projeto; `doctor
--json` no projeto gerado tem `configuração`, `hook`, `fmt-hook` e `sync` ok
(o `binário` pode falhar: não há `bin/` no teste); `delivery new` + `validate
--no-git` no projeto gerado; `sync` sem mudanças → 0; edita um subagente → `sync`
→ 1 e arquivo intacto; apaga o subagente → `sync` → 0 e recriado; `--release` →
`sync` → 0 e o arquivo editado fica; liga um módulo na config → `sync` cria o
subagente. Atualize `README.md` (estado e tabela de comandos) e o layout do
`CLAUDE.md`.

## Critérios de aceite da Fase 3

- `make check` e CI verdes em todas as PRs; núcleo continua só stdlib.
- Projeto gerado por `init` passa em `config.Load`, `go vet`, `go test` e nas
  checagens `configuração`, `hook`, `fmt-hook` e `sync` do `doctor`.
- `init` nunca grava em destino com conteúdo e não deixa resto em falha.
- `sync` nunca altera arquivo editado, liberado ou fora da área gerenciada, nem
  grava fora da raiz (testes dedicados, inclusive symlink e lock adulterado).
- Revisores gerados sem ferramenta de escrita; `name` de todo subagente igual ao
  arquivo.
- Nada de Antigravity nem de projeto real nos templates.
- `NOTICE` lista todo arquivo e grupo de templates portado.
- Nenhum arquivo do pipeline-ai foi alterado (`git -C ~/workspace/pipeline-ai
  status --short` vazio, colado na última PR).
