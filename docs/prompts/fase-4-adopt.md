# Prompt — Fase 4: `adopt` e primeiro projeto existente

> Para colar no Claude Code com o repositório `pipeline-ai-claude` aberto no WSL.
> Pré-requisitos: Fase 3 e ajustes posteriores mesclados (PRs #21 a #32); tag
> `v0.1.0` publicada; decisões TD-31 a TD-34 aceitas em `docs/DECISOES.md`;
> corpus de paridade do projeto piloto fora de qualquer repositório, apontado por
> `PIPELINE_CLAUDE_PARITY_CORPUS`.

---

Você vai implementar a **Fase 4** do toolkit: o pacote `adopt`, uma checagem nova
no `doctor` e o comando `pipeline-claude adopt`. Leia antes, nesta ordem:
`CLAUDE.md`, `docs/DECISOES.md` (em especial TD-07, TD-08, TD-16, TD-17, TD-22 a
TD-26, TD-29 e TD-31 a TD-34) e os pacotes que você vai usar (`internal/config`,
`internal/manifest`, `internal/templates`, `internal/scaffold`,
`internal/syncer`, `internal/doctor`).

## Regras de execução

1. **Uma PR por pacote**, branch `task/TASK-NNN`, na ordem do grafo abaixo.
2. **Teste primeiro.** Cada PR começa com testes de tabela que falham; depois o
   código. O plano do `adopt` ganha `FuzzPlan` (ver §1); o leitor de front
   matter de subagente ganha `FuzzAgentFrontMatter` (ver §2).
3. **Código novo.** O pipeline-ai não tem adoção; nada é portado nesta fase.
4. **Domínio fictício** em testes e exemplos (regra 4 do `CLAUDE.md`). O projeto
   piloto é real: nada dele entra no repositório (nome, caminhos, prefixos,
   reports, saídas). A evidência de paridade vai na PR só como contagem
   (`N reports, 0 divergências`).
5. `make check` verde antes de abrir cada PR; cole comandos e saídas reais na
   descrição, inclusive a falha inicial dos testes.
6. Se uma regra deste prompt conflitar com o `CLAUDE.md` ou o `DECISOES.md`,
   **pare e pergunte**. Escolhas que o prompt não cobre vão numa seção "Pontos
   que decidi sem especificação explícita" da PR.

## Grafo de execução

```
adopt ──────────┐
                ├─> CLI (adopt) ─> README e guia de adoção
doctor (agents) ┘
```

`adopt` e a checagem do `doctor` são independentes: podem ir em paralelo, cada
um numa worktree (`git worktree add ../pac-<pacote> -b task/TASK-NNN
origin/main`). CLI por último.

## 1. `internal/adopt`

`Plan(opts Options) (*Plan, error)` e `Apply(p *Plan) (Result, error)`;
`Options{Root, Provider, ToolkitVersion string; Modules []string}`;
`Plan{Root string; Items []Item; Instructions []Instruction; Lock
*manifest.Lock; Config *config.Config}`; `Item{Path string; Action Action;
Reason string}`; `Instruction{Path, Reason, Snippet string}`;
`Result{Created []string}`.

**Pré-condições (TD-33)**, todas antes de qualquer escrita, cada uma com erro
próprio:
- `Root` absoluto e limpo, diretório real (symlink recusado), com `go.mod`.
- Dentro de um working tree git, com `git status --porcelain` vazio (sem shell;
  falha do git é erro, TD-08).
- Sem `.pipeline-claude.lock`.
- `ToolkitVersion` válida (`scaffold.ValidVersion`).
- `.pipeline-claude.json`: existente → `config.Load` (inválido é erro, nada
  muda); ausente → renderizado do template de `templates` com:
  - `Provider`: argumento; vazio → host do remote `origin` (`github.com` →
    `github`, `gitlab.com` ou host com `gitlab` → `gitlab`); sem remote ou host
    desconhecido → erro pedindo `--provider`;
  - `Module`: diretiva `module` do `go.mod`;
  - `Name`: último elemento do módulo, normalizado para `^[a-z][a-z0-9-]*$`
    (falha → erro pedindo para criar a config à mão);
  - `Modules`: argumento; vazio → os módulos de `config.Modules` cujo
    `.claude/agents/<módulo>.md` já existe.
  A config renderizada precisa passar no `config.Load` antes de seguir.

**Plano (TD-31, TD-32).** Para cada arquivo de `templates.Expected(Managed,
modules)` e para `.claude/settings.json`, `.mcp.json` e `.pipeline-claude.json`:

| Situação | Ação | Lock |
|---|---|---|
| gerenciado ausente | `criar` | `files` com o hash do template |
| gerenciado presente, igual ao template (hash TD-23) | `manter` | `files` |
| gerenciado presente, diferente | `do-projeto` | `released` |
| gerenciado presente e não é arquivo regular (symlink, diretório) | erro | — |
| projeto ausente (`settings.json`, `.mcp.json`, config) | `criar` | — |
| projeto presente | `manter` + instrução se faltar conteúdo do toolkit | — |

Instruções (nunca escrita) quando o arquivo existe:
- `.claude/settings.json`: se o hook `pipeline-claude hook` (PreToolUse),
  o `fmt-hook` (PostToolUse) ou as regras `permissions.deny` do template
  faltarem, o trecho JSON exato de cada um. Use a mesma detecção do `doctor`
  (`hookCheck`/`fmtHookCheck`; exporte o que precisar, sem duplicar a regra).
  Hook de outro comando no mesmo evento é mencionado: o projeto decide se
  remove.
- `.mcp.json`: se não registrar o servidor `pipeline-claude`, o trecho.
- `Makefile`: ausente ou sem `PIPELINE_CLAUDE_VERSION` → o bloco
  `PIPELINE_CLAUDE_VERSION := <versão>` + alvo `tools` (mesmo texto do template
  do `init`).
- `CLAUDE.md`: ausente ou sem a seção da pipeline → a seção do template, com o
  aviso de que o resto do arquivo é do projeto.
- `.gitignore`: sem `bin/` (ou `/bin/`) → a linha.
- `git config core.hooksPath`: diferente de `.githooks` → o comando.

Arquivo da área gerenciada fora do catálogo (outro subagente do projeto) não
entra no plano nem no lock; `.claude/commands/`, `docs-ai/`, código e `go.mod`
nunca entram.

**`Apply`.** Grava, nesta ordem: arquivos `criar` (cada um com `O_EXCL`,
diretórios criados componente a componente sem seguir symlink, destino
conferido dentro da raiz após `filepath.Clean`), a config se for criada, e por
último o lock (`manifest.Write`, `toolkit_version` = `ToolkitVersion`). Antes
de cada escrita, confere de novo o disco: se algo apareceu no caminho, para,
remove o que esta execução criou e devolve erro. Falha em qualquer passo →
remove o que esta execução criou (nunca o que já existia) e devolve erro com a
lista do que foi removido.

**Testes:** projeto fictício (git init + commit) sem nada → cria tudo e o
`syncer.Plan` seguinte dá tudo `nada`; com subagente igual ao template →
`manter` e `files`; com subagente customizado → `do-projeto`, `released`, arquivo
intacto byte a byte, e o `sync` seguinte não o toca; com `settings.json` que
tem outro hook → `manter` e instrução com o trecho; config existente válida
(usada) e inválida (erro, nada criado); working tree sujo, sem `go.mod`, lock
existente, symlink na área gerenciada, versão inválida (erro, nada criado);
provedor pelo remote (github, gitlab, host próprio com gitlab, sem remote);
falha injetada na n-ésima escrita (nada criado fica no disco, o que existia
fica igual); `Apply` depois de o disco mudar desde o plano. `FuzzPlan`: para
árvores arbitrárias na área gerenciada, `Plan` nunca entra em pânico, nunca
propõe caminho fora da raiz e nunca marca `criar` em caminho existente.

## 2. `internal/doctor` (checagem `subagentes`)

Checagem nova `subagentes` (TD-34), depois de `sensores`.

- Leitor de front matter de subagente: bloco `---` no início; chaves
  `chave: valor`; `tools` aceita string separada por vírgula **ou** lista YAML em
  bloco (`tools:` seguido de linhas `  - Nome`); outras chaves (`model`,
  `maxTurns`, …) são aceitas e ignoradas. Sem front matter, sem `name` ou
  bloco que não fecha → erro com o arquivo.
- Para cada `.claude/agents/*.md` (gerenciado, `released` ou fora do lock):
  `name` igual ao nome do arquivo; `tools` presente e não vazio (omitido herda
  tudo); fora de `config.EditorAgents`, nenhuma de `Edit`, `Write`,
  `MultiEdit`, `NotebookEdit` nem `Bash`, exceto `git-delivery` com `Bash`
  (TD-29).
- Status: tudo certo → ok com a contagem; violação de ferramenta ou `tools`
  ausente → aviso listando `subagente: ferramenta`, em ordem; front matter
  ilegível ou `name` diferente → erro; sem `.claude/agents/` → aviso; config
  ausente ou inválida → aviso "não verificado".

Testes numa tabela `TestCheckAgents`: projeto do `scaffold` dá ok; revisor com
`Edit`; revisor com `Bash`; `git-delivery` com `Bash` ok; editor com `Edit` ok;
lista YAML em bloco; `name` diferente; front matter sem fechamento; `tools`
ausente; subagente em `released` também é checado. `FuzzAgentFrontMatter`:
entrada arbitrária nunca entra em pânico.

## 3. CLI

Ligue no `cmd/pipeline-claude`, removendo `adopt` de `planned`:

`adopt [--dir <d>] [--provider github|gitlab] [--modules a,b]
[--toolkit-version <v>] [--dry-run] [--json]`.

- `--dir` padrão: diretório atual. Versão padrão: a do binário se
  `pinnable` (como no `init`); senão, uso inválido pedindo `--toolkit-version`.
- Pré-condição ou entrada inválida → 2; falha de escrita → 1; ok → 0.
- Saída humana: uma linha por item (`criar`, `manter`, `do-projeto`), depois as
  instruções numeradas com o trecho de cada uma, e os próximos passos (`make
  tools`, `bin/pipeline-claude doctor`, commit da adoção). JSON
  `{"ok", "items", "instructions", "created"}`.
- `--dry-run`: plano e instruções, nada gravado, exit 0 se o `Apply` seria
  possível.

Teste de ponta a ponta com o binário de teste: projeto fictício com git,
subagente customizado e `settings.json` com outro hook → `adopt --dry-run`
não grava; `adopt` cria o resto e o lock; o subagente customizado fica igual e
em `released`; `doctor --json` mostra `sync` ok e `subagentes` com aviso se o
customizado violar o TD-29; `sync` depois → 0 sem mudança; segundo `adopt` → 2
(lock existe). Atualize `README.md` (estado Fase 4, comando, seção "Adoção") e
o layout do `CLAUDE.md`.

## 4. Guia de adoção

`docs/ADOCAO.md`: passo a passo genérico (pré-condições, `--dry-run`, leitura
das instruções, `settings.json` com hook antigo, sensores pelo `make` quando o
projeto usa build tags, `report_schema` e `flag_front` para regra própria de
report, `editor_agents` e o fim de fluxo de patch, conferência de paridade do
validador com `TestParityCorpus`). Sem nada do projeto piloto.

## Critérios de aceite da Fase 4

- `make check` e CI verdes em todas as PRs; núcleo continua só stdlib.
- `adopt` nunca altera arquivo existente (teste byte a byte) e não deixa resto
  em falha.
- Projeto adotado passa em `config.Load` e nas checagens `configuração`,
  `sync` e `subagentes` do `doctor` (esta pode avisar sobre subagente do
  projeto); `sync` logo depois do `adopt` não muda nada.
- `TestParityCorpus` passa localmente contra o corpus do projeto piloto
  (contagem colada na última PR, sem nomes).
- Nenhum arquivo do pipeline-ai foi alterado (`git -C ~/workspace/pipeline-ai
  status --short` vazio, colado na última PR).

## Fora do escopo desta fase

A migração do projeto piloto acontece no repositório dele, numa branch própria,
depois da fase mesclada: `adopt --dry-run`, revisão das instruções, aplicação,
ajuste da config (`protected_paths`, `editor_agents`, `report_schema`,
sensores) e remoção do hook antigo. Nada dessa migração entra neste
repositório.
