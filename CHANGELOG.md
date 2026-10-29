# Changelog

Mudanças do `pipeline-claude` por versão, no formato
[Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/). A partir da
`v1.0.0` o projeto segue [versionamento semântico](https://semver.org/lang/pt-BR/)
sobre o contrato descrito em [`docs/CONTRATO.md`](docs/CONTRATO.md).

Instalação de uma versão (TD-06, TD-38):

```sh
go install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@<versão>
```

## [Não lançado]

## [v1.0.0-rc.3] - 2026-10-29

Terceiro candidato da v1.0: corrige os atritos da revisão das primeiras
deliveries do projeto de demonstração (TD-36), feitas com a `v1.0.0-rc.2`.

### Adicionado

- `init` avisa (em stderr, sem mudar o código de saída) quando o último
  elemento do `--module`, sem o sufixo `/vN`, difere do nome do projeto:
  costuma ser erro de digitação.
- Template do report com a seção "Code Review" (`Resultado` e `Bloqueios e
  sugestoes`); o `code-reviewer` diz que a saída dele alimenta essa seção.

### Corrigido

- Checklist do template do report: o item dos sensores cita todos os do
  `.pipeline-claude.json`, não só `vet`, `test` e `build`.

## [v1.0.0-rc.2] - 2026-10-28

Segundo candidato da v1.0, usado pelo projeto de demonstração (TD-36): corrige
os atritos do ensaio com a `v1.0.0-rc.1`. A revisão das primeiras deliveries da
demo levou à `v1.0.0-rc.3`.

### Corrigido

- CI gerado pelo `init`: o job de gates roda no Go estável (`go-version:
  stable` no GitHub, `golang:1` no GitLab), não no Go do `go.mod`. Com a linha
  do `go.mod` sem correção de segurança, o sensor `vuln` reprovava o primeiro
  push pela biblioteca padrão do toolchain. A validação da delivery continua no
  Go do `go.mod`.
- `.gitignore` gerado ignora `/<nome>`, o binário que o sensor `build`
  (`go build ./...`) grava na raiz do projeto.
- `pre-push` gerenciado aceita o primeiro push que cria a `main` num remoto
  onde ela ainda não existe (repositório vazio), sem `--no-verify`; push para
  uma `main` que já existe continua bloqueado. Chega aos projetos pelo `sync`.

## [v1.0.0-rc.1] - 2026-10-28

Primeiro candidato da v1.0 (TD-38). O ensaio do projeto de demonstração com
ela levou à `v1.0.0-rc.2`.

### Adicionado

- Contrato estável da v1.0 congelado por testes (TD-37): campos do
  `.pipeline-claude.json` e do `.pipeline-claude.lock`, comandos, opções e
  códigos de saída da CLI, chaves e tipos das saídas `--json`, ferramentas e
  recursos MCP e destinos da área gerenciada, em `testdata/contract/`.
- `init` gera o CI do projeto pelo provedor: `.github/workflows/delivery.yml`
  com `--provider github` e `.gitlab-ci.yml` com `--provider gitlab`, com o job
  de gates (`make tools` e `pipeline-claude sensors`) e a validação da delivery
  em PR/MR de branch `task/<ID>`. O `adopt` continua sem criar CI.
- `.gitignore` do projeto novo ignora `.claude/settings.local.json` (aceite do
  servidor MCP e permissões locais do Claude Code).
- `CHANGELOG.md` e [`docs/CONTRATO.md`](docs/CONTRATO.md): o que é estável a
  partir da v1.0 e a política de versão.

### Corrigido

- `doctor` lê `Makefile` e `go.mod` com fim de linha CRLF: a versão fixada do
  toolkit e a versão do Go deixam de sair como ausentes no Windows.

### Alterado

- CI do toolkit roda os testes também em macOS e Windows (TD-39); o
  repositório fixa fim de linha LF (`.gitattributes`).

## [v0.3.0] - 2026-10-28

### Adicionado

- `report_writers` no `.pipeline-claude.json` (TD-35): revisor que escreve a
  própria seção do report pode editar só dentro de `deliveries_dir`; o hook
  bloqueia o resto. O `doctor` aceita Edit e Write nesses revisores, mas não
  Bash.
- [`docs/CI.md`](docs/CI.md): receitas para validar a delivery no GitLab CI e
  no GitHub Actions.

## [v0.2.0] - 2026-10-27

### Adicionado

- Comando `adopt` (TD-31 a TD-33): instala a pipeline num projeto Go que já
  existe. Só cria o que falta, nunca altera arquivo existente, registra no lock
  como `released` o que já existia diferente do template e imprime o trecho a
  acrescentar em cada arquivo existente; `--dry-run` mostra o plano.
- `doctor` confere as ferramentas de cada subagente em `.claude/agents/`,
  gerenciado ou do projeto (TD-34): revisor com escrita ou Bash é apontado.
- Guia de adoção em [`docs/ADOCAO.md`](docs/ADOCAO.md).

## [v0.1.0] - 2026-10-27

Primeira versão instalável (Fases 0 a 3).

### Adicionado

- `hook` (`PreToolUse`): bloqueia edição de caminho protegido fora dos agentes
  autorizados, com falha fechada (payload ilegível, config inválida ou erro de
  git bloqueiam).
- `fmt-hook` (`PostToolUse`): formata o `.go` editado com `go/format`, sem
  depender de `gofmt` no PATH.
- `validate`: confere o report de delivery contra o `report_schema` do projeto,
  o índice de deliveries e o diff completo da branch; `flag_front` exige campo
  do front matter quando a flag é verdadeira (TD-30).
- `sensors`: roda os gates do projeto por argv, sem shell, com timeout e saída
  limitada; `--json` para automação.
- `delivery new`: cria o report da próxima delivery com ID sequencial e registra
  no índice, sem sobrescrever.
- `doctor`: diagnóstico do ambiente e do projeto (Go, binário fixado, hooks
  registrados, arquivos gerenciados, sensores pedidos).
- `mcp`: servidor MCP por stdio com `pipeline_doctor`, `pipeline_validate`,
  `pipeline_sensors`, `pipeline_delivery_new` e `pipeline_diff` (diff de
  revisão só leitura, TD-29), e os recursos de configuração efetiva, índice de
  deliveries e invariantes do projeto.
- `init`: projeto novo `api` Go + PostgreSQL com subagentes, skills, hooks, MCP,
  pre-push, gates e documentação de delivery, gerado por staging e com o lock
  `.pipeline-claude.lock`; `--git` inicia o repositório.
- `sync`: atualiza os arquivos gerenciados pelo lock sem sobrescrever edição
  local; `--dry-run` e `--release <caminho>`.
- Revisores gerados só leitura: diff e sensores chegam pelo MCP (TD-29).
- `enabled_modules` recusa módulo repetido (TD-28).

[Não lançado]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v1.0.0-rc.3...HEAD
[v1.0.0-rc.3]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v1.0.0-rc.2...v1.0.0-rc.3
[v1.0.0-rc.2]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v1.0.0-rc.1...v1.0.0-rc.2
[v1.0.0-rc.1]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v0.3.0...v1.0.0-rc.1
[v0.3.0]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/releases/tag/v0.1.0
