// Package fixtures sanitiza e verifica payloads de hook do Claude Code usados
// como fixtures de teste.
//
// O repositório é público. Payloads capturados em projetos reais carregam
// caminhos locais, identificadores de sessão e conteúdo de arquivos; nada disso
// pode ser versionado. Sanitize troca esses valores por marcadores fixos e Check
// garante que uma fixture versionada não carrega nada além disso.
package fixtures

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Valores fixos que substituem dados reais.
const (
	PlaceholderRoot       = "/workspace/exemplo"
	PlaceholderHome       = "/home/exemplo"
	PlaceholderSessionID  = "00000000-0000-0000-0000-000000000000"
	PlaceholderTranscript = PlaceholderHome + "/.claude/projects/exemplo/sessao.jsonl"
	PlaceholderScratchpad = PlaceholderHome + "/.claude/scratchpad/exemplo"
	PlaceholderAgentID    = "agent-0001"
	PlaceholderToolUseID  = "toolu_0001"
	PlaceholderContent    = "<removido>"
)

// contentKeys são campos cujo valor é conteúdo de arquivo ou resposta de
// ferramenta. Nunca vão para o repositório.
var contentKeys = map[string]bool{
	"content":       true,
	"old_string":    true,
	"new_string":    true,
	"new_source":    true,
	"tool_response": true,
	"prompt":        true,
	"message":       true,
}

// fixedKeys são campos identificadores trocados por valores fixos.
var fixedKeys = map[string]string{
	"session_id":      PlaceholderSessionID,
	"transcript_path": PlaceholderTranscript,
	// scratchpad_dir aponta para um diretório temporário fora do projeto e do
	// home (ex.: /tmp/claude-<uid>/...), que não é coberto pela troca de prefixo.
	"scratchpad_dir": PlaceholderScratchpad,
	"agent_id":       PlaceholderAgentID,
	"tool_use_id":    PlaceholderToolUseID,
}

// Options configura a sanitização.
type Options struct {
	// Root é o caminho absoluto da raiz do projeto onde a captura aconteceu.
	Root string
	// Home é o diretório home de quem capturou. Opcional.
	Home string
}

// Sanitize recebe um payload JSON bruto e devolve a versão sanitizada,
// indentada, e um nome de arquivo sugerido. Falha se, depois da troca, ainda
// restar Root ou Home em qualquer valor.
func Sanitize(raw []byte, opts Options) (out []byte, name string, err error) {
	root := strings.TrimRight(opts.Root, "/")
	home := strings.TrimRight(opts.Home, "/")
	if root == "" || !strings.HasPrefix(root, "/") {
		return nil, "", errors.New("fixtures: Root precisa ser um caminho absoluto")
	}

	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, "", fmt.Errorf("fixtures: payload não é JSON válido: %w", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, "", errors.New("fixtures: payload não é um objeto JSON")
	}

	clean := sanitizeValue("", m, root, home).(map[string]any)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // mantém "<removido>" legível
	enc.SetIndent("", "  ")
	if err := enc.Encode(clean); err != nil {
		return nil, "", fmt.Errorf("fixtures: falha ao serializar: %w", err)
	}
	out = buf.Bytes()

	if bytes.Contains(out, []byte(root)) || (home != "" && bytes.Contains(out, []byte(home))) {
		return nil, "", errors.New("fixtures: a saída ainda contém o caminho original; fixture descartada")
	}
	return out, suggestName(clean), nil
}

func sanitizeValue(key string, v any, root, home string) any {
	if fixed, ok := fixedKeys[key]; ok {
		if _, isString := v.(string); isString {
			return fixed
		}
	}
	if contentKeys[key] {
		return PlaceholderContent
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = sanitizeValue(k, val, root, home)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = sanitizeValue(key, val, root, home)
		}
		return out
	case string:
		s := replacePrefixAll(x, root, PlaceholderRoot)
		if home != "" {
			s = replacePrefixAll(s, home, PlaceholderHome)
		}
		return s
	default:
		return v
	}
}

// replacePrefixAll troca ocorrências de prefix seguidas de fim de string ou "/"
// por repl. Não troca prefix que seja só parte de outro nome (ex.: /a/b em /a/bc).
func replacePrefixAll(s, prefix, repl string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(prefix)
		if end == len(s) || s[end] == '/' || s[end] == '"' || s[end] == ' ' {
			b.WriteString(s[:i])
			b.WriteString(repl)
		} else {
			b.WriteString(s[:end])
		}
		s = s[end:]
	}
}

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

func suggestName(m map[string]any) string {
	part := func(k, def string) string {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
		return def
	}
	raw := strings.ToLower(fmt.Sprintf("%s-%s-%s",
		part("hook_event_name", "evento"),
		part("tool_name", "ferramenta"),
		part("agent_type", "main"),
	))
	return strings.Trim(unsafeName.ReplaceAllString(raw, "-"), "-") + ".json"
}

// Problem descreve uma violação encontrada por Check.
type Problem struct {
	Pointer string
	Message string
}

func (p Problem) String() string { return p.Pointer + ": " + p.Message }

// Check verifica se um payload versionado respeita as regras de fixture:
// identificadores fixos, conteúdo removido e todo caminho absoluto sob
// PlaceholderRoot ou PlaceholderHome. blocklist são termos proibidos
// (comparação sem diferenciar caixa) mantidos fora do repositório.
func Check(raw []byte, blocklist []string) []Problem {
	var problems []Problem
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []Problem{{Pointer: "/", Message: "JSON inválido: " + err.Error()}}
	}
	walk("", "", v, &problems)

	lower := strings.ToLower(string(raw))
	for _, term := range blocklist {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || strings.HasPrefix(term, "#") {
			continue
		}
		if strings.Contains(lower, term) {
			problems = append(problems, Problem{Pointer: "/", Message: "contém termo bloqueado da lista privada"})
		}
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Pointer < problems[j].Pointer })
	return problems
}

func walk(pointer, key string, v any, problems *[]Problem) {
	add := func(msg string) {
		*problems = append(*problems, Problem{Pointer: pointerOrRoot(pointer), Message: msg})
	}

	if fixed, ok := fixedKeys[key]; ok {
		if s, isString := v.(string); isString && s != fixed {
			add(fmt.Sprintf("%s deve ser %q", key, fixed))
		}
		return
	}
	if contentKeys[key] {
		if s, isString := v.(string); !isString || s != PlaceholderContent {
			add(fmt.Sprintf("%s deve ser %q", key, PlaceholderContent))
		}
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			walk(pointer+"/"+k, k, val, problems)
		}
	case []any:
		for i, val := range x {
			walk(fmt.Sprintf("%s/%d", pointer, i), key, val, problems)
		}
	case string:
		if strings.HasPrefix(x, "/") && !underPlaceholder(x) {
			add("caminho absoluto fora de " + PlaceholderRoot + " ou " + PlaceholderHome)
		}
	}
}

func underPlaceholder(p string) bool {
	c := path.Clean(p)
	for _, base := range []string{PlaceholderRoot, PlaceholderHome} {
		if c == base || strings.HasPrefix(c, base+"/") {
			return true
		}
	}
	return false
}

func pointerOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
