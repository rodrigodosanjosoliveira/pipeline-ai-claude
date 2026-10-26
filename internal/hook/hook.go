// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/hook/hook.go @ 0eac73a (MIT).
// Alterações: só o protocolo do Claude Code, sem saída JSON do Antigravity (TD-02);
// falha fechada em payload ilegível, evento errado, caminho ausente e config
// ausente (TD-08); notebook_path no NotebookEdit, relativo resolvido contra a raiz
// e symlinks seguidos (TD-09); no_agent_paths (TD-10); chaves do payload lidas
// sem casamento por caixa. Leitura do payload e resolução de caminho em
// internal/hookio, compartilhadas com o fmt-hook.

// Package hook implementa o hook PreToolUse do Claude Code que impede edição de
// caminho protegido fora dos agentes editores.
//
// O Claude Code envia o payload em stdin. Exit 0 libera a ferramenta; exit 2
// bloqueia e mostra o stderr ao modelo. Toda dúvida bloqueia (TD-08).
package hook

import (
	"fmt"
	"io"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/hookio"
)

// Códigos de saída do hook.
const (
	ExitAllow = 0
	ExitBlock = 2
)

// preToolUse é o único evento em que o hook decide.
const preToolUse = "PreToolUse"

// Run lê o payload do Claude Code em stdin e devolve o código de saída. getwd
// fornece o diretório inicial quando o payload não traz cwd.
func Run(stdin io.Reader, stderr io.Writer, getwd func() (string, error)) int {
	block := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "pipeline-claude hook: "+format+"\n", args...)
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
	case present && event != preToolUse:
		return block("hook registrado no evento %q; ele só decide em %s. Rode `pipeline-claude doctor` para corrigir o .claude/settings.json.", event, preToolUse)
	}

	tool, present, err := payload.String("tool_name")
	if err != nil || !present {
		return block("tool_name ausente ou ilegível")
	}
	agent, _, err := payload.String("agent_type")
	if err != nil {
		return block("agent_type ilegível")
	}
	cwd, _, err := payload.String("cwd")
	if err != nil {
		return block("cwd ilegível")
	}

	key, isEdit := hookio.PathKey(tool)
	if !isEdit {
		return ExitAllow
	}

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
		return ExitAllow // outro repositório
	}
	rel := resolved.Rel

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
