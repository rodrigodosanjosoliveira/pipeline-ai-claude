package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// dropSensors tira da config gerada os sensores com os nomes dados.
func dropSensors(t *testing.T, root string, names ...string) {
	t.Helper()
	p := filepath.Join(root, config.FileName)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var kept []any
	for _, s := range m["sensors"].([]any) {
		name := s.(map[string]any)["name"].(string)
		drop := false
		for _, n := range names {
			drop = drop || n == name
		}
		if !drop {
			kept = append(kept, s)
		}
	}
	m["sensors"] = kept
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSensors(t *testing.T) {
	tests := []struct {
		name    string
		modules []string
		mutate  func(t *testing.T, root string)
		status  Status
		msg     []string
		noMsg   []string
		action  string
	}{
		{name: "gerado pelo scaffold", status: StatusOK, msg: []string{"mod_verify", "vuln"}},
		{name: "gerado com agent-auditor", modules: []string{"agent-auditor"}, status: StatusOK, msg: []string{"test"}},
		{
			name:   "config ausente",
			mutate: func(t *testing.T, root string) { removeRel(t, root, config.FileName) },
			status: StatusWarn,
			msg:    []string{"não verificado"},
			action: "configuração",
		},
		{
			name:   "sem vuln",
			mutate: func(t *testing.T, root string) { dropSensors(t, root, "vuln") },
			status: StatusWarn,
			msg:    []string{"vuln (security-review, supply-chain-review)"},
			noMsg:  []string{"mod_verify ("},
			action: `"name": "vuln"`,
		},
		{
			name:   "sem vuln e mod_verify, em ordem",
			mutate: func(t *testing.T, root string) { dropSensors(t, root, "vuln", "mod_verify") },
			status: StatusWarn,
			msg:    []string{"mod_verify (supply-chain-review)", "vuln ("},
			action: `"name": "mod_verify"`,
		},
		{
			name:    "módulo pede test e a config não tem",
			modules: []string{"agent-auditor"},
			mutate:  func(t *testing.T, root string) { dropSensors(t, root, "test") },
			status:  StatusWarn,
			msg:     []string{"test (agent-auditor)"},
			action:  `"name": "test"`,
		},
		{
			name:   "sensor test sobrando sem o módulo não importa",
			mutate: func(t *testing.T, root string) { dropSensors(t, root, "test") },
			status: StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := scaffolded(t, tt.modules...)
			if tt.mutate != nil {
				tt.mutate(t, root)
			}
			c := find(t, collect(root, healthyEnv().env()), checkSensors)
			if c.Status != tt.status {
				t.Fatalf("status = %s, quero %s (%s → %s)", c.Status, tt.status, c.Message, c.Action)
			}
			rest := c.Message
			for _, s := range tt.msg {
				i := strings.Index(rest, s)
				if i < 0 {
					t.Errorf("mensagem %q sem %q (ou fora de ordem)", c.Message, s)
					continue
				}
				rest = rest[i+len(s):]
			}
			for _, s := range tt.noMsg {
				if strings.Contains(c.Message, s) {
					t.Errorf("mensagem %q com %q", c.Message, s)
				}
			}
			if !strings.Contains(c.Action, tt.action) {
				t.Errorf("ação %q sem %q", c.Action, tt.action)
			}
			if tt.action == "" && c.Action != "" {
				t.Errorf("ação = %q, quero vazia", c.Action)
			}
		})
	}
}

func TestCheckSensorsOrdem(t *testing.T) {
	// sensores entra logo depois de sync (TD-29).
	checks := collect(project(t, nil), healthyEnv().env())
	for i, c := range checks {
		if c.Name == checkSync {
			if i+1 >= len(checks) || checks[i+1].Name != checkSensors {
				t.Errorf("checagem depois de sync não é sensores: %+v", checks)
			}
			return
		}
	}
	t.Errorf("checagem sync ausente: %+v", checks)
}
