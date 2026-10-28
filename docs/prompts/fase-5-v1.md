# Prompt — Fase 5: projeto de demonstração e v1.0

> Para colar no Claude Code com o repositório `pipeline-ai-claude` aberto no WSL.
> Pré-requisitos: Fases 0 a 4 e ajustes posteriores mesclados (até a PR #38);
> `v0.3.0` publicada; decisões TD-36 a TD-39 aceitas em `docs/DECISOES.md`.

---

Você vai preparar e publicar a **v1.0** do toolkit: congelar o contrato com
testes, documentar o contrato e o histórico de versões, rodar o CI em Linux,
macOS e Windows, levar ao `init` o que os pilotos ensinaram, e provar tudo num
projeto público de demonstração criado pelo `init`. Leia antes: `CLAUDE.md`,
`docs/DECISOES.md` (em especial TD-06, TD-07, TD-09, TD-22 a TD-26, TD-29,
TD-35 e TD-36 a TD-39), `docs/ADOCAO.md` e `docs/CI.md`.

## Regras de execução

1. **Uma PR por fatia**, branch `task/TASK-NNN`, na ordem do grafo abaixo.
2. **Teste primeiro.** Cada PR começa com testes que falham; depois o código.
3. **Domínio fictício** em tudo (regra 4 do `CLAUDE.md`). O projeto de
   demonstração é fictício e público; nada de projeto real entra nele.
4. `make check` verde antes de abrir cada PR; cole comandos e saídas reais na
   descrição, inclusive a falha inicial dos testes.
5. **Ação externa só com autorização do usuário:** criar repositório, publicar
   tag ou GitHub Release. Peça antes, uma vez por ação.
6. Se uma regra deste prompt conflitar com o `CLAUDE.md` ou o `DECISOES.md`,
   **pare e pergunte**. Escolhas que o prompt não cobre vão numa seção "Pontos
   que decidi sem especificação explícita" da PR.

## Grafo de execução

```
contrato (testes) ─┐
CI multiplataforma ┼─> CHANGELOG e CONTRATO.md ─> v1.0.0-rc.1 ─> demo ─> v1.0.0
init (CI e ignore) ┘
```

As três primeiras fatias são independentes e podem ir em paralelo, cada uma
numa worktree.

## 1. Testes de contrato (TD-37)

Arquivos-referência em `testdata/contract/`, comparados por testes; `go test
./... -run Contract -update` regrava (flag de teste, padrão `false`). Mudança num
arquivo-referência aparece no diff da PR e exige entrada no `CHANGELOG.md`
(seção "Não lançado"); o checklist do `CONTRIBUTING.md` passa a pedir isso.

- **`config.txt`:** cada campo do `.pipeline-claude.json` com caminho JSON
  (`report_schema.flag_sections.*.fields`), tipo e se é obrigatório, gerado por
  reflexão sobre os tipos de `internal/config` (tags `json`), em ordem.
- **`lock.txt`:** o mesmo para `manifest.Lock`.
- **`cli.txt`:** cada comando com suas opções (nome, tipo, padrão), lido do
  `-h` de cada comando do binário de teste; mais a tabela de códigos de saída.
- **`json/<comando>.txt`:** para `validate`, `doctor`, `sensors`, `delivery
  new`, `init`, `sync` e `adopt` com `--json`, os caminhos das chaves e o tipo de
  cada valor (`ok: bool`, `items[].action: string`…), não os valores. Rodados
  num projeto fictício como os testes de ponta a ponta já fazem.
- **`mcp.json`:** `tools/list` (nome, `inputSchema`) e `resources/list` (URI,
  nome), sem as descrições.
- **`managed.txt`:** os prefixos da área gerenciada (`manifest.ManagedArea`) e
  os destinos do catálogo gerenciado (`templates.Catalog()` com classe
  `Managed`) — os destinos são contrato; o conteúdo, não.

Testes: cada arquivo-referência confere; mudar um campo, uma opção, uma chave de
JSON ou uma ferramenta MCP faz o teste correspondente falhar (prove com uma
mutação por arquivo); `-update` regrava de forma determinística (duas execuções,
mesmo resultado).

## 2. CI em Linux, macOS e Windows (TD-39)

`ci.yml`: o job de testes com `go-version-file: go.mod` passa a ter matriz
`ubuntu-latest`, `macos-latest`, `windows-latest` (actions fixadas por SHA, como
hoje). Os gates de formatação, `stdlib-only`, `govulncheck` e build continuam só
em Linux.

Rode a suíte no Windows e no macOS (pelo CI da PR) e corrija o que quebrar. Teste
que depende de `sh`, permissão Unix, symlink ou `os.Geteuid` pula com
`t.Skipf` e o motivo — nunca em silêncio e nunca por tolerância a erro. Falha
real de comportamento (caminho com `\`, caixa, CRLF, fim de linha do índice,
nome de binário `.exe`) é corrigida no código, com teste.

## 3. `init`: o que os pilotos ensinaram

- **CI do projeto gerado** (projeto, não gerenciado; TD-22): com
  `--provider github`, `.github/workflows/delivery.yml`; com `gitlab`,
  `.gitlab-ci.yml`. Os dois a partir das receitas do `docs/CI.md` (validação da
  delivery em PR/MR de branch `task/<ID>`) mais um job de gates com `make tools`
  e `bin/pipeline-claude sensors`. GitHub: actions fixadas por SHA e
  `head_ref`/`base_ref` por variável de ambiente.
- **`.gitignore` gerado** com `.claude/settings.local.json` (a aceitação do
  servidor MCP e as permissões locais do Claude Code ficam lá).

Testes: arquivo de CI certo por provedor; YAML do GitHub sem interpolação de
`${{ github.head_ref }}` dentro de `run`; `.gitignore` com a linha; e2e do
`init` atualizado. `docs/CI.md` passa a dizer que projeto novo já nasce com o CI.

## 4. `CHANGELOG.md` e `docs/CONTRATO.md` (TD-37, TD-38)

- `CHANGELOG.md` no formato "Keep a Changelog", em português: seções
  `v0.1.0`, `v0.2.0` e `v0.3.0` reconstruídas das tags e das PRs (o que cada
  versão trouxe, em linguagem de quem usa) e "Não lançado".
- `docs/CONTRATO.md`: o que é estável a partir da v1.0 (lista do TD-37), o que
  não é (conteúdo dos templates, saída humana), a política de versão (patch,
  minor, major com `/v2`) e onde estão os testes de contrato.
- README: estado "v1.0 em preparação", links para os dois e para o projeto de
  demonstração (depois de criado).

## 5. `v1.0.0-rc.1`

Com as fatias 1 a 4 mescladas e o CI verde nas três plataformas: mova o "Não
lançado" do `CHANGELOG.md` para `v1.0.0-rc.1` numa PR, e, **com autorização do
usuário**, publique a tag anotada `v1.0.0-rc.1` e confira `go install
…@v1.0.0-rc.1`.

## 6. Projeto de demonstração (TD-36)

**Com autorização do usuário**, crie o repositório público
`biblioteca-bairro` (sugestão de dono: o mesmo deste) e:

1. `pipeline-claude init biblioteca-bairro --module
   github.com/<dono>/biblioteca-bairro --provider github --toolkit-version
   v1.0.0-rc.1 --git`, primeiro commit e push.
2. CI verde no primeiro push (gates; a validação da delivery só roda em PR de
   `task/<ID>`).
3. **Pelo menos uma delivery pelo fluxo completo**, numa sessão do Claude Code
   aberta no projeto de demonstração, conduzida pelo usuário (esta sessão
   prepara e revisa; não roda o fluxo de lá): por exemplo `DEL-001` "multa por
   atraso na devolução", com teste, código em `internal/` pelo
   `dev-implementation` e a PR validada no CI.
4. README do projeto de demonstração explicando o que ele é e apontando para o
   toolkit.

Anote, numa issue ou numa PR do toolkit, todo atrito encontrado. Atrito que
quebra o contrato ou o fluxo é corrigido antes da v1.0.0, numa PR própria.

## 7. `v1.0.0`

Sem atrito pendente: "Não lançado" vira `v1.0.0`, README sai de "em preparação",
e, **com autorização do usuário**, tag anotada `v1.0.0` e GitHub Release com as
notas da versão. O projeto de demonstração sobe para a `v1.0.0` pelo `sync`
(`PIPELINE_CLAUDE_VERSION`, `make tools`, `sync`), numa PR dele com CI verde.

## Critérios de aceite da Fase 5

- `make check` e CI verdes em todas as PRs, com o job de testes verde em Linux,
  macOS e Windows; núcleo continua só stdlib.
- Testes de contrato cobrindo config, lock, CLI, saídas JSON, MCP e área
  gerenciada, cada um provado por mutação.
- `CHANGELOG.md` com todas as versões e `docs/CONTRATO.md` publicados.
- Projeto gerado pelo `init` nasce com CI e com `.claude/settings.local.json`
  ignorado.
- Projeto de demonstração público, criado pelo `init`, com CI verde e ao menos
  uma delivery pelo fluxo completo, atualizado para a `v1.0.0` pelo `sync`.
- Tags `v1.0.0-rc.1` e `v1.0.0` e GitHub Release da `v1.0.0`, cada uma com
  autorização do usuário.

## Fora do escopo desta fase

Plugin do Claude Code (ponto em aberto 1), binários pré-compilados (TD-38) e
tipos de projeto além de `api` + PostgreSQL (TD-07).
