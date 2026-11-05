# Prompt — Fase 6: v1.1, o que a demo ensinou

> Para colar no Claude Code com o repositório `pipeline-ai-claude` aberto no WSL.
> Pré-requisitos: Fase 5 encerrada (`v1.0.0` publicada, demo `biblioteca-bairro`
> na `v1.0.0` pela DEL-010); decisões TD-41 a TD-46 aceitas em
> `docs/DECISOES.md`.

---

Você vai preparar e publicar a **v1.1**. Ela traz os itens do feedback da
validação da `v1.0.0-rc.4` na demo (DEL-008) que ficaram fora da `v1.0.0` por não
quebrarem o fluxo:
- handoffs em arquivo;
- status do índice calculado pelo git;
- testes pulados visíveis nos sensores;
- aviso de superfície de segurança por import;
- log das decisões do hook;
- capacidades dos revisores;
- duas práticas novas nos templates.

Tudo é acréscimo compatível pelo contrato da v1.0 (TD-37): versão minor, sem
`/v2`.

Leia antes:
- `CLAUDE.md`;
- `docs/CONTRATO.md`;
- `docs/DECISOES.md`, em especial TD-11, TD-22, TD-25, TD-26, TD-37 e TD-41 a
  TD-46;
- os templates de `internal/templates/files/managed/`.

## Regras de execução

1. **Uma PR por fatia**, branch `task/TASK-NNN`, na ordem do grafo abaixo.
2. **Teste primeiro.** Cada PR começa com testes que falham; depois o código.
   Parser novo ganha fuzz nativo.
3. **Contrato.** Toda chave de JSON, opção, campo de config ou ferramenta MCP
   nova aparece no diff de `testdata/contract/`. Regrave com
   `go test ./cmd/pipeline-claude -run Contract -update`. Prove a mudança com
   uma mutação, e registre-a em "Não lançado" do `CHANGELOG.md` na mesma PR.
   Nada pode sumir nem mudar de tipo nos arquivos-referência; se precisar,
   **pare e pergunte**.
4. **Domínio fictício** em tudo (regra 4 do `CLAUDE.md`).
5. `make check` verde antes de abrir cada PR. Cole na descrição os comandos e as
   saídas reais, inclusive a falha inicial dos testes.
6. **Ação externa só com autorização do usuário:** publicar tag ou GitHub
   Release. Peça antes, uma vez por ação.
7. **A demo é conduzida pelo usuário,** numa sessão do Claude Code aberta nela.
   Esta sessão prepara e revisa, e não roda o fluxo de lá.
8. Se uma regra deste prompt conflitar com o `CLAUDE.md` ou o `DECISOES.md`,
   **pare e pergunte**. Escolhas que o prompt não cobre vão numa seção "Pontos
   que decidi sem especificação explícita" da PR.

## Grafo de execução

```
1 handoffs (TD-41) ─────┐
2 index sync (TD-42) ───┼─> 6 templates (TD-46) ─> 7 v1.1.0-rc.1 ─> 8 demo ─> 9 v1.1.0
3 sensores (TD-43) ─────┤
4 doctor imports (TD-44)┤
5 log do hook (TD-45) ──┘
```

As fatias 1 a 5 são independentes e podem ir em paralelo, cada uma numa
worktree. Elas só conflitam no `CHANGELOG.md`; quem mescla depois faz rebase. A
fatia 6 depende das fatias 1 a 3, porque os templates citam as ferramentas
novas.

## 1. Handoffs em arquivo (TD-41)

Ferramentas MCP novas em `internal/mcp`:

- **`pipeline_handoff_put`**
  - Argumentos: `delivery`, `agent` e `content`, todos obrigatórios.
  - Grava `docs-ai/deliveries/<ID>/handoffs/NN-<agent>.md`, com `NN` de 01 a 99.
  - A criação é exclusiva (`O_EXCL`). Se outro revisor paralelo pegou o mesmo
    `NN`, o servidor tenta o próximo número.
  - Devolve o caminho relativo à raiz.
  - Nunca sobrescreve nem apaga.
- **`pipeline_handoff_get`**
  - Argumentos: `delivery` (obrigatório) e `agent` (opcional).
  - Devolve os handoffs da delivery em ordem de `NN`, cada um com o caminho;
    com `agent`, só os daquele subagente.
  - Pasta `handoffs/` inexistente: devolve uma lista vazia, sem erro.

