#!/usr/bin/env bash
# capture-hook.sh — grava o payload bruto de um hook do Claude Code para virar
# fixture (Fase 0). Nunca bloqueia: sai sempre com 0, para não alterar o
# comportamento da sessão em que a captura acontece.
#
# Uso (no .claude/settings.local.json de uma worktree descartável):
#   "command": "bash ~/workspace/pipeline-ai-claude/scripts/capture-hook.sh"
#
# Destino: $PIPELINE_CLAUDE_CAPTURE_DIR (default: ~/workspace/pipeline-ai-claude/captures),
# que está no .gitignore do toolkit. Os arquivos brutos contêm caminhos e
# conteúdo reais: só saem dali pelo tools/fixture-sanitize.
set -u
dir="${PIPELINE_CLAUDE_CAPTURE_DIR:-$HOME/workspace/pipeline-ai-claude/captures}"
umask 077
mkdir -p "$dir" 2>/dev/null || exit 0
ts="$(date +%s%N 2>/dev/null || date +%s)"
cat > "$dir/$ts-$$.json" 2>/dev/null || true
exit 0
