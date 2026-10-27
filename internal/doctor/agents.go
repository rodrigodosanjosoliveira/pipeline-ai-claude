package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// agentsRel é o diretório dos subagentes do projeto, relativo à raiz.
const agentsRel = ".claude/agents"

// restrictedTools são as ferramentas que, fora de editor_agents, nenhum
// subagente recebe (TD-29), em ordem alfabética.
var restrictedTools = []string{"Bash", "Edit", "MultiEdit", "NotebookEdit", "Write"}

// shellAgent é o único subagente fora de editor_agents com Bash: faz commit e
// push (TD-29).
const shellAgent = "git-delivery"

// agentMeta é o que a checagem subagentes lê do front matter de um subagente.
// HasTools diz se a chave tools existe; Tools vazio com ela presente é tools
// vazio, que no Claude Code herda tudo como a chave omitida.
type agentMeta struct {
	Name     string
	Tools    []string
	HasTools bool
}

// parseAgentFrontMatter lê o front matter de um subagente: bloco entre linhas
// "---" no início do arquivo, chaves "chave: valor" na coluna 0. tools aceita
// string separada por vírgula, lista em linha ([a, b]) ou lista YAML em bloco
// (tools: seguido de linhas "- Nome"); outras chaves são ignoradas, inclusive
// as linhas recuadas abaixo delas. Fim de linha CRLF é aceito. Qualquer outra
// forma de tools é erro: na dúvida, a checagem não passa (TD-08).
func parseAgentFrontMatter(data []byte) (agentMeta, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if lines[0] != "---" {
		return agentMeta{}, errors.New("sem front matter (o arquivo precisa começar com uma linha ---)")
	}
	end := slices.Index(lines[1:], "---")
	if end < 0 {
		return agentMeta{}, errors.New("front matter sem fechamento (linha ---)")
	}

	var m agentMeta
	seen := map[string]bool{}
	key := ""         // chave da coluna 0 em vigor
	toolList := false // tools: sem valor, à espera de itens "- Nome"
	for _, l := range lines[1 : end+1] {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		indented := l[0] == ' ' || l[0] == '\t'
		isItem := t == "-" || strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "-\t")
		switch {
		case key == "tools" && (indented || isItem):
			if !isItem || !toolList {
				return agentMeta{}, fmt.Errorf("tools fora dos formatos aceitos (vírgulas, [a, b] ou lista - Nome): %q", l)
			}
			if tool := unquote(t[1:]); tool != "" {
				m.Tools = append(m.Tools, tool)
			}
			continue
		case key != "" && (indented || isItem):
			continue // continuação de outra chave
		}

		k, v, found := strings.Cut(l, ":")
		k = strings.TrimSpace(k)
		if !found || indented || k == "" {
			return agentMeta{}, fmt.Errorf("linha fora do formato chave: valor: %q", l)
		}
		if seen[k] && (k == "name" || k == "tools") {
			return agentMeta{}, fmt.Errorf("chave %q repetida", k)
		}
		seen[k] = true
		key, toolList = k, false
		v = stripComment(strings.TrimSpace(v))
		switch k {
		case "name":
			m.Name = unquote(v)
		case "tools":
			m.HasTools = true
			if v == "" {
				toolList = true
				continue
			}
			if strings.HasPrefix(v, "[") {
				if !strings.HasSuffix(v, "]") {
					return agentMeta{}, fmt.Errorf("tools com lista em linha sem fechamento: %q", l)
				}
				v = v[1 : len(v)-1]
			}
			for _, x := range strings.Split(v, ",") {
				if tool := unquote(x); tool != "" {
					m.Tools = append(m.Tools, tool)
				}
			}
		}
	}
	if m.Name == "" {
		return agentMeta{}, errors.New("front matter sem name")
	}
	if strings.ContainsFunc(m.Name, unicode.IsControl) {
		return agentMeta{}, fmt.Errorf("name com caractere de controle: %q", m.Name)
	}
	return m, nil
}

