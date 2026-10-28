# Decisões do toolkit pipeline-ai-claude

Registro das decisões que definem o toolkit, com o motivo de cada uma. Marcações
`C-xx` identificam divergências deliberadas em relação ao pipeline-ai; a
divergência está descrita na própria decisão.

**Referência de leitura:** pipeline-ai `main` em `0eac73a` (v0.5.0 + tipo `cli`).

## Escopo e distribuição

### TD-01 — Projeto apartado do pipeline-ai
O toolkit não altera nem depende do pipeline-ai, que segue fixado no Antigravity.
Código pode ser portado (MIT) com registro de origem.
**Motivo:** os dois runtimes evoluem em ritmos e protocolos diferentes; uma base
compartilhada obrigaria cada mudança a quebrar ou contornar o outro lado.

### TD-02 — Runtime alvo: Claude Code
Subagentes em `.claude/agents/` com `tools:`, commands em `.claude/commands/`,
hooks no `.claude/settings.json`, MCP em `.mcp.json`.
**Motivo:** no Claude Code a identidade do agente (`agent_type`) e a restrição de
ferramentas por agente são aplicadas pelo runtime, não só por instrução de prompt.

### TD-03 — Go, núcleo só stdlib
Sem dependência externa no `go.mod`; gate `make stdlib-only` no CI.
**Motivo:** binário estático sem `python3`/`jq`/bash no Windows; mesma motivação do
D-16 e D-19 do pipeline-ai.

### TD-04 — Público, MIT, GitHub
`github.com/rodrigodosanjosoliveira/pipeline-ai-claude`. Binário `pipeline-claude`.

### TD-05 — Nada de projeto real no repositório
Fixtures sanitizadas por `tools/fixture-sanitize` e verificadas por
`TestFixturesVersionadas`; lista privada de termos via
`PIPELINE_CLAUDE_FIXTURE_BLOCKLIST`. Testes de paridade contra reports reais leem
um corpus fora do repositório (`PIPELINE_CLAUDE_PARITY_CORPUS`) e são ignorados no
CI. Templates e exemplos usam domínio fictício.

### TD-06 — Instalação fora do `go.mod` do projeto
Projetos instalam com `go install .../cmd/pipeline-claude@<versão>` fixada no
`Makefile` deles, não com a diretiva `tool`.
**Motivo:** a ferramenta de governança não deve entrar no grafo de dependências do
produto, que o `govulncheck` e o `go mod tidy` do projeto verificam (C16).

### TD-07 — Escopo da v1: `api` + PostgreSQL; provedores GitHub e GitLab
`cli` e `desktop` ficam para depois.

## Governança

