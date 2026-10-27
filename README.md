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

> **Estado:** em construção (Fase 3). A CLI expõe `init`, `sync`, `hook`, `fmt-hook`, `validate`, `sensors`, `delivery new`, `doctor`, `mcp`, `version` e `help`; `adopt` sai com código 2 até a Fase 4.

## Comandos

| Comando | Função |
|---|---|
| `pipeline-claude init <nome>` | Cria um projeto novo (`api` Go + PostgreSQL) num diretório vazio: subagentes, skills, hooks, MCP, pre-push, gates, documentação de delivery e o lock dos arquivos gerenciados; `--git` inicia o repositório |
| `pipeline-claude adopt` | (Fase 4) Instala a pipeline num projeto existente sem tocar em código |
| `pipeline-claude sync` | Atualiza os arquivos gerenciados pelo lock sem sobrescrever edição local; `--dry-run` mostra o plano, `--release <caminho>` passa o arquivo para o projeto |
| `pipeline-claude hook` | `PreToolUse`: bloqueia edição de caminho protegido fora dos agentes autorizados (falha fechada) |
| `pipeline-claude fmt-hook` | `PostToolUse`: formata com `go/format` o `.go` editado (sem `gofmt` no PATH) |
| `pipeline-claude validate` | Valida o relatório de delivery contra o esquema do projeto e o diff completo |
| `pipeline-claude sensors` | Gates do projeto com saída JSON |
| `pipeline-claude delivery new` | Cria o relatório da próxima delivery e registra no índice |
| `pipeline-claude doctor` | Diagnóstico do ambiente e do projeto, inclusive arquivos gerenciados fora do lock |
| `pipeline-claude mcp` | Servidor MCP (stdio) para o Claude Code |

## Uso

Projeto novo com o binário instalado numa versão publicada:

```sh
pipeline-claude init biblioteca-bairro --module example.com/biblioteca-bairro --git
cd biblioteca-bairro && make tools && bin/pipeline-claude doctor
```

Com um binário de desenvolvimento, informe a versão a fixar no `Makefile`
(`--toolkit-version vX.Y.Z`). Depois de atualizar `PIPELINE_CLAUDE_VERSION` e
rodar `make tools`, `bin/pipeline-claude sync` traz os templates novos; arquivo
editado pelo projeto não é tocado e aparece no `sync` e no `doctor`.

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
