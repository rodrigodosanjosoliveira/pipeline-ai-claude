// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/hook/hook.go @ 0eac73a (MIT).
// Alterações: só o protocolo do Claude Code, sem saída JSON do Antigravity (TD-02);
// falha fechada em payload ilegível, evento errado, caminho ausente e config
// ausente (TD-08); notebook_path no NotebookEdit, relativo resolvido contra a raiz
// e symlinks seguidos (TD-09); no_agent_paths (TD-10); chaves do payload lidas
// sem casamento por caixa.

// Package hook implementa o hook PreToolUse do Claude Code que impede edição de
// caminho protegido fora dos agentes editores.
//
// O Claude Code envia o payload em stdin. Exit 0 libera a ferramenta; exit 2
// bloqueia e mostra o stderr ao modelo. Toda dúvida bloqueia (TD-08).
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// Códigos de saída do hook.
const (
	ExitAllow = 0
	ExitBlock = 2
)

// preToolUse é o único evento em que o hook decide.
const preToolUse = "PreToolUse"

// pathKey é, para cada ferramenta de edição, o campo de tool_input com o
// caminho alvo.
var pathKey = map[string]string{
	"Edit":         "file_path",
	"Write":        "file_path",
	"MultiEdit":    "file_path",
	"NotebookEdit": "notebook_path",
}

// errAbsent indica campo ausente no payload.
var errAbsent = errors.New("campo ausente")

// Run lê o payload do Claude Code em stdin e devolve o código de saída. getwd
// fornece o diretório inicial quando o payload não traz cwd.
func Run(stdin io.Reader, stderr io.Writer, getwd func() (string, error)) int {
	block := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "pipeline-claude hook: "+format+"\n", args...)
		return ExitBlock
	}

	raw, err := io.ReadAll(stdin)
	if err != nil {
		return block("falha ao ler stdin: %v", err)
	}
	// Map, não struct: encoding/json casaria chaves de struct sem diferenciar
	// caixa, e "Tool_Name" poderia mascarar "tool_name".
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return block("payload não é um objeto JSON válido")
	}

	event, err := stringField(payload, "hook_event_name")
	switch {
	case errors.Is(err, errAbsent):
	case err != nil:
		return block("hook_event_name ilegível")
	case event != preToolUse:
		return block("hook registrado no evento %q; ele só decide em %s. Rode `pipeline-claude doctor` para corrigir o .claude/settings.json.", event, preToolUse)
	}

	tool, err := stringField(payload, "tool_name")
	if err != nil {
		return block("tool_name ausente ou ilegível")
	}
	agent, err := stringField(payload, "agent_type")
	if err != nil && !errors.Is(err, errAbsent) {
		return block("agent_type ilegível")
	}
	cwd, err := stringField(payload, "cwd")
	if err != nil && !errors.Is(err, errAbsent) {
		return block("cwd ilegível")
	}

	key, isEdit := pathKey[tool]
	if !isEdit {
		return ExitAllow
	}

	var input map[string]json.RawMessage
	if raw, ok := payload["tool_input"]; !ok || json.Unmarshal(raw, &input) != nil || input == nil {
		return block("%s sem tool_input legível", tool)
	}
	target, err := stringField(input, key)
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

	rel, inside, err := relToRoot(cfg.Root(), target)
	if err != nil {
		return block("não foi possível resolver o caminho alvo: %v", err)
	}
	if !inside {
		return ExitAllow // outro repositório
	}

	switch {
	case cfg.IsNoAgent(rel):
		fmt.Fprint(stderr, blockMessage(rel, agent, "caminho sem edição por agentes", cfg))
		return ExitBlock
	case cfg.IsProtected(rel) && !cfg.IsEditorAgent(agent):
		fmt.Fprint(stderr, blockMessage(rel, agent, "agente sem permissão", cfg))
		return ExitBlock
	}
	return ExitAllow
}

// stringField lê m[key] como string. Devolve errAbsent se a chave não existir
// e erro se o valor não for string.
func stringField(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", errAbsent
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return s, nil
}

// relToRoot resolve target (relativo à raiz, se não for absoluto) seguindo
// symlinks e devolve o caminho relativo à raiz com "/". inside=false se o alvo
// resolvido ficar fora da raiz (TD-09).
func relToRoot(root, target string) (rel string, inside bool, err error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false, fmt.Errorf("raiz %s: %w", root, err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", false, fmt.Errorf("raiz %s: %w", root, err)
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootAbs, abs)
	}
	real, err := resolveExisting(filepath.Clean(abs))
	if err != nil {
		return "", false, err
	}
	rel, ok := within(rootReal, real)
	if !ok {
		return "", false, nil
	}
	return filepath.ToSlash(rel), true, nil
}

// resolveExisting aplica EvalSymlinks ao maior prefixo existente de abs e
// devolve esse prefixo resolvido seguido do restante. O arquivo pode ainda não
// existir. Um link quebrado no caminho é erro: a ferramenta o seguiria para um
// destino que não dá para conferir.
func resolveExisting(abs string) (string, error) {
	var rest []string
	p := abs
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			parts := append([]string{real}, rest...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		if _, lerr := os.Lstat(p); lerr == nil {
			return "", fmt.Errorf("%s: link simbólico quebrado", p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("%s: nenhum prefixo existente", abs)
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

// within devolve path relativo a root se estiver dentro dele. O prefixo da raiz
// é comparado sem diferenciar caixa, errando para o lado de "dentro" (e,
// portanto, do bloqueio) em filesystem case-insensitive (TD-09).
func within(root, path string) (string, bool) {
	if strings.EqualFold(path, root) {
		return ".", true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(path) < len(prefix) || !strings.EqualFold(path[:len(prefix)], prefix) {
		return "", false
	}
	return path[len(prefix):], true
}

func blockMessage(rel, agent, reason string, cfg *config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "BLOQUEADO pelo pipeline-claude: %s.\n\n", reason)
	fmt.Fprintf(&b, "  arquivo: %q\n", rel)
	if agent == "" {
		b.WriteString("  origem:  sessão principal\n")
	} else {
		fmt.Fprintf(&b, "  origem:  agente %q\n", agent)
	}

	b.WriteString("\nEditores autorizados (editor_agents):\n")
	if len(cfg.EditorAgents) == 0 {
		b.WriteString("  (nenhum configurado)\n")
	}
	for _, a := range cfg.EditorAgents {
		fmt.Fprintf(&b, "  - %s\n", a)
	}

	if cfg.IsNoAgent(rel) {
		b.WriteString("\nPróximo passo: este caminho está em no_agent_paths e não é editado por nenhum agente;\n")
		b.WriteString("a alteração é feita por uma pessoa, fora da sessão.\n")
	} else {
		b.WriteString("\nPróximo passo: delegue a alteração a um dos editores autorizados.\n")
		b.WriteString("Para mudar o escopo protegido, edite protected_paths em " + config.FileName + ".\n")
	}
	return b.String()
}
