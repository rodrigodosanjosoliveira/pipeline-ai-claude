# pipeline-ai-claude — guia para o Claude Code

Toolkit em Go que cria e governa projetos de desenvolvimento assistido por
agentes no **Claude Code**. Ocupa, para o Claude Code, o papel que o
[pipeline-ai](https://github.com/rodrigodosanjosoliveira/pipeline-ai) ocupa para o
Antigravity. Decisões em [`docs/DECISOES.md`](docs/DECISOES.md).

## Regras invioláveis

1. **O pipeline-ai é só referência.** Nunca editar, abrir PR, nem depender dele
   (`go.mod`, submódulo, `replace`). Código pode ser **portado** (MIT), com
   cabeçalho de origem e entrada no `NOTICE` (ver "Código portado").
2. **Núcleo só stdlib.** Nenhuma dependência externa no `go.mod`; não pode existir
   `go.sum`. Gate: `make stdlib-only`. Ferramentas (govulncheck) rodam por
   `go run ...@<versão>` fixada no `Makefile`.
3. **Falha fechada.** Payload ilegível, configuração ausente ou inválida, erro de
   git ou caminho que não dá para resolver: o hook **bloqueia** (exit 2) e o
   validador **reprova** (exit 1). Nunca "liberar para não travar a sessão".
4. **Repositório público.** Nada de projeto real de empresa entra aqui: nomes de
   produto, pacotes, tabelas, caminhos locais, reports, IDs de sessão. Fixtures
   de hook só via `tools/fixture-sanitize`; o teste `TestFixturesVersionadas`
   reprova o resto. Exemplos e templates usam domínio fictício.
5. **Teste antes do código.** Cada mudança começa por teste de tabela que falha.
   Fuzz nativo para parsers (`report`, `config`, payload do hook).
6. **Escrita só dentro do alvo.** Todo caminho gerado ou gravado é conferido
   dentro do diretório de destino após `filepath.Clean` (contraditório C18).

## Código portado

Arquivo portado do pipeline-ai começa com:

```go
// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/<caminho> @ 0eac73a (MIT).
// Alterações: <lista curta: o que mudou e por quê, citando TD-xx ou C-xx>.
```

e ganha uma linha na tabela do `NOTICE`. Template portado (arquivo que vai para o
projeto gerado) não leva cabeçalho: a origem fica só no `NOTICE`, uma linha por
grupo (TD-22). Testes portados mantêm os casos
originais; casos cujo comportamento mudou de propósito ficam com comentário
citando a decisão.

## Comandos

```
make check        # fmt-check + vet + stdlib-only + test (-race)
make build        # bin/pipeline-claude com versão via ldflags
make vuln         # govulncheck fixado
make fixtures-check BLOCKLIST=<arquivo privado>
```

## Layout

```
cmd/pipeline-claude     dispatcher da CLI (init e sync em init_sync.go, adopt em adopt.go)
internal/buildinfo      versão do binário
internal/config         carrega e valida .pipeline-claude.json
internal/gitdiff        arquivos alterados (diff completo, TD-11) e diff de revisão (TD-29)
internal/report         parser e checagem do report de delivery
internal/hookio         leitura do payload de hook e resolução de caminho na raiz
internal/hook           hook PreToolUse (bloqueio de caminho protegido)
internal/fmthook        hook PostToolUse (formata .go com go/format)
internal/validate       validação de delivery (report, índice, diff)
internal/sensors        execução dos sensores do projeto (argv, sem shell)
internal/delivery       criação de delivery com ID sequencial e registro no índice
internal/doctor         diagnóstico do ambiente e do projeto
internal/mcp            servidor MCP por stdio (ferramentas e recursos)
internal/manifest       lock dos arquivos gerenciados (.pipeline-claude.lock)
internal/templates      catálogo e templates embutidos da camada gerada (init e sync)
internal/scaffold       init: projeto novo por staging, sem sobrescrever, com lock
internal/syncer         sync dos arquivos gerenciados (plano TD-25, lock, release)
internal/adopt          adoção de projeto Go existente: só cria, lock TD-32, instruções
internal/fixtures       sanitização e checagem de payloads de hook
tools/fixture-sanitize  converte capturas brutas em fixtures publicáveis
scripts/capture-hook.sh grava payload bruto de hook (Fase 0, nunca bloqueia)
testdata/hooks/claude   fixtures sanitizadas por versão do Claude Code
docs/                   decisões, guias e prompts de execução por fatia
```

Pacotes previstos: nenhum. Guia de adoção de projeto existente em
`docs/ADOCAO.md`. Fase 5 (v1.0) em `docs/prompts/fase-5-v1.md`.

## Convenções

- Erros com `fmt.Errorf("contexto: %w", err)`; nunca engolir erro; sem `panic`.
- Mensagens ao usuário em português; identificadores em inglês.
- Códigos de saída: 0 ok · 1 reprovado · 2 bloqueio do hook ou uso inválido.
- Saída humana por padrão; `--json` onde houver automação.
- Branch `task/TASK-NNN`; commit `{type}({scope}): {resumo} [TASK-NNN]`; PR com
  CI verde. Push direto na `main` é bloqueado pela proteção da branch.
- Uma PR por pacote/fatia; evidência (comandos e saídas) na descrição da PR.
