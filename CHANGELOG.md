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

[Não lançado]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v0.3.0...HEAD
[v0.3.0]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v0.2.0...v0.3.0
[v0.2.0]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/releases/tag/v0.1.0
