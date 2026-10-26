// Package hookio lê o payload dos hooks do Claude Code e resolve o caminho
// alvo contra a raiz do projeto. É compartilhado pelo hook PreToolUse e pelo
// fmt-hook PostToolUse, para os dois lerem o payload e resolverem caminhos do
// mesmo jeito (TD-08, TD-09).
package hookio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Payload é o objeto JSON recebido em stdin. Os campos são lidos sob demanda
// e por nome exato, para cada hook decidir em que ordem validar.
type Payload struct {
	// Map, não struct: encoding/json casaria chaves de struct sem diferenciar
	// caixa, e "Tool_Name" poderia mascarar "tool_name".
	fields map[string]json.RawMessage
}

// ReadPayload lê r inteiro e exige um objeto JSON.
func ReadPayload(r io.Reader) (Payload, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Payload{}, fmt.Errorf("falha ao ler stdin: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Payload{}, errors.New("payload não é um objeto JSON válido")
	}
	return Payload{fields: fields}, nil
}

// String lê o campo key como string. present=false se a chave não existir;
// erro se o valor não for string. null conta como presente e vazio.
func (p Payload) String(key string) (value string, present bool, err error) {
	raw, ok := p.fields[key]
	if !ok {
		return "", false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, fmt.Errorf("%s: %w", key, err)
	}
	return value, true, nil
}

// Object lê o campo key como objeto JSON. Ausente, null ou outro tipo é erro.
func (p Payload) Object(key string) (Payload, error) {
	raw, ok := p.fields[key]
	if !ok {
		return Payload{}, fmt.Errorf("%s ausente", key)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Payload{}, fmt.Errorf("%s não é um objeto", key)
	}
	return Payload{fields: fields}, nil
}

// pathKeys é, para cada ferramenta de edição, o campo de tool_input com o
// caminho alvo.
var pathKeys = map[string]string{
	"Edit":         "file_path",
	"Write":        "file_path",
	"MultiEdit":    "file_path",
	"NotebookEdit": "notebook_path",
}

// PathKey devolve o campo de tool_input com o caminho alvo da ferramenta de
// edição tool. ok=false se tool não for ferramenta de edição.
func PathKey(tool string) (key string, ok bool) {
	key, ok = pathKeys[tool]
	return key, ok
}

// Target é um caminho alvo resolvido contra a raiz do projeto.
type Target struct {
	// Inside diz se o alvo resolvido fica dentro da raiz.
	Inside bool
	// Rel é o caminho relativo à raiz, com "/". Vazio se Inside for false.
	Rel string
	// Real é o caminho absoluto depois de seguir os symlinks do maior prefixo
	// existente; é onde uma escrita de fato cai.
	Real string
}

// ResolveInRoot resolve target (relativo à raiz, se não for absoluto) seguindo
// symlinks (TD-09). Um link quebrado no caminho é erro: a ferramenta o seguiria
// para um destino que não dá para conferir.
func ResolveInRoot(root, target string) (Target, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return Target{}, fmt.Errorf("raiz %s: %w", root, err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return Target{}, fmt.Errorf("raiz %s: %w", root, err)
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(rootAbs, abs)
	}
	real, err := resolveExisting(filepath.Clean(abs))
	if err != nil {
		return Target{}, err
	}
	rel, ok := within(rootReal, real)
	if !ok {
		return Target{Real: real}, nil
	}
	return Target{Inside: true, Rel: filepath.ToSlash(rel), Real: real}, nil
}

// resolveExisting aplica EvalSymlinks ao maior prefixo existente de abs e
// devolve esse prefixo resolvido seguido do restante. O arquivo pode ainda não
// existir.
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
