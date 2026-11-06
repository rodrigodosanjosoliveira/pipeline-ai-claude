// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/hook/hook.go @ 0eac73a (MIT).
// Alterações: só o protocolo do Claude Code, sem saída JSON do Antigravity (TD-02);
// falha fechada em payload ilegível, evento errado, caminho ausente e config
// ausente (TD-08); notebook_path no NotebookEdit, relativo resolvido contra a raiz
// e symlinks seguidos (TD-09); no_agent_paths (TD-10); chaves do payload lidas
// sem casamento por caixa; report_writers só escrevem em deliveries_dir (TD-35);
// registro opcional das decisões em hook_log, com o próprio log protegido
// (TD-45).
// Leitura do payload e resolução de caminho em internal/hookio, compartilhadas
// com o fmt-hook.

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
	"os"
	"path/filepath"
	"strings"
	"time"

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

	// decide registra a decisão no hook_log, se houver, e devolve o código.
	// Falha ao registrar bloqueia (TD-45, regra 3).
	decide := func(rel, rule, msg string) int {
		decision, code := "allow", ExitAllow
		if msg != "" {
			decision, code = "deny", ExitBlock
		}
		if err := appendLog(cfg, logEntry{Tool: tool, Path: rel, Agent: agent, Decision: decision, Rule: rule}); err != nil {
			fmt.Fprintf(stderr, "BLOQUEADO pelo pipeline-claude: falha ao gravar hook_log (%s): %v\n", cfg.HookLog, err)
			fmt.Fprint(stderr, "Toda decisão do hook é registrada; sem o registro, nada é liberado. Rode `pipeline-claude doctor`.\n")
			if msg != "" {
				fmt.Fprint(stderr, "\n"+msg)
			}
			return ExitBlock
		}
		fmt.Fprint(stderr, msg)
		return code
	}

	resolved, err := hookio.ResolveInRoot(cfg.Root(), target)
	if err != nil {
		return decide("", "unresolved_path", fmt.Sprintf("pipeline-claude hook: não foi possível resolver o caminho alvo: %v\n", err))
	}
	if !resolved.Inside {
		return decide("", "outside_root", "") // outro repositório
	}
	rel := resolved.Rel

	switch {
	case cfg.IsHookLog(rel):
		return decide(rel, "hook_log", blockMessage(rel, agent, "arquivo de registro do hook (hook_log)", cfg))
	case cfg.IsNoAgent(rel):
		return decide(rel, "no_agent_paths", blockMessage(rel, agent, "caminho sem edição por agentes", cfg))
	case cfg.IsProtected(rel) && !cfg.IsEditorAgent(agent):
		return decide(rel, "protected_paths", blockMessage(rel, agent, "agente sem permissão", cfg))
	case cfg.IsReportWriter(agent) && !cfg.InDeliveries(rel):
		return decide(rel, "report_writers", blockMessage(rel, agent, "agente de report fora de deliveries_dir", cfg))
	case cfg.IsProtected(rel):
		return decide(rel, "editor_agents", "")
	}
	return decide(rel, "unprotected", "")
}

// logEntry é uma linha do hook_log (TD-45). Nunca leva conteúdo da edição.
type logEntry struct {
	Time     string `json:"time"`
	Tool     string `json:"tool"`
	Path     string `json:"path"`
	Agent    string `json:"agent"`
	Decision string `json:"decision"`
	Rule     string `json:"rule"`
}

// appendLog acrescenta e ao hook_log de cfg numa escrita só, em modo append.
// Sem hook_log, não faz nada. O arquivo, com symlinks resolvidos, precisa
// ficar dentro da raiz; a pasta é criada se faltar.
func appendLog(cfg *config.Config, e logEntry) error {
	if cfg.HookLog == "" {
		return nil
	}
	e.Time = time.Now().UTC().Format(time.RFC3339)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	resolve := func() (string, error) {
		r, err := hookio.ResolveInRoot(cfg.Root(), cfg.HookLog)
		if err != nil {
			return "", err
		}
		if !r.Inside {
			return "", errors.New("o caminho resolve para fora da raiz do projeto")
		}
		return r.Real, nil
	}
	p, err := resolve()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// De novo depois do MkdirAll: a pasta pode ter virado symlink.
	if p, err = resolve(); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
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

	switch {
	case cfg.IsHookLog(rel):
		b.WriteString("\nPróximo passo: o hook_log é gravado só pelo hook e não é editado por nenhuma ferramenta;\n")
		b.WriteString("para desligar o registro, uma pessoa remove hook_log de " + config.FileName + ".\n")
	case cfg.IsNoAgent(rel):
		b.WriteString("\nPróximo passo: este caminho está em no_agent_paths e não é editado por nenhum agente;\n")
		b.WriteString("a alteração é feita por uma pessoa, fora da sessão.\n")
	case !cfg.IsProtected(rel) && cfg.IsReportWriter(agent):
		fmt.Fprintf(&b, "\nPróximo passo: agente de report_writers só escreve em deliveries_dir (%s/);\n", cfg.DeliveriesDir)
		b.WriteString("registre o achado no report e devolva a alteração a um dos editores autorizados.\n")
	default:
		b.WriteString("\nPróximo passo: delegue a alteração a um dos editores autorizados.\n")
		b.WriteString("Para mudar o escopo protegido, edite protected_paths em " + config.FileName + ".\n")
	}
	return b.String()
}