O `put` recusa sem gravar nada (erro MCP com mensagem em português) quando:
- a delivery não existe (pasta ou report ausente);
- `agent` não casa com `^[a-z][a-z0-9-]*$` ou não tem arquivo em
  `.claude/agents/`;
- `content` está vazio, tem NUL ou passa de 64 KiB;
- o caminho final sai da pasta da delivery depois de `filepath.Clean`;
- já existem 99 handoffs.

Testes:
- tabela com cada recusa acima;
- `put` seguido de `get` devolve o mesmo conteúdo;
- `put`s concorrentes (`-race`, N goroutines) geram N arquivos distintos sem
  perda;
- `get` com e sem `agent`;
- fuzz dos argumentos do `put`, sem escrita fora da pasta;
- `mcp.json` regravado com as duas ferramentas.

## 2. `index sync` (TD-42)

Novo comando `pipeline-claude index sync [--base <ref>] [--dry-run] [--json]` e
ferramenta MCP `pipeline_index_sync` (argumentos `base` e `dry_run`). O pacote
fica em `internal/delivery` ou num `internal/index` novo; registre a escolha na
PR e no layout do `CLAUDE.md`.

Para cada linha `draft` ou `em revisão` do `INDEX.md`, procure um commit com
`git log --fixed-strings --grep "[<ID>]" --format=%H <base>`. Se achar, muda
para `merged`:
- a coluna Status da linha;
- a chave `status` do front matter de `docs-ai/deliveries/<ID>/report.md`.

Não mexe em mais nada, e o resto dos dois arquivos fica byte a byte, inclusive
CRLF.

Saídas:
- humana: cada mudança e o resumo;
- `--json`: `{"ok", "changes": [{"id", "from", "to", "commit"}]}`.

Códigos de saída:
- 0: ok, com ou sem mudança;
- 1: erro de git, índice ilegível ou report que não dá para atualizar, sem
  gravar nada (falha fechada);
- 2: uso inválido.

Testes:
- repositório git fictício com deliveries `draft`, `em revisão`, `merged` e com
  outro status;
- só as duas primeiras viram `merged`, e só quando há commit;
- ID que é prefixo de outro (`DEL-1` e `DEL-10`) não casa errado;
- `--dry-run` não grava;
- `base` inexistente sai com 1 sem gravar;
- idempotente: a segunda execução não muda nada;
- CRLF preservado;
- contrato atualizado em `cli.txt`, `json/index-sync.txt` (novo) e `mcp.json`.

## 3. Sensores com testes pulados visíveis (TD-43)

Em `internal/config`, `Sensor` ganha dois campos opcionais:
- `format`: só `go-test-json`;
- `skips`: `warn` ou `fail`, e só vale com `format`.

Valor desconhecido reprova a config.

Com `format: go-test-json`, `internal/sensors` lê a saída como eventos do
`go test -json` (`test2json`). O campo `output` do sensor vira um resumo:
- pacotes;
- testes que passaram, falharam e pularam;
- cada teste pulado com a linha do motivo (`--- SKIP` mais o texto do `t.Skip`);
- a saída dos testes que falharam.

O resultado ganha a chave `skipped` (número). Ela também existe, com 0, em
sensor sem `format`.

Regras:
- `skips: warn` mostra `AVISO: N teste(s) pulado(s)` na saída humana e mantém o
  sensor verde;
- `skips: fail` reprova com `skipped` > 0;
- linha que não é JSON válido de evento reprova o sensor, com o erro.

O `init` passa a gerar o sensor `test` como
`["go", "test", "-json", "./..."]`, com `format: go-test-json`. O `CHANGELOG`
diz como trocar à mão num projeto existente, porque a config não é gerenciada.

Testes:
- parser com eventos reais de `go test -json` de um pacote fictício com pass,
  fail e skip;
- fuzz do parser;
- `warn` e `fail`;
- linha ilegível;
- config com `format` ou `skips` inválidos;
- e2e do `init` com o sensor novo;
- contrato atualizado em `config.txt` e `json/sensors.txt`.