### TD-08 — Falha fechada em todo o caminho do hook
Payload ilegível, ferramenta de edição sem caminho, configuração ausente ou
inválida: exit 2.
**Diverge de:** pipeline-ai libera com JSON inválido (teste "JSON inválido não
trava a sessão") (C1).

### TD-09 — Resolução de caminho
Caminho relativo resolvido contra a raiz do projeto (não contra o cwd do processo);
symlinks seguidos (`EvalSymlinks` no maior prefixo existente); comparação de
caminhos protegidos sem diferenciar caixa (errar para o lado do bloqueio); campo
`notebook_path` no `NotebookEdit` (C2, P3).

### TD-10 — Modelo de autorização combinado
`protected_paths` editáveis só por `agent_type ∈ editor_agents`; `no_agent_paths`
não editáveis por nenhum agente (aplicação fora da sessão). Sessão principal
nunca edita caminho protegido (C3).

### TD-11 — Diff completo no validador
União de `<base>...HEAD`, working tree, staged e não rastreados, com renomeação
desligada (`--no-renames`). Falha do git reprova, salvo `--no-git` explícito (C9).

### TD-12 — Esquema de report declarado na configuração
Front matter, seções, campos obrigatórios, flag → seção/campos, nível de título,
valores-placeholder. Booleanos estritos (`true`/`false`) (C10, C12).

### TD-13 — Flags derivadas do diff
`auto_flags` (padrão de caminho → flag) generaliza o D-14 do pipeline-ai; o
validador reprova quando o diff toca o padrão e a flag está `false`.

### TD-14 — Configuração própria
`.pipeline-claude.json` (não colide com `.pipeline-ai.json`), `DisallowUnknownFields`,
`version: 1`.

### TD-15 — Arquivos gerenciados × arquivos do projeto
O toolkit só atualiza o que gerou e o projeto não editou, conferido por sha256 em
`.pipeline-claude.lock`. `CLAUDE.md`, `docs-ai/`, permissões e configuração são do
projeto (C17).

### TD-16 — MCP só grava fora de caminho protegido
`sync` não é exposto via MCP (C17).

### TD-17 — Escrita confinada ao destino
`init`, `sync`, `adopt` e `delivery new` validam nomes (`^[a-z][a-z0-9-]*$` para
projeto) e conferem que todo caminho gravado fica dentro do destino após
`filepath.Clean` (C18).

## Fase 2

### TD-18 — `fmt-hook` formata com `go/format`, no `PostToolUse`
Só arquivo `.go` dentro da raiz do projeto e fora de `no_agent_paths`, resolvido
como no hook (TD-09). Formata com `go/format` da stdlib, sem depender de `gofmt`
no PATH; grava só se mudou, por arquivo temporário + rename, preservando a
permissão. Payload ilegível ou evento diferente de `PostToolUse` → exit 2 (TD-08).
Erro de sintaxe → exit 2 com a mensagem do parser: no `PostToolUse` o exit 2 não
desfaz a edição, devolve o erro ao agente para ele corrigir.
**Motivo:** formatação determinística sem gastar turno (D-18 do pipeline-ai) e sem
dependência de ferramenta externa no Windows (TD-03).
**Ressalva:** a formatação segue a versão do Go que compilou o binário; o `doctor`
já confere o Go do projeto.

### TD-19 — `sensors` executa argv, sem shell
Cada sensor roda `cmd` como argv na raiz do projeto, na ordem declarada, com
timeout por sensor (`--timeout`, padrão 10 min) e saída capturada com limite.
Todos rodam, mesmo depois de uma falha, para o relatório ficar completo. Comando
ausente, timeout ou exit ≠ 0 reprovam. Nenhum sensor configurado → exit 1
(nada foi verificado; falha fechada, TD-08).
**Motivo:** sem shell não há quoting nem diferença entre sh e cmd.exe (TD-03).

### TD-20 — `delivery new` gera ID sequencial por prefixo e nunca sobrescreve
ID = `<prefixo>-<NNN>`, com prefixo de `delivery_prefixes` e `NNN` = maior número
do prefixo no índice e em `deliveries_dir`, mais um, com três dígitos no mínimo.
O ID gerado tem de casar `delivery_id_pattern`. Diretório e report criados em modo
exclusivo; índice reescrito por arquivo temporário + rename; se o índice falhar,
o diretório criado é removido. Título de uma linha, sem `|` nem caractere de
controle. Escrita confinada ao destino (TD-17).
**Motivo:** dois agentes ou duas pessoas criando delivery ao mesmo tempo não
podem gerar o mesmo ID em silêncio nem corromper o índice.

### TD-21 — MCP: stdio, ferramentas de leitura e `delivery_new` restrita
JSON-RPC 2.0 por linha no stdin/stdout; stdout só carrega protocolo, log vai
para stderr. Ferramentas: `pipeline_doctor`, `pipeline_validate`,
`pipeline_sensors`, `pipeline_delivery_new`. `pipeline_delivery_new` é recusada
quando `deliveries_dir` ou `index_file` está em `protected_paths` (TD-16).
Configuração ausente ou inválida → toda ferramenta responde `isError`. `sync`
não é exposto (TD-16).

## Fase 3

### TD-22 — Templates embutidos com catálogo explícito
`internal/templates` embute (`embed.FS`) os arquivos gerados e declara cada um
num catálogo: caminho de destino, classe (**gerenciado** ou **do projeto**,
TD-15) e condição (sempre, ou módulo de `enabled_modules`). Renderização com
`text/template`, delimitadores `[[`/`]]` (Markdown e JSON usam `{{` à vontade) e
`missingkey=error`. Arquivo gerenciado depende só da versão do toolkit e de
`enabled_modules`, nunca de nome, módulo Go ou resumo do projeto: o `sync`
consegue re-renderizá-lo só com a config.
Templates portados do pipeline-ai registram a origem no `NOTICE` (uma linha por
grupo de arquivos), **não** em cabeçalho no arquivo, que iria parar no projeto
gerado. O arquivo Go do catálogo leva o cabeçalho normal.
**Motivo:** o catálogo é a fonte única do que é gerenciado; sem ele, `sync` e
`init` divergem sobre o que o toolkit é dono.

### TD-23 — `.pipeline-claude.lock`
JSON com `version: 1`, `toolkit_version`, `files` (caminho → sha256 em hex) e
`released` (caminhos que o projeto assumiu), chaves ordenadas, LF e quebra de
linha final: escrever duas vezes o mesmo lock dá os mesmos bytes. O sha256 é do
conteúdo com CRLF normalizado para LF (checkout com `autocrlf` no Windows não
pode virar "editado"). Leitura estrita (`DisallowUnknownFields`, versão
obrigatória); caminho relativo com `/`, limpo, sem `..`, e dentro da área
gerenciada (`.claude/agents/`, `.claude/skills/`, `.githooks/`). Lock ilegível
ou com caminho fora da área: `sync` e `doctor` falham fechado (TD-08).
**Motivo:** um lock adulterado não pode fazer o `sync` gravar ou apagar fora do
que o toolkit gera.

### TD-24 — `init` gera em diretório novo, por staging
`pipeline-claude init <nome>`: nome `^[a-z][a-z0-9-]*$` (TD-17), só `api` +
PostgreSQL (TD-07), provedor `github|gitlab`. O destino não pode existir, ou tem
de ser um diretório vazio. Tudo é gerado num diretório temporário irmão do
destino e movido no fim (rename do staging, ou de cada entrada para dentro do
destino vazio); qualquer falha remove o staging e deixa o destino como estava.
Nada é sobrescrito. A versão fixada no `Makefile` gerado (TD-06) é a do binário
se for versão de módulo (`vX.Y.Z`, pré-lançamento ou pseudo-versão); build
`dev`/`dirty` exige `--toolkit-version` explícito. `--git` roda `git init -b
main` e `git config core.hooksPath .githooks`; não faz commit.
**Motivo:** `init` pela metade deixa um projeto que parece válido e não é.

### TD-25 — `sync` só toca o que gerou e o projeto não editou
Plano por arquivo gerenciado esperado (catálogo + `enabled_modules`) e por
entrada do lock:

| Situação | Ação |
|---|---|
| no lock, disco = lock, template igual | nada |
| no lock, disco = lock, template mudou | atualiza |
| no lock, disco ≠ lock | **editado**: não toca |
| no lock, ausente no disco | recria (é o jeito de restaurar um editado) |
| fora do lock, ausente no disco | cria |
| fora do lock, existe no disco | **não gerenciado**: não toca |
| em `released` | nada, nunca |
| no lock, fora do esperado (template removido ou módulo desligado), disco = lock | apaga |
| idem, disco ≠ lock | **editado**: não toca, sai do lock só com `--release` |

Grava por temporário + rename, recusa symlink em qualquer componente do caminho
e confere o destino dentro da raiz (TD-17). O lock é reescrito no fim com o que
foi aplicado. `--dry-run` mostra o plano sem gravar; `--release <caminho>` move a
entrada para `released`. Exit 0 sem conflito, 1 com editado ou não gerenciado, 2
com config ou lock ausente/inválido, ou versão do binário ≠ versão do `Makefile`
(templates de outra versão desfariam o pin, TD-06). Não exposto via MCP (TD-16).
**Motivo:** atualizar templates sem atropelar o que o projeto ajustou (C17).

### TD-26 — Camada Claude Code gerada
- **Subagentes** em `.claude/agents/<nome>.md` (gerenciados), `name` igual ao
  nome do arquivo (o hook compara `agent_type` com `editor_agents`, TD-10),
  `tools` explícito em todos. Núcleo: `pm-triage`, `architecture-guardrails`,
  `test-writer`, `dev-implementation`, `code-reviewer`, `security-review`,
  `supply-chain-review`, `qa-validation`, `docs-update`, `git-delivery`.
  Módulos: um por entrada de `config.Modules`.
- **Revisores sem escrita** (resolve o ponto em aberto 2): `code-reviewer`,
  `security-review`, `supply-chain-review`, `architecture-guardrails`,
  `migration-reviewer`, `agent-auditor` e `tenant-isolation` não recebem `Edit`,
  `Write`, `MultiEdit` nem `NotebookEdit`. Ajuste pós-review volta ao
  `dev-implementation`.
- **Fluxo** (resolve o ponto 3): `security-review` uma vez, depois do
  `code-reviewer`, obrigatório quando o diff toca `security_surface_paths` (a
  flag `require_security` já é forçada pelo validador, TD-13).
- **Esquema de report** (resolve o ponto 4): o `config.DefaultReportSchema`; o
  `.pipeline-claude.json` gerado não declara `report_schema`, e o
  `report_template` gerado bate com ele.
- **`tenant-isolation`** (resolve o ponto 5): entra como subagente genérico,
  porque a config já aceita o módulo; desligado por padrão.
- **Pre-push** (resolve o ponto 6): `.githooks/pre-push` gerenciado, bloqueando
  push direto na `main`; ativo com `init --git` ou `git config core.hooksPath
  .githooks`.
- **Skills** de fluxo em `.claude/skills/<nome>/SKILL.md` (gerenciadas; o Claude
  Code unificou commands e skills e recomenda skills): `nova-delivery`,
  `fluxo`, `validar`.
- **Do projeto** (gerados uma vez, nunca tocados pelo `sync`): `CLAUDE.md`,
  `.claude/settings.json`, `.mcp.json`, `.pipeline-claude.json`, `Makefile`,
  `.gitignore`, `go.mod`, código inicial, `docs-ai/`.
- `.claude/settings.json` registra `hook` (PreToolUse,
  `Edit|Write|MultiEdit|NotebookEdit`) e `fmt-hook` (PostToolUse,
  `Edit|Write|MultiEdit`) com `"${CLAUDE_PROJECT_DIR}"/bin/pipeline-claude`, e
  `permissions.deny` de `Edit` em `/.claude/**`, `/.mcp.json`,
  `/.pipeline-claude.json` e `/.pipeline-claude.lock` (âncora `/` = raiz; vale
  também para subagentes). Os mesmos caminhos vão em `no_agent_paths` (TD-10, e portanto também em
  `protected_paths`):
  duas camadas, uma do runtime e uma do hook.
- `.mcp.json` registra `pipeline-claude mcp` com
  `"${CLAUDE_PROJECT_DIR:-.}/bin/pipeline-claude"` (a variável só existe no
  processo do servidor; o padrão `.` cobre o resto).
**Motivo:** a identidade e as ferramentas do agente são aplicadas pelo runtime
(TD-02); o que a sessão principal não pode editar fica negado em duas camadas.

### TD-27 — `doctor` confere os arquivos gerenciados
Checagem `sync`: lock ausente → aviso (projeto anterior ao `init` ou ainda sem
`adopt`); lock inválido → erro; plano do `sync` com editado ou não gerenciado →
aviso listando os caminhos; atualização pendente → aviso com `pipeline-claude
sync`. Nunca grava.

## Depois da Fase 3

### TD-28 — `enabled_modules` sem repetição
A config recusa módulo repetido em `enabled_modules`, com o índice e o nome na
mensagem. O `init` já recusava e os templates não renderizam lista repetida; o
`sync` deixa de tirar a repetição em silêncio e também reprova. Config inválida
faz o hook bloquear toda edição (TD-08): o `doctor` mostra o motivo.
**Motivo:** o mesmo arquivo era válido para a config e inválido para os
templates; cada consumidor novo (o `adopt`, na Fase 4) teria de lembrar de tirar
a repetição. Sem projeto em versão publicada, recusar agora não quebra ninguém.

### TD-29 — Revisores só leitura; diff e checagens pelo MCP
Subagente fora de `editor_agents` não recebe Bash nem ferramenta de escrita, com
uma exceção: `git-delivery` (commit e push). O diff vem da ferramenta MCP
`pipeline_diff`, só leitura: arquivos alterados como no TD-11 (os não
rastreados listados à parte, sem conteúdo), patch `git diff <merge-base>` contra
o `base_ref`, filtro opcional por caminho (pathspec literal, sem mágica) e corte
em 64 KiB com aviso. Checagem que pedia shell vira sensor do projeto (`vuln`:
govulncheck fixado; `mod_verify`: `go mod verify`), rodado por
`pipeline_sensors`: o comando fica na config, que está em `no_agent_paths`.
Projeto existente recebe os subagentes pelo `sync`; os sensores entram na config
à mão, e o `doctor` avisa sensor que um subagente usa e a config não tem.
**Limite conhecido:** a sessão principal, os editores e o `git-delivery` mantêm
Bash, que grava sem passar pelo hook nem pelo `permissions.deny`. O hook protege
contra edição pelas ferramentas de edição, não contra um agente com shell; o que
pega depois é o diff na PR, o `auto_flags` do `validate`, o pre-push e o CI.
**Motivo:** o revisor lê conteúdo não confiável (o diff) e é o alvo natural de
injeção de prompt; a lista de ferramentas é garantida pelo Claude Code, a
instrução "não contorne com o Bash" não. Complementa o TD-21 (ferramentas MCP) e
o TD-26 (ferramentas dos subagentes).

### TD-30 — `flag_front`: flag que exige campo do front matter
`report_schema.flag_front` liga uma flag do front matter (qualquer chave
`^[a-z][a-z0-9_]*$`, não só `require_*`) aos campos do front matter que ela
exige quando é `true`; por exemplo, `{"arquitetura_change": ["aprovacao"]}`. A
flag passa a ter valor estrito (`true`/`false`) e o campo exigido segue as
regras de preenchimento do `front_required` (placeholder e `<...>` reprovam).
Sem `flag_front`, nada muda.
**Motivo:** projeto existente com regra "mudança de arquitetura exige
aprovação registrada" não tinha como expressá-la no esquema; o `adopt` (Fase
4) perderia a regra.

