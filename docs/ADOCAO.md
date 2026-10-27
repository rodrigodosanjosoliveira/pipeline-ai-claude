# Adoção de projeto existente

Guia para instalar a pipeline do `pipeline-claude` num projeto Go que já existe,
com ou sem camada de agentes própria. Decisões em `DECISOES.md` (TD-31 a TD-34).

O `adopt` **só cria**: nunca altera arquivo que já existe. O que precisa entrar em
arquivo existente sai como instrução, com o trecho exato, e o `doctor` confere
depois.

## 1. Antes de começar

- Projeto Go (`go.mod` na raiz), `api` + PostgreSQL (TD-07).
- Working tree limpo (`git status --porcelain` vazio): a adoção vira um commit
  revisável, separado de qualquer trabalho em andamento. Faça numa branch
  própria.
- Binário do toolkit numa versão publicada, por exemplo
  `GOBIN=$(pwd)/bin go install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@v0.1.0`.
  Com binário de desenvolvimento, passe `--toolkit-version vX.Y.Z`.

## 2. Ver o plano

```sh
bin/pipeline-claude adopt --dry-run
```

Cada arquivo aparece com uma ação:

| Ação | O que significa |
|---|---|
| `criar` | não existe; o `adopt` cria a partir do template |
| `manter` | existe e fica como está (igual ao template, ou arquivo do projeto) |
| `do-projeto` | subagente, skill ou pre-push que existe diferente do template: fica do projeto (`released` no lock) e o `sync` nunca o toca |

Subagentes customizados ficam como estão: o conhecimento do projeto (regras de
domínio, build tags, aprovações) não é trocado pelo template genérico.

## 3. Aplicar

```sh
bin/pipeline-claude adopt
```

Cria o que falta, `.pipeline-claude.json` (se não existir) e
`.pipeline-claude.lock`. Depois aplique as instruções impressas:

- **`.claude/settings.json` existente:** acrescente o hook `pipeline-claude hook`
  (PreToolUse), o `fmt-hook` (PostToolUse) e as regras de `permissions.deny`.
  Se já houver outro hook de bloqueio, decida se ele continua: com o
  `pipeline-claude hook`, os subagentes de `editor_agents` editam os caminhos
  protegidos e a sessão principal continua bloqueada, o que costuma substituir
  fluxos de "agente gera patch, humano aplica".
- **`Makefile`:** `PIPELINE_CLAUDE_VERSION` e o alvo `tools`.
- **`CLAUDE.md`:** a seção da pipeline; o resto do arquivo continua do projeto.
- **`.gitignore`:** `bin/`.
- **`git config core.hooksPath .githooks`:** para o pre-push gerenciado rodar.

## 4. Ajustar a configuração

A config criada pelo `adopt` sai com os valores do template. Revise antes do
commit:

- **`protected_paths`:** os caminhos que só os editores alteram. Inclua os que
  o projeto já protegia.
- **`editor_agents`:** os subagentes que editam caminho protegido.
- **`delivery_prefixes` e `delivery_id_pattern`:** os prefixos que o índice
  já usa (por exemplo `^(ACV|EMP)-[0-9]{3}$`). Índice com mais de uma tabela
  funciona: a linha nova vai para a primeira tabela com coluna de ID.
- **`report_schema`:** seções e campos do report do projeto. Regra própria do
  tipo "flag X exige campo Y no front matter" vai em `flag_front` (TD-30);
  desligue `forbid_markers` se reports antigos usam TODO como texto.
- **`sensors`:** comandos sem shell. Projeto com build tags ou checagens
  próprias usa os alvos do `make` (`["make", "vet"]`, `["make", "test"]`,
  `["make", "vuln"]`) ou um script do projeto como sensor. Os subagentes
  revisores pedem `vuln` e `mod_verify` pelo nome; o `doctor` avisa se faltarem.

## 5. Conferir

```sh
make tools
bin/pipeline-claude doctor
bin/pipeline-claude sync --dry-run   # nada a fazer logo depois da adoção
```

- `configuração`, `hook`, `fmt-hook` e `sync` precisam dar ok.
- `subagentes` avisa revisor do projeto com ferramenta de escrita ou Bash
  (TD-29). Ajuste o `tools` do subagente ou inclua-o em `editor_agents`.
- `sensores` avisa sensor pedido pelos subagentes e ausente na config.

## 6. Paridade do validador

Se o projeto já tinha um validador de report, compare os vereditos antes de
trocar: monte, **fora de qualquer repositório**, uma pasta com a
`.pipeline-claude.json`, os reports (`<deliveries_dir>/<ID>/report.md`) e um
`expected.json` (`{"<ID>": "ok"|"fail"}`) com o veredito do validador antigo, e
rode no clone do toolkit:

```sh
PIPELINE_CLAUDE_PARITY_CORPUS=/caminho/do/corpus go test -run TestParityCorpus ./internal/validate/
```

Zero divergências: o `pipeline-claude validate` pode substituir o validador
antigo.

## 7. Commit

Revise o diff (só arquivos novos, mais as instruções que você aplicou) e faça o
commit da adoção. Daqui em diante, `bin/pipeline-claude sync` atualiza os
arquivos gerenciados quando a versão do toolkit mudar.
