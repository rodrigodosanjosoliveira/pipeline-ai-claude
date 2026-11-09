package doctor

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// sensorNameRe é o formato de nome de sensor da config: nome fora dele numa
// chamada (placeholder de documentação, por exemplo) não é pedido de sensor.
var sensorNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// parseAgentSensors devolve, ordenados e sem repetição, os sensores que o texto
// de um subagente pede pelo nome (TD-48): numa linha com pipeline_sensors, a
// lista names: [...] depois dele na mesma linha ou no início da linha seguinte
// (prosa quebrada, como nos templates). A lista fecha na mesma linha; itens
// entre aspas simples ou duplas; item fora do formato de nome é ignorado.
func parseAgentSensors(data []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out []string
	for i, l := range lines {
		at := strings.Index(l, "pipeline_sensors")
		if at < 0 {
			continue
		}
		_, list, found := strings.Cut(l[at:], "names:")
		if !found && i+1 < len(lines) {
			next := strings.TrimLeft(strings.TrimSpace(lines[i+1]), "`")
			list, found = strings.CutPrefix(next, "names:")
		}
		if found {
			out = append(out, namesList(list)...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// namesList lê a lista em linha "[a, b]" no início de s.
func namesList(s string) []string {
	s, ok := strings.CutPrefix(strings.TrimSpace(s), "[")
	if !ok {
		return nil
	}
	s, _, ok = strings.Cut(s, "]")
	if !ok {
		return nil
	}
	var out []string
	for _, x := range strings.Split(s, ",") {
		if n := unquote(x); sensorNameRe.MatchString(n) {
			out = append(out, n)
		}
	}
	return out
}

// agentSensorRequests lê os .claude/agents/*.md do projeto, gerenciados,
// liberados ou fora do lock, e devolve sensor → subagentes que o pedem pelo
// nome. Symlink, arquivo irregular ou ilegível fica de fora: a checagem
// subagentes já os aponta como erro.
func agentSensorRequests(root string) map[string][]string {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(agentsRel)))
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, e := range entries {
		file := e.Name()
		if !strings.HasSuffix(file, ".md") || !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(agentsRel), file))
		if err != nil {
			continue
		}
		agent := strings.TrimSuffix(file, ".md")
		for _, s := range parseAgentSensors(data) {
			out[s] = append(out[s], agent)
		}
	}
	return out
}
