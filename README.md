# pipeline-ai-claude

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/actions/workflows/ci.yml/badge.svg)](https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude/actions/workflows/ci.yml)

Toolkit em Go para criar e governar projetos de desenvolvimento assistido por
agentes no **Claude Code**: pipeline de subagentes com gates determinísticos,
relatório de delivery validado e proteção de caminhos sensíveis.

É o equivalente, para o Claude Code, do
[pipeline-ai](https://github.com/rodrigodosanjosoliveira/pipeline-ai) (Antigravity).
Os dois são projetos independentes; código do pipeline-ai é portado com
atribuição (ver [`NOTICE`](NOTICE)).

> **Estado:** v1.0 em preparação (Fase 5). A CLI expõe `init`, `adopt`, `sync`, `hook`, `fmt-hook`, `validate`, `sensors`, `delivery new`, `doctor`, `mcp`, `version` e `help`. Última versão publicada: `v1.0.0-rc.3`; mudanças no [`CHANGELOG.md`](CHANGELOG.md). O que fica estável a partir da v1.0 está em [`docs/CONTRATO.md`](docs/CONTRATO.md).

## Comandos

| Comando | Função |
|---|---|
| `pipeline-claude init <nome>` | Cria um projeto novo (`api` Go + PostgreSQL) num diretório vazio: subagentes, skills, hooks, MCP, pre-push, gates, CI do provedor (`--provider github` ou `gitlab`), documentação de delivery e o lock dos arquivos gerenciados; `--git` inicia o repositório |
| `pipeline-claude adopt` | Instala a pipeline num projeto Go existente: só cria o que falta e o lock, nunca altera arquivo existente e imprime o trecho a acrescentar em cada um; `--dry-run` mostra o plano |
| `pipeline-claude sync` | Atualiza os arquivos gerenciados pelo lock sem sobrescrever edição local; `--dry-run` mostra o plano, `--release <caminho>` passa o arquivo para o projeto |
| `pipeline-claude hook` | `PreToolUse`: bloqueia edição de caminho protegido fora dos agentes autorizados (falha fechada) |
| `pipeline-claude fmt-hook` | `PostToolUse`: formata com `go/format` o `.go` editado (sem `gofmt` no PATH) |
| `pipeline-claude validate` | Valida o relatório de delivery contra o esquema do projeto e o diff completo |
| `pipeline-claude sensors` | Gates do projeto com saída JSON |
| `pipeline-claude delivery new` | Cria o relatório da próxima delivery e registra no índice |
| `pipeline-claude doctor` | Diagnóstico do ambiente e do projeto: hooks, arquivos gerenciados, sensores pedidos e ferramentas de cada subagente |
| `pipeline-claude mcp` | Servidor MCP (stdio) para o Claude Code |

## Uso

Projeto novo com o binário instalado numa versão publicada:

```sh
pipeline-claude init biblioteca-bairro --module example.com/biblioteca-bairro --git
cd biblioteca-bairro && make tools && bin/pipeline-claude doctor
```

O [biblioteca-bairro](https://github.com/rodrigodosanjosoliveira/biblioteca-bairro)
é o projeto público de demonstração (fictício), criado assim pelo `init` e com as
deliveries feitas pelo fluxo de subagentes.

Com um binário de desenvolvimento, informe a versão a fixar no `Makefile`
(`--toolkit-version vX.Y.Z`). Depois de atualizar `PIPELINE_CLAUDE_VERSION` e
rodar `make tools`, `bin/pipeline-claude sync` traz os templates novos; arquivo
editado pelo projeto não é tocado e aparece no `sync` e no `doctor`.

### Adoção

Projeto Go que já existe, com ou sem camada de agentes própria:

```sh
bin/pipeline-claude adopt --dry-run   # plano e instruções, nada gravado
bin/pipeline-claude adopt
```

Passo a passo, configuração e paridade do validador em
[`docs/ADOCAO.md`](docs/ADOCAO.md); validação da delivery no CI (GitLab e GitHub)
em [`docs/CI.md`](docs/CI.md). Projeto criado pelo `init` já nasce com esse CI.

### Versões

Instalação só por `go install` com a versão fixada no `Makefile` do projeto
(`PIPELINE_CLAUDE_VERSION`). Histórico em [`CHANGELOG.md`](CHANGELOG.md);
contrato estável (config, lock, CLI, `--json`, MCP e área gerenciada) e
política de versão em [`docs/CONTRATO.md`](docs/CONTRATO.md).

## Roteiro

| Fase | Entrega |
|---|---|
| 0 | Fundação: repositório, CI, decisões, captura e sanitização de fixtures de hook |
| 1 | `config`, `gitdiff`, `report`, `hook`, `validate`, `doctor` |
| 2 | `fmt-hook`, `sensors`, `delivery new`, `mcp` |
| 3 | Templates, `init`, `sync` |
| 4 | `adopt` e primeiro projeto existente |
| 5 | Contrato, CI em Linux, macOS e Windows, projeto de demonstração e v1.0 |

## Desenvolvimento

Requer Go 1.24+.

```sh
make check   # gofmt, go vet, núcleo só stdlib, testes com -race
make build   # bin/pipeline-claude
make vuln    # govulncheck fixado
```

Regras do projeto em [`CLAUDE.md`](CLAUDE.md); decisões em
[`docs/DECISOES.md`](docs/DECISOES.md).

## Licença

MIT. Ver [`LICENSE`](LICENSE) e [`NOTICE`](NOTICE).