## Fase 4

### TD-31 — `adopt` só cria; arquivo existente nunca é alterado
`pipeline-claude adopt` instala a pipeline num projeto Go existente sem reescrever
nada que já está lá. Cria o que falta da camada gerada (subagentes, skills,
pre-push, `.mcp.json`, `.claude/settings.json`, `.pipeline-claude.json`) e o
lock. Para arquivo que já existe e precisaria de conteúdo do toolkit
(`.claude/settings.json` com outros hooks, `Makefile`, `CLAUDE.md`,
`.gitignore`), imprime o trecho exato a acrescentar; o `doctor` confere se
entrou. Código, `go.mod`, `docs-ai/` e `.claude/commands/` nunca são tocados.
Cada arquivo é criado com `O_EXCL`, conferido dentro da raiz (TD-17), sem
seguir symlink; falha no meio remove o que esta execução criou.
**Motivo:** mesclar automaticamente JSON e Markdown de outro autor reordena ou
perde conteúdo; a instrução impressa é revisável no diff da adoção.

### TD-32 — Lock da adoção: o que já existe é do projeto
Arquivo da área gerenciada (TD-23) criado pelo `adopt` entra em `files`.
Arquivo que já existia entra em `files` se for igual ao template (hash, TD-23);
diferente, entra em `released` (do projeto: o `sync` nunca o toca, TD-25).
Arquivo da área gerenciada fora do catálogo fica fora do lock. O `sync`
seguinte parte desse lock como num projeto criado pelo `init`.
**Motivo:** subagentes customizados carregam conhecimento do projeto (regras
de domínio, build tags, aprovações) que o template genérico não tem; trocar
por padrão seria regressão.