// unquote tira espaços e um par de aspas (simples ou duplas) em volta de s.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// stripComment tira um comentário YAML (" #...") de um valor sem aspas.
func stripComment(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") {
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

// restrictedTool devolve o nome da ferramenta de tool (sem o padrão entre
// parênteses, como em "Bash(git diff:*)") se ela for de escrita ou Bash. O
// nome é comparado sem diferenciar caixa: na dúvida, avisa.
func restrictedTool(tool string) (string, bool) {
	base, _, _ := strings.Cut(tool, "(")
	base = strings.TrimSpace(base)
	for _, r := range restrictedTools {
		if strings.EqualFold(base, r) {
			return base, true
		}
	}
	return "", false
}

// agentsCheck confere cada .claude/agents/*.md, gerenciado, liberado ou fora
// do lock (TD-34): front matter legível, name igual ao nome do arquivo, tools
// explícito e, fora de editor_agents, nenhuma ferramenta de escrita nem Bash,
// exceto git-delivery com Bash (TD-29). Front matter ilegível, name diferente
// ou symlink (não seguido: o runtime seguiria e o subagente ficaria sem
// checagem) é erro; violação de ferramenta ou tools ausente é aviso. Config
// ausente ou inválida é aviso "não verificado".
func agentsCheck(root string, cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return bad(checkAgents, StatusWarn, "não verificado: configuração ausente ou inválida", "corrija a configuração (ver checagem configuração)")
	}
	const syncAction = "rode `pipeline-claude sync` para recriar os subagentes gerenciados"
	for _, rel := range []string{".claude", agentsRel} {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return bad(checkAgents, StatusWarn, agentsRel+" não encontrado; nenhum subagente verificado", syncAction)
		case err != nil:
			return bad(checkAgents, StatusError, fmt.Sprintf("falha ao ler %s: %v", rel, err), "confira as permissões de "+rel)
		case info.Mode()&fs.ModeSymlink != 0:
			return bad(checkAgents, StatusError, rel+" é symlink; o doctor não o segue", "troque o symlink "+rel+" por um diretório real dentro do projeto")
		case !info.IsDir():
			return bad(checkAgents, StatusError, rel+" não é diretório", "troque "+rel+" por um diretório com os subagentes ("+agentsRel+"/<nome>.md)")
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(agentsRel)))
	if err != nil {
		return bad(checkAgents, StatusError, fmt.Sprintf("falha ao ler %s: %v", agentsRel, err), "confira as permissões de "+agentsRel)
	}

	var errs, warns []string
	n := 0
	for _, e := range entries {
		file := e.Name()
		if !strings.HasSuffix(file, ".md") {
			continue
		}
		rel := agentsRel + "/" + file
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			errs = append(errs, rel+": symlink, não seguido")
			continue
		case !e.Type().IsRegular():
			errs = append(errs, rel+": não é arquivo regular")
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: falha ao ler: %v", rel, err))
			continue
		}
		meta, err := parseAgentFrontMatter(data)
		if err != nil {
			errs = append(errs, rel+": "+err.Error())
			continue
		}
		name := strings.TrimSuffix(file, ".md")
		if meta.Name != name {
			errs = append(errs, fmt.Sprintf("%s: name %q diferente do nome do arquivo", rel, meta.Name))
			continue
		}
		n++
		if len(meta.Tools) == 0 {
			warns = append(warns, name+": tools ausente (herda todas as ferramentas)")
			continue
		}
		if cfg.IsEditorAgent(name) {
			continue
		}
		var found []string
		for _, tool := range meta.Tools {
			base, restricted := restrictedTool(tool)
			if restricted && !(name == shellAgent && strings.EqualFold(base, "Bash")) {
				found = append(found, base)
			}
		}
		slices.Sort(found)
		for _, tool := range slices.Compact(found) {
			warns = append(warns, name+": "+tool)
		}
	}

	warnMsg := "subagente(s) fora da regra de ferramentas (TD-29): " + strings.Join(warns, ", ")
	switch {
	case len(errs) > 0:
		msg := "subagente(s) com front matter ilegível ou name errado: " + strings.Join(errs, "; ")
		if len(warns) > 0 {
			msg += "; " + warnMsg
		}
		return bad(checkAgents, StatusError, msg,
			"corrija o front matter de cada arquivo (bloco --- no início e no fim, name igual ao nome do arquivo) e troque symlink por arquivo real")
	case len(warns) > 0:
		return bad(checkAgents, StatusWarn, warnMsg,
			"declare tools explícito; fora de editor_agents, tire Edit, Write, MultiEdit, NotebookEdit e Bash (só o "+shellAgent+" mantém Bash) ou inclua o subagente em editor_agents no "+config.FileName)
	case n == 0:
		return bad(checkAgents, StatusWarn, "nenhum subagente em "+agentsRel, syncAction)
	}
	return ok(checkAgents, fmt.Sprintf("%d subagente(s) com name, tools e ferramentas conforme o TD-29", n))
}