## 4. `doctor`: imports sensíveis fora da superfície (TD-44)

O `doctor` percorre os pacotes Go do projeto:
- ignora `_test.go`, `vendor/`, `testdata/` e diretórios que começam com `.`
  ou `_`;
- lê só os imports, com `go/parser` e `parser.ImportsOnly`.

Avisa quando um pacote importa algo da lista de imports sensíveis (casamento
por prefixo de caminho de import) e o diretório dele não está coberto por
`security_surface_paths`. Use a mesma regra de cobertura do validador; não
escreva uma nova.

Configuração:
- lista padrão: `database/sql`, `os/exec`, `github.com/jackc/pgx` e
  `github.com/lib/pq`;
- campo opcional `security_surface_imports` (lista não vazia de caminhos de
  import) substitui a lista padrão.

Arquivo `.go` ilegível vira aviso com o caminho; não derruba o `doctor`.

Testes:
- projeto fictício com `internal/migrate` importando `pgx` fora da superfície
  (avisa) e dentro (não avisa);
- `os/exec` em `cmd/`;
- import só em `_test.go` (não avisa);
- `security_surface_imports` substituindo a lista;
- contrato atualizado em `config.txt`.

## 5. Log das decisões do hook (TD-45)

Em `internal/config`, campo opcional `hook_log`: caminho relativo, que precisa
ficar dentro da raiz depois de `filepath.Clean`. Absoluto ou com `..` que saia
da raiz reprova a config.

Com o campo definido, `internal/hook` acrescenta uma linha JSON por decisão,
com os campos:
- `time` (UTC, RFC 3339);
- `tool`;
- `path` (relativo à raiz, com `/`);
- `agent` (o `agent_type` do payload, vazio na sessão principal);
- `decision` (`allow` ou `deny`);
- `rule` (código curto da regra que decidiu).

Regras:
- nunca grava conteúdo da edição;
- abre em modo append, com uma escrita por linha;
- Edit/Write no próprio arquivo de log é sempre bloqueado, para qualquer agente;
- falha ao abrir ou gravar o log bloqueia (exit 2) com mensagem que começa por
  `BLOQUEADO pelo pipeline-claude`.

O `init` gera `"hook_log": ".pipeline-claude/hook-log.jsonl"` e acrescenta
`/.pipeline-claude/` ao `.gitignore`.

Testes:
- allow e deny geram uma linha cada, com os campos certos;
- o conteúdo do `new_string` nunca aparece no log;
- edição do próprio log é bloqueada;
- pasta do log sem permissão de escrita bloqueia (pule no Windows com motivo,
  TD-39);
- sem `hook_log`, nada é gravado;
- fixtures do hook continuam passando;
- contrato atualizado em `config.txt`;
- e2e do `init`.

## 6. Templates (TD-41, TD-42, TD-46 e práticas)

Mudança de conteúdo de template: chega aos projetos pelo `sync`. Não é contrato,
salvo o frontmatter `tools:`, que os testes de template conferem.

Mudanças por arquivo:

- **`fluxo`**
  - A regra 1 chama `pipeline_index_sync` no lugar do `git log` manual.
  - A regra 3 passa ao próximo subagente o caminho do handoff anterior, não um
    resumo.
  - A sessão principal não reescreve handoffs.
- **Subagentes do fluxo**
  - Ganham `pipeline_handoff_put` e `pipeline_handoff_get` em `tools:`.
  - Terminam gravando o handoff e devolvendo só o caminho, `STATUS`, `PROXIMO`
    e `SINAIS`.
- **`docs-update`**
  - Deixa de receber `MESCLADAS`; muda só a própria linha para `em revisão`.
  - A regra de insistir com `Edit` quando a recusa não vem do hook vale só para
    `docs-ai/deliveries/<ID>/report.md`. Esse é o achado BAIXO-1 do
    `security-review` da DEL-009 da demo.
- **`migration-reviewer`**
  - Ganha `pipeline_sensors` (TD-46).
  - Nenhum revisor ganha `Bash`.
- **`test-writer`**: prática de mutantes, que surgiu na DEL-008.
  - Quando houver uma implementação de referência viável, valida que os testes
    falham contra ela com mutações simples, numa cópia descartável fora da
    árvore do projeto.
  - Nunca deixa a cópia nem os mutantes no diff.
  - Registra no handoff `Mutantes: N mortos, M sobreviventes`, com o motivo de
    cada sobrevivente.