### TD-33 — Pré-condições da adoção
O `adopt` exige: raiz com `go.mod` (só `api` Go + PostgreSQL, TD-07), working
tree do git limpo (`git status --porcelain` vazio, para a adoção ser um diff
revisável), nenhum `.pipeline-claude.lock` (projeto já adotado usa `sync`) e
versão do toolkit fixável (como no `init`, TD-24). `.pipeline-claude.json`
existente precisa passar no `config.Load`; ausente, é criado a partir do
template com o provedor do remote `origin`, o módulo do `go.mod` e
`enabled_modules` com os módulos cujos subagentes já existem. `--dry-run`
mostra o plano sem gravar. Exit 0 ok, 1 falha de escrita, 2 pré-condição ou
uso inválido. Não exposto via MCP (TD-16).
**Motivo:** adoção pela metade ou sobre trabalho não commitado mistura a
mudança do toolkit com a do projeto.

### TD-34 — `doctor` confere os subagentes, gerenciados ou do projeto
Checagem `subagentes`: cada `.claude/agents/*.md` com front matter legível,
`name` igual ao arquivo e `tools` explícito; fora de `editor_agents`, nenhum com
ferramenta de escrita nem Bash, exceto `git-delivery` (TD-29). Violação é
aviso com o nome e a ferramenta; front matter ilegível é erro. Vale também para
subagente em `released`.
**Motivo:** depois do `adopt`, os subagentes do projeto ficam fora do `sync`; a
regra do TD-29 precisa de uma checagem que não dependa do template.

## Depois da Fase 4

### TD-35 — `report_writers`: revisor que escreve o report fica em `deliveries_dir`
`report_writers` na config lista subagentes fora de `editor_agents` que
escrevem a própria seção do report. O hook os limita a `deliveries_dir`:
qualquer edição deles fora dali é bloqueada, mesmo em caminho não protegido;
`protected_paths` e `no_agent_paths` continuam valendo antes. O `doctor` aceita
ferramenta de escrita nesses subagentes, mas não Bash, que não passa pelo hook.
Exige `deliveries_dir` fora de `protected_paths` e nome fora de
`editor_agents`. Sem `report_writers`, nada muda.
**Motivo:** projeto adotado em que cada revisor preenche sua seção do report
(TD-34 avisava para sempre); o aviso vira regra aplicada pelo hook, mais
restrita que antes (o revisor deixa de poder editar qualquer arquivo não
protegido).

## Pontos em aberto

1. Distribuição da camada Claude também como plugin do Claude Code.

Os pontos 2 a 6 (revisor, ordem do fluxo, esquema de report padrão,
`tenant-isolation` e pre-push) foram resolvidos no TD-26.
