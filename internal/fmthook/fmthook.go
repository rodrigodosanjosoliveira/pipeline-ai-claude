// Package fmthook implementa o hook PostToolUse do Claude Code que formata o
// arquivo Go editado pelo agente (TD-18).
//
// Formata com go/format, sem depender de gofmt no PATH. Payload ilegível,
// evento errado ou configuração ausente devolvem exit 2 (TD-08). Erro de
// sintaxe também devolve 2: no PostToolUse isso não desfaz a edição, só leva o
// erro ao agente para ele corrigir.
package fmthook

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"go/scanner"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/hookio"
)

// Códigos de saída do hook.
const (
	ExitOK    = 0
	ExitBlock = 2
)

// postToolUse é o único evento em que o hook age.
const postToolUse = "PostToolUse"

// goTools são as ferramentas cuja edição pode produzir Go. NotebookEdit fica
// de fora: notebook não é Go.
var goTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true}

// Run lê o payload do Claude Code em stdin e devolve o código de saída. getwd
// fornece o diretório inicial quando o payload não traz cwd.
func Run(stdin io.Reader, stderr io.Writer, getwd func() (string, error)) int {
	block := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "pipeline-claude fmt-hook: "+format+"\n", args...)
		return ExitBlock
	}

	payload, err := hookio.ReadPayload(stdin)
	if err != nil {
		return block("%v", err)
	}

	event, present, err := payload.String("hook_event_name")
	switch {
	case err != nil:
		return block("hook_event_name ilegível")
	case present && event != postToolUse:
		return block("hook registrado no evento %q; ele só age em %s. Rode `pipeline-claude doctor` para corrigir o .claude/settings.json.", event, postToolUse)
	}

	tool, present, err := payload.String("tool_name")
	if err != nil || !present {
		return block("tool_name ausente ou ilegível")
	}
	cwd, _, err := payload.String("cwd")
	if err != nil {
		return block("cwd ilegível")
	}
	if !goTools[tool] {
		return ExitOK
	}
	key, _ := hookio.PathKey(tool)

	input, err := payload.Object("tool_input")
	if err != nil {
		return block("%s sem tool_input legível", tool)
	}
	target, _, err := input.String(key)
	if err != nil || target == "" {
		return block("%s sem tool_input.%s", tool, key)
	}

	if cwd == "" {
		if cwd, err = getwd(); err != nil {
			return block("sem cwd no payload e falha ao obter o diretório atual: %v", err)
		}
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return block("configuração ausente ou inválida: %v\nRode `pipeline-claude doctor` para diagnosticar.", err)
	}

	resolved, err := hookio.ResolveInRoot(cfg.Root(), target)
	if err != nil {
		return block("não foi possível resolver o caminho alvo: %v", err)
	}
	if !resolved.Inside {
		return ExitOK // outro repositório
	}
	rel := resolved.Rel
	if !strings.EqualFold(filepath.Ext(rel), ".go") || cfg.IsNoAgent(rel) {
		return ExitOK
	}

	info, err := os.Stat(resolved.Real)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ExitOK // a edição pode ter falhado
	case err != nil:
		return block("falha ao ler %s: %v", rel, err)
	case !info.Mode().IsRegular():
		return block("%s não é arquivo regular", rel)
	}

	src, err := os.ReadFile(resolved.Real)
	if err != nil {
		return block("falha ao ler %s: %v", rel, err)
	}
	out, err := format.Source(src)
	if err != nil {
		return block("%s", syntaxError(rel, err))
	}
	if bytes.Equal(src, out) {
		return ExitOK
	}
	if err := replace(resolved.Real, out, info.Mode().Perm()); err != nil {
		return block("falha ao gravar %s: %v", rel, err)
	}
	fmt.Fprintf(stderr, "pipeline-claude fmt-hook: formatado %s\n", rel)
	return ExitOK
}

// syntaxError formata o erro do parser como arquivo:linha:coluna: msg.
func syntaxError(rel string, err error) string {
	var list scanner.ErrorList
	if !errors.As(err, &list) || len(list) == 0 {
		return fmt.Sprintf("%s: %v", rel, err)
	}
	lines := make([]string, len(list))
	for i, e := range list {
		lines[i] = fmt.Sprintf("%s:%d:%d: %s", rel, e.Pos.Line, e.Pos.Column, e.Msg)
	}
	return strings.Join(lines, "\n")
}

// replace grava data em path por arquivo temporário no mesmo diretório e
// rename, com a permissão perm. path já foi conferido dentro da raiz.
func replace(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pipeline-claude-fmt-*")
	if err != nil {
		return fmt.Errorf("criar temporário: %w", err)
	}
	name := tmp.Name()
	fail := func(step string, err error) error {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("%s: %w", step, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("gravar temporário", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail("ajustar permissão", err)
	}
	if err := tmp.Close(); err != nil {
		return fail("fechar temporário", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fail("substituir arquivo", err)
	}
	return nil
}