- **`qa-validation`**: cita o `skipped` do sensor de teste como evidência. Com
  `skipped` > 0 em teste de camada de banco, o resultado é `parcial` com o
  motivo, não `ok`.

Testes de template:
- `tools:` de cada agente conferido por tabela, inclusive nenhum revisor com
  `Bash`;
- o `fluxo` cita `pipeline_index_sync` e `pipeline_handoff_get` e não cita mais
  `git log --fixed-strings`;
- o `docs-update` restringe o `Edit` ao report;
- o `test-writer` traz a linha `Mutantes:`.

## 7. `v1.1.0-rc.1`

Com as fatias 1 a 6 mescladas e o CI verde nas três plataformas:
1. Numa PR, mova o "Não lançado" do `CHANGELOG.md` para `v1.1.0-rc.1`. A seção
   inclui a lista do que o projeto existente troca à mão:
   - o sensor `test`;
   - `hook_log` e `.gitignore`;
   - `security_surface_imports`, se quiser.
2. **Com autorização do usuário**, publique a tag anotada e a GitHub Release
   como pre-release.
3. Confira o `go install …@v1.1.0-rc.1`.

## 8. Validação na demo

O usuário sobe a demo para `v1.1.0-rc.1` pelo `sync`, aplica à mão as trocas de
config da seção do CHANGELOG e roda **uma delivery de código** pelo fluxo
completo. Não serve só a atualização: precisa tocar `internal/` e uma migração,
para exercitar `migration-reviewer` e `security-review`.

Esta sessão revisa a PR da demo e confere:
- os handoffs em `docs-ai/deliveries/<ID>/handoffs/`, sem resumos reescritos
  pela sessão principal;
- o `INDEX.md` atualizado pelo `index sync`;
- o sensor de teste mostrando os pulados (sem a variável do banco) e o QA como
  `parcial` por isso;
- o `doctor` sem aviso de import fora da superfície, ou com o aviso tratado na
  própria delivery;
- o log do hook com pelo menos um `deny` real de uma sessão. Se o fluxo não
  gerar um bloqueio sozinho, o usuário provoca um: por exemplo, pede à sessão
  principal para editar `internal/`.

Anote todo atrito. Atrito que quebra o fluxo ou o contrato é corrigido antes da
`v1.1.0`, numa PR própria, com nova `-rc.N` se mudar código.

## 9. `v1.1.0`

Sem atrito pendente:
1. "Não lançado" vira `v1.1.0`, e o README atualiza a última versão.
2. **Com autorização do usuário**, publique a tag anotada `v1.1.0` e a GitHub
   Release estável.
3. A demo sobe para a `v1.1.0` pelo `sync`, numa PR dela com CI verde.

## Critérios de aceite da Fase 6

- `make check` e CI verdes em todas as PRs, com o job de testes verde em Linux,
  macOS e Windows; o núcleo continua só stdlib.
- Contrato só com acréscimos, provados por mutação e registrados no
  `CHANGELOG.md`.
- Handoffs gravados pelos subagentes e lidos pelo próximo, sem reescrita pela
  sessão principal, na demo.
- `index sync` determinístico e usado pelo `/fluxo`.
- Sensor de teste mostrando os testes pulados e o QA citando-os.
- `doctor` cruzando imports com `security_surface_paths`.
- Log do hook com um `deny` real na demo.
- Nenhum revisor só leitura com `Bash`; `migration-reviewer` com
  `pipeline_sensors`.
- Tags `v1.1.0-rc.1` e `v1.1.0` e GitHub Release da `v1.1.0`, cada uma com
  autorização do usuário.

## Fora do escopo desta fase

- Plugin do Claude Code (ponto em aberto 1).
- Binários pré-compilados (TD-38).
- Tipos de projeto além de `api` + PostgreSQL (TD-07).
- O `doctor` cruzar as variáveis de ambiente do CI com as locais: o resumo do
  sensor já mostra o motivo de cada teste pulado.
- Validação real de `adopt`, GitLab, `release-ops` e `tenant-isolation`, que
  continuam como limitações conhecidas.
