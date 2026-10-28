# Contrato estável do pipeline-claude

A partir da `v1.0.0`, o que está nesta página só muda de forma incompatível numa
versão maior (TD-37). Projetos fixam a versão do toolkit no `Makefile`
(`PIPELINE_CLAUDE_VERSION`, TD-06) e atualizam pelo `sync`; este contrato diz o
que eles podem esperar de uma versão nova.

## O que é estável

| Parte | Compatível | Incompatível | Arquivo-referência |
|---|---|---|---|
| `.pipeline-claude.json` com `version: 1`: campos, padrões e validação | campo novo opcional | campo removido ou renomeado; validação mais restrita que reprove config válida | `testdata/contract/config.txt` |
| `.pipeline-claude.lock` com `version: 1` | campo novo opcional | campo removido, renomeado ou com outro significado | `testdata/contract/lock.txt` |
| Comandos, opções e códigos de saída da CLI (0 ok, 1 reprovado, 2 bloqueio do hook ou uso inválido) | comando ou opção nova | comando ou opção removida, renomeada, com outro tipo ou outro padrão; código de saída com outro significado | `testdata/contract/cli.txt` |
| Saídas `--json` de `validate`, `doctor`, `sensors`, `delivery new`, `init`, `sync` e `adopt` | chave nova | chave removida ou com tipo diferente | `testdata/contract/json/validate.txt`, `testdata/contract/json/doctor.txt`, `testdata/contract/json/sensors.txt`, `testdata/contract/json/delivery-new.txt`, `testdata/contract/json/init.txt`, `testdata/contract/json/sync.txt`, `testdata/contract/json/adopt.txt` |
| Ferramentas e recursos MCP: nomes, argumentos (`inputSchema`) e URIs | ferramenta, recurso ou argumento opcional novo | ferramenta, recurso ou argumento removido ou renomeado; argumento novo obrigatório | `testdata/contract/mcp.json` |
| Área gerenciada (`.claude/agents/`, `.claude/skills/`, `.githooks/`), destinos dos arquivos gerenciados e o significado de `files` e `released` no lock | arquivo gerenciado novo | prefixo ou destino removido ou movido; `released` voltando a ser tocado pelo `sync` | `testdata/contract/managed.txt` |
| `hook` e `fmt-hook`: protocolo de hook do Claude Code, decisão (exit 0 libera, 2 bloqueia) e falha fechada | — | liberar o que antes bloqueava por erro ou dado ilegível | testes de `internal/hook` e `internal/fmthook` com as fixtures de `testdata/hooks/claude` |

## O que não é contrato

- **Conteúdo dos templates:** texto dos subagentes, skills, pre-push, `Makefile`,
  CI e documentação gerados. Muda em qualquer versão e chega ao projeto pelo
  `sync` (só nos arquivos gerenciados que o projeto não editou, TD-25).
- **Saída humana** dos comandos (sem `--json`): mensagens, ordem e formatação.
  Automação usa `--json`.
- **Descrições** das ferramentas e recursos MCP.
- **Pacotes Go em `internal/`:** não são importáveis por outro módulo e mudam
  livremente.

## Política de versão

- **Patch** (`v1.0.1`): correção que não muda o contrato, mudança de conteúdo de
  template, documentação.
- **Minor** (`v1.1.0`): acréscimo compatível — campo opcional, opção, chave de
  JSON, ferramenta MCP, arquivo gerenciado novo.
- **Major** (`v2.0.0`): qualquer mudança incompatível da tabela acima, com
  caminho de módulo `/v2` (regra do Go):
  `go install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/v2/cmd/pipeline-claude@v2.0.0`.

Toda versão sai como tag anotada na `main` com GitHub Release, instalada só por
`go install` (TD-38). Antes de uma versão maior ou da `v1.0.0`, sai pelo menos
uma `-rc.N`. O histórico fica no [`CHANGELOG.md`](../CHANGELOG.md).

## Testes de contrato

Os arquivos-referência de `testdata/contract/` são comparados pelos testes
`TestContract*` de `cmd/pipeline-claude/contract_test.go`, que leem a CLI, as
saídas `--json` e o MCP do binário de teste, como o usuário os vê, e os campos
de config e lock por reflexão sobre os tipos.

Quando um teste de contrato reprova:

1. Confira se a mudança é intencional e se é compatível pela tabela acima.
2. Regrave os arquivos-referência:

   ```sh
   go test ./cmd/pipeline-claude -run Contract -update
   ```

3. Revise o diff de `testdata/contract/` e registre a mudança na seção
   "Não lançado" do [`CHANGELOG.md`](../CHANGELOG.md). Mudança incompatível
   depois da `v1.0.0` só numa versão maior.

O checklist do [`CONTRIBUTING.md`](../CONTRIBUTING.md) e o template da PR pedem
o mesmo.
