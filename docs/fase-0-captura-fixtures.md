# Fase 0 — captura de fixtures de hook do Claude Code

Objetivo: obter payloads **reais** de `PreToolUse` e `PostToolUse` da versão do
Claude Code em uso, para que o hook do toolkit seja testado contra o formato que
o runtime realmente envia (em especial o campo `agent_type` dentro de
subagentes), e não contra JSON escrito à mão.

Regra: a captura acontece numa **worktree descartável** de um projeto que já usa
subagentes no Claude Code. Nada da worktree é commitado; os payloads brutos ficam
em `captures/` (ignorado pelo git) e só entram no repositório depois de passar
pelo `tools/fixture-sanitize`.

## 1. Preparar

```sh
cd ~/workspace/<projeto-com-subagentes>
git worktree add ../captura-hooks HEAD
cd ../captura-hooks
claude --version          # anote: vira o nome do diretório das fixtures
mkdir -p internal/exemplo docs-ai
printf 'package exemplo\n' > internal/exemplo/alvo.go
```

Use **só** arquivos com nome fictício (`internal/exemplo/alvo.go`,
`docs-ai/exemplo.md`, `docs-ai/exemplo.ipynb`), para que nenhum nome do projeto
apareça nos caminhos capturados.

## 2. Registrar o hook de captura

Crie `.claude/settings.local.json` **na worktree** (os hooks deste arquivo se somam
aos do `settings.json` do projeto):

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Edit|Write|MultiEdit|NotebookEdit",
        "hooks": [{ "type": "command", "command": "bash ~/workspace/pipeline-ai-claude/scripts/capture-hook.sh" }] }
    ],
    "PostToolUse": [
      { "matcher": "Edit|Write|MultiEdit|NotebookEdit",
        "hooks": [{ "type": "command", "command": "bash ~/workspace/pipeline-ai-claude/scripts/capture-hook.sh" }] }
    ]
  }
}
```

O script nunca bloqueia (sai sempre com 0). Se o projeto já tiver um hook de
bloqueio, ele continua valendo; o payload é gravado mesmo quando a edição é
bloqueada.

## 3. Cenários (um por vez, na mesma sessão do `claude` aberta na worktree)

| # | Origem | Ação |
|---|---|---|
| 1 | sessão principal | `Edit` em `internal/exemplo/alvo.go` |
| 2 | sessão principal | `Write` de `docs-ai/exemplo.md` |
| 3 | sessão principal | `MultiEdit` em `internal/exemplo/alvo.go` |
| 4 | sessão principal | `NotebookEdit` em `docs-ai/exemplo.ipynb` (crie um notebook mínimo antes) |
| 5 | subagente **editor** (ex.: `dev-implementation`) | `Edit` em `internal/exemplo/alvo.go` |
| 6 | subagente **editor** | `Write` de `internal/exemplo/novo.go` |
| 7 | subagente **não editor** com `Write` (ex.: `pm-triage`) | `Write` de `docs-ai/exemplo.md` |
| 8 | sessão principal | `Edit` com caminho **relativo**, se o modelo aceitar (`internal/exemplo/alvo.go`) |

Peça explicitamente cada ação ao Claude Code (ex.: "use o subagente
dev-implementation para trocar `package exemplo` por `package exemplo // x` em
internal/exemplo/alvo.go").

## 4. Sanitizar e versionar

```sh
cd ~/workspace/pipeline-ai-claude
go run ./tools/fixture-sanitize \
  -root "$(cd ../captura-hooks && pwd -P)" \
  -in captures \
  -out testdata/hooks/claude/<versão-do-claude-code>
make test                                              # regras estruturais
make fixtures-check BLOCKLIST=~/<lista-privada>.txt    # termos do projeto real
```

A lista privada tem um termo por linha (nome do projeto, módulos, empresa,
usuário) e **nunca** entra no repositório.

Confira à mão pelo menos um arquivo de cada cenário e registre na PR:

- se `agent_type` aparece nos cenários 5–7 e com qual valor;
- se o cenário 8 chegou com caminho relativo ou absoluto;
- qual campo de caminho o `NotebookEdit` usa (`file_path` ou `notebook_path`).

## 5. Limpar

```sh
rm -rf captures/*
cd ~/workspace/<projeto-com-subagentes>
git worktree remove --force ../captura-hooks
git status --short    # nada novo no projeto
```
