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

> **Estado:** em construção (Fase 1). A CLI expõe `hook`, `validate`, `doctor`, `version` e `help`; os demais comandos saem com código 2 até a fase prevista.

## O que vai fazer

| Comando | Função |
|---|---|
| `pipeline-claude init` | Cria um projeto novo (`api` Go + PostgreSQL) com subagentes, commands, hooks, MCP, gates e documentação de delivery |
| `pipeline-claude adopt` | Instala a pipeline num projeto existente sem tocar em código |
| `pipeline-claude sync` | Atualiza arquivos gerenciados sem sobrescrever edição local |
| `pipeline-claude hook` | `PreToolUse`: bloqueia edição de caminho protegido fora dos agentes autorizados (falha fechada) |
| `pipeline-claude fmt-hook` | `PostToolUse`: `gofmt` no arquivo editado |
| `pipeline-claude validate` | Valida o relatório de delivery contra o esquema do projeto e o diff completo |
| `pipeline-claude sensors` | Gates do projeto com saída JSON |
| `pipeline-claude delivery new` | Cria o relatório da próxima delivery e registra no índice |
| `pipeline-claude doctor` | Diagnóstico do ambiente |
| `pipeline-claude mcp` | Servidor MCP (stdio) para o Claude Code |

## Roteiro

| Fase | Entrega |
|---|---|
| 0 | Fundação: repositório, CI, decisões, captura e sanitização de fixtures de hook |
| 1 | `config`, `gitdiff`, `report`, `hook`, `validate`, `doctor` |
| 2 | `fmt-hook`, `sensors`, `delivery new`, `mcp` |
| 3 | Templates, `init`, `sync` |
| 4 | `adopt` e primeiro projeto existente |
| 5 | Projeto novo piloto e v1.0 |

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
