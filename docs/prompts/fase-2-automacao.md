# Prompt — Fase 2: automação do fluxo

> Para colar no Claude Code com o repositório `pipeline-ai-claude` aberto no WSL.
> Pré-requisitos: Fase 1 mesclada (PRs #5 a #12); decisões TD-18 a TD-21 aceitas
> em `docs/DECISOES.md`; clone de leitura do pipeline-ai em `~/workspace/pipeline-ai`
> no commit `0eac73a`. A paridade da Fase 1 (`TestParityCorpus`) pendente não
> bloqueia esta fase.

---

Você vai implementar a **Fase 2** do toolkit: os pacotes `hookio`, `fmthook`,
`sensors`, `delivery` e `mcp`, um ajuste no `doctor`, e ligar `fmt-hook`,
`sensors`, `delivery new` e `mcp` na CLI. Leia antes, nesta ordem: `CLAUDE.md`,
`docs/DECISOES.md` (em especial TD-08, TD-09, TD-16, TD-17 e TD-18 a TD-21),
`testdata/hooks/claude/README.md` e os pacotes da Fase 1 que você vai usar
(`internal/config`, `internal/hook`, `internal/validate`, `internal/doctor`).

## Regras de execução

1. **Uma PR por pacote**, branch `task/TASK-NNN`, na ordem do grafo abaixo. Não
   misture pacotes numa PR, exceto onde o item diz o contrário (`hookio`).
2. **Teste primeiro.** Cada PR começa com testes de tabela que falham; depois o
   código. Parsers (payload do `fmt-hook`, mensagens do MCP, tabela do índice)
   ganham alvo de fuzz (`FuzzXxx`) que roda no `go test` comum pelos seeds.
3. **Portar, não copiar decisão.** `internal/mcp` parte de
   `pipeline-ai/internal/mcp/{protocol,server}.go` e `internal/delivery` parte de
   `pipeline-ai/internal/mcp/scaffold.go` (somente leitura: não edite, não
   commite, não abra PR lá). Cada arquivo portado leva o cabeçalho de origem do
   `CLAUDE.md` e uma linha no `NOTICE`. `hookio`, `fmthook` e `sensors` não têm
   original. Onde o comportamento muda, o teste cita a decisão (`TD-xx`).
4. `make check` verde antes de abrir cada PR; cole comandos e saídas reais na
   descrição, inclusive a falha inicial dos testes.
5. Se uma regra deste prompt conflitar com o `CLAUDE.md` ou o `DECISOES.md`,
   **pare e pergunte**. Não resolva sozinho. Escolhas que o prompt não cobre vão
   numa seção "Pontos que decidi sem especificação explícita" da PR.

## Grafo de execução

```
hookio ──> fmthook
config ─┬─> sensors ──┐
        └─> delivery ─┼─> mcp ──> CLI
validate, doctor ─────┘
doctor (ajuste) ─────────────────> CLI
```

`hookio`, `sensors`, `delivery` e o ajuste do `doctor` são independentes entre si:
podem ir em paralelo, cada um numa worktree (`git worktree add ../pac-<pacote> -b
task/TASK-NNN origin/main`) com um subagente. `fmthook` depois de `hookio`
mesclado; `mcp` depois de `sensors` e `delivery`; CLI por último.

## 1. `internal/hookio` (extração, sem mudança de comportamento)

Extraia do `internal/hook` o que o `fmt-hook` também precisa, e faça o `hook` usar
o pacote novo **na mesma PR**:

- `ReadPayload(r io.Reader) (Payload, error)`: lê stdin, exige objeto JSON, lê as
  chaves por nome exato (`map[string]json.RawMessage`, nunca struct com casamento
  sem caixa) e expõe `HookEventName`, `ToolName`, `AgentType`, `CWD` (com
  presença: ausente × vazio) e `TargetPath()` (`file_path`, ou `notebook_path` no
  `NotebookEdit`). Tipo errado em campo conhecido é erro.
- `ResolveInRoot(root, target string) (rel string, inside bool, err error)`: a
  resolução de TD-09 que hoje está em `hook.relToRoot` (relativo contra a raiz,
  `Clean`, `EvalSymlinks` no maior prefixo existente, link quebrado é erro,
  prefixo da raiz sem diferenciar caixa).

Critério: **nenhum teste do `internal/hook` muda** e todos passam; os testes de
`ReadPayload` e `ResolveInRoot` migram para `hookio` (cópia, não movimento: o
`hook` mantém os seus). `FuzzReadPayload`: nunca entra em pânico; payload aceito é
sempre objeto JSON.

## 2. `internal/fmthook`

`Run(stdin io.Reader, stderr io.Writer, getwd func() (string, error)) int` — hook
`PostToolUse` do Claude Code (TD-18).

Ordem de decisão:
1. Payload ilegível (via `hookio`) → **2**.
2. `hook_event_name` presente e ≠ `PostToolUse` → **2** (registrado no evento
   errado; mensagem aponta o `doctor`).
3. `tool_name ∉ {Edit, Write, MultiEdit}` → 0 (`NotebookEdit` não é Go).
4. Caminho ausente ou vazio → **2**.
5. Config: `config.Load` a partir do `cwd` do payload (ou `getwd`). Ausente ou
   inválida → **2**.
6. `hookio.ResolveInRoot`; fora da raiz → 0.
7. Extensão ≠ `.go` (sem diferenciar caixa) → 0. `IsNoAgent(rel)` → 0 sem tocar
   no arquivo.
8. Arquivo inexistente (a edição pode ter falhado) → 0. Não é arquivo regular → **2**.
9. `go/format.Source`. Erro de sintaxe → **2** com `arquivo:linha:coluna: msg`.
10. Sem diferença → 0 sem gravar. Com diferença: grava no caminho resolvido por
    arquivo temporário no mesmo diretório + `os.Rename`, preservando a permissão;
    falha ao gravar → **2**. Gravado → 0 e uma linha em stderr
    (`pipeline-claude fmt-hook: formatado <rel>`).

Testes com as fixtures reais `posttooluse-*` (raiz `/workspace/exemplo` trocada
pela raiz temporária, como no `hook`):

| Fixture (`testdata/hooks/claude/2.1.309/`) | Preparação | Esperado |
|---|---|---|
| `posttooluse-edit-main.json` | `internal/exemplo/alvo.go` desformatado | 0, arquivo formatado |
| `posttooluse-edit-main.json` | `alvo.go` já formatado | 0, `mtime` inalterado |
| `posttooluse-edit-main.json` | `alvo.go` com erro de sintaxe | **2**, arquivo intacto |
| `posttooluse-write-dev-implementation.json` | `novo.go` desformatado | 0, formatado |
| `posttooluse-write-main.json` / `-pm-triage.json` | `.md` | 0, intacto |
| `posttooluse-notebookedit-main.json` | — | 0 |
| `pretooluse-*.json` (5) | — | **2** (evento errado) |
| derivado: `MultiEdit` em `.go` | **sintético** | 0, formatado |
| derivado: `.go` em `no_agent_paths` | config com `docs-ai/restrito/` | 0, intacto |

Um teste falha se surgir fixture em `testdata/hooks/claude/**` sem linha nesta
tabela. Mais: permissão preservada, symlink para `.go` dentro da raiz (formata o
alvo), symlink para fora da raiz (0, intacto), arquivo `.GO`, diretório com nome
`x.go` (**2**), config ausente. `FuzzRun`: nunca devolve 0 para payload que não
seja objeto JSON com evento ausente ou `PostToolUse`; nunca grava fora da raiz
temporária.

## 3. `internal/sensors`

`Run(ctx, cfg *config.Config, opts Options) Result` e
`Print(w io.Writer, r Result, json bool) int` (TD-19). `Options{Names []string,
Timeout time.Duration}`.

- Sem `Names`: todos os sensores, na ordem da config. Com `Names`: só esses, na
  ordem da config; nome desconhecido é uso inválido (o chamador devolve 2).
- Cada sensor: `exec.CommandContext` com `cmd[0]` e `cmd[1:]`, `Dir = Root()`,
  ambiente herdado, timeout próprio. stdout e stderr juntos, guardando só os
  últimos 8 KiB (`truncated: true` quando cortar).
- Resultado por sensor: `name`, `ok`, `exit_code` (−1 se não rodou), `duration_ms`,
  `output`, `truncated`, `error` (comando não encontrado, timeout).
- Todos rodam mesmo após falha. Nenhum sensor configurado → resultado reprovado
  com mensagem "nenhum sensor configurado" (exit 1).
- Saída humana: uma linha por sensor (`[ok]`/`[falha]`, nome, duração), saída do
  sensor só nos que falharam, resumo. `--json`: `{"ok", "sensors": [...]}`.
  Exit 0 todos ok, 1 algum falhou.

Testes com sensores que são o próprio binário de teste (padrão
`os.Args[0] -test.run=TestHelperProcess` com variável de ambiente), sem depender
de `sh`: sucesso, exit 3, saída maior que o limite, timeout, comando inexistente,
filtro por nome, nome desconhecido, ordem preservada, nenhum sensor, JSON.

## 4. `internal/delivery`

`New(cfg *config.Config, opts NewOptions, now time.Time) (Created, error)`;
`NewOptions{Title, Prefix string}`; `Created{ID, ReportPath, IndexPath string}`
(caminhos relativos à raiz) (TD-17, TD-20).

- **Prefixo**: `opts.Prefix`, ou o único de `delivery_prefixes` se houver só um.
  Vazio com zero ou vários prefixos configurados → erro. Fora de
  `delivery_prefixes` → erro.
- **Título**: obrigatório, uma linha, ≤ 120 caracteres, sem `|` nem caractere de
  controle.
- **Próximo número**: maior `N` entre os IDs `<prefixo>-<N>` nas células da tabela
  do índice e nos nomes de diretório de `deliveries_dir`, mais um; formatado com
  pelo menos 3 dígitos. Comparação do prefixo sensível a caixa. O ID gerado tem de
  casar `delivery_id_pattern` (se configurado), senão erro dizendo que a config é
  incoerente.
- **Índice** (`index_file`): precisa existir e ter uma tabela com cabeçalho. A
  linha nova vai depois da última linha da primeira tabela, com o número de
  colunas do cabeçalho: coluna cujo título normalizado seja `id`, `delivery` ou
  `delivery_id` recebe o ID; `titulo`/`title`, o título; `status`, `draft`;
  `data`/`date`, a data (`2006-01-02`, de `now`); as demais ficam vazias. Sem
  coluna de ID → erro.
- **Report**: `report_template` (se configurado) com os marcadores `<delivery_id>`,
  `<titulo>` e `<data>` substituídos; senão, gerado do `report_schema`: front com
  `front_required` (`delivery_id` preenchido, `status: draft`, os demais vazios) e
  cada chave de `flag_sections` como `false`; cada seção de `sections_required` com
  os campos de `fields_required` como `- <Campo>: <preencher>`; a seção de
  checklist com um item `- [ ] <preencher>`.
- **Escrita**: confere que diretório, report e índice ficam dentro da raiz após
  `filepath.Clean`; `os.Mkdir` (falha se existir), report com `O_EXCL`, índice por
  temporário + rename preservando permissão e fim de linha (LF/CRLF) do original.
  Falha no índice → remove o diretório criado e devolve o erro.

Testes: ID inicial (`-001`), sequência a partir do índice, a partir de diretório
sem linha no índice, buracos (`-001`, `-007` → `-008`), número com 4 dígitos,
prefixo errado, título inválido (vazio, `|`, quebra de linha, longo), ID fora do
`delivery_id_pattern`, índice ausente, índice sem tabela, sem coluna de ID, índice
CRLF, template com marcadores, report gerado do esquema, diretório já existente
(nada sobrescrito), rollback quando o índice falha (índice somente leitura).
**Sincronia com o validador**: um report criado por `New` e depois preenchido
passa em `validate.Run(--no-git)`; o criado sem preencher reprova só por
placeholder/checklist, nunca por índice ou ID. `FuzzIndexTable`: o parser da
tabela nunca entra em pânico e todo ID que ele devolve está numa célula.

## 5. `internal/mcp`

`Serve(ctx, in io.Reader, out, errOut io.Writer, getwd func() (string, error)) error`
— servidor MCP por stdio (TD-21). Porta `protocol.go` e `server.go`.

- **Transporte**: uma mensagem JSON-RPC 2.0 por linha; linha acima de 1 MiB ou
  JSON inválido → resposta de erro `-32700` com `id: null` e o servidor continua.
  Requisição (com `id`) sempre recebe resposta; notificação (sem `id`) nunca. EOF
  → retorna `nil`. stdout só protocolo.
- **Métodos**: `initialize` (devolve a `protocolVersion` pedida se for uma de
  `2024-11-05`, `2025-03-26`, `2025-06-18`, senão a mais nova; `serverInfo` com
  `pipeline-claude` e `buildinfo.Version()`; capacidades `tools` e `resources`),
  `notifications/initialized`, `ping`, `tools/list`, `tools/call`,
  `resources/list`, `resources/read`. Método desconhecido → `-32601`.
- **Config**: carregada a cada chamada a partir de `getwd`. Ausente ou inválida →
  a ferramenta responde `isError: true` com a mensagem e a dica do `doctor`.
- **Ferramentas** (`inputSchema` com `additionalProperties: false`; argumento
  desconhecido ou de tipo errado → `-32602`):
  - `pipeline_doctor` `{json?}` → saída do `doctor`.
  - `pipeline_validate` `{target, base?, no_git?}` → saída `--json` do `validate`;
    reprovado é `isError: true`.
  - `pipeline_sensors` `{names?, timeout_seconds?}` → `sensors` em JSON; falha é
    `isError: true`.
  - `pipeline_delivery_new` `{title, prefix?}` → `delivery.New`. **Recusada** com
    `isError` se `IsProtected(deliveries_dir)` ou `IsProtected(index_file)`
    (TD-16), antes de qualquer escrita.
  - Nada de `sync` nem de escrita genérica.
- **Recursos**: `pipeline://config` (config efetiva em JSON), `pipeline://invariants`
  (conteúdo de cada `invariants_files`, com título por arquivo; arquivo ausente é
  erro do recurso), `pipeline://deliveries/index` (`index_file`). URI desconhecida
  → `-32602`.

Testes por transcrição: tabela de `{entrada (linhas), saída esperada (decodificada e
comparada por campo)}` cobrindo cada método, notificação sem resposta, JSON
inválido seguido de requisição válida, linha longa, versão de protocolo
desconhecida, cada ferramenta com sucesso e erro, `delivery_new` recusada com
`deliveries_dir` protegido (e nada criado em disco), config ausente. `FuzzServe`:
para qualquer entrada, toda linha de stdout é JSON-RPC válido e o servidor
termina.

## 6. `internal/doctor` (ajuste)

Nova checagem `fmt-hook`: `pipeline-claude fmt-hook` registrado em `PostToolUse`
com matcher cobrindo `Edit|Write|MultiEdit` → `ok`; ausente → `aviso` (formatação
é conveniência, não governança); registrado em `PreToolUse` → `erro` (o
`fmt-hook` bloquearia toda edição, regra 2 do §2). A checagem `hook` continua
como está, e as duas não podem confundir os comandos (`hook` × `fmt-hook`). Testes
na tabela existente de `TestCheckHook` e uma nova `TestCheckFmtHook`.

## 7. CLI

Ligue no `cmd/pipeline-claude`, removendo de `planned`:
- `fmt-hook` (payload no stdin; argumento extra é uso inválido).
- `sensors [--json] [--timeout <duração>] [nome...]`.
- `delivery new --title <título> [--prefix <p>] [--json]` (saída: ID e caminho do
  report; JSON `{"id", "report", "index"}`); `delivery` sem subcomando ou com
  subcomando desconhecido é uso inválido.
- `mcp` (sem argumentos; roda até EOF).

Teste de ponta a ponta com o binário compilado em `TestMain`: `fmt-hook` com
fixture real formatando um arquivo; `sensors` com um sensor que passa e um que
falha (usando o próprio binário de teste como comando); `delivery new` duas vezes
gerando IDs sequenciais e `validate --no-git` reconhecendo o índice; sessão `mcp`
com `initialize`, `tools/list` e `tools/call pipeline_validate`. Atualize a
linha de estado do `README.md`.

## Critérios de aceite da Fase 2

- `make check` e CI verdes em todas as PRs; núcleo continua só stdlib.
- Todas as fixtures de hook com decisão declarada e coberta também no `fmt-hook`.
- `hook` sem mudança de comportamento após a extração (`internal/hook` com os
  mesmos testes, todos verdes).
- `delivery new` nunca sobrescreve arquivo e nunca grava fora da raiz (testes
  dedicados).
- `pipeline_delivery_new` recusada via MCP quando o destino é protegido.
- `NOTICE` lista todo arquivo portado.
- Nenhum arquivo do pipeline-ai foi alterado (`git -C ~/workspace/pipeline-ai
  status --short` vazio, colado na última PR).
