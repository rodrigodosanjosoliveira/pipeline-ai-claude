package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/scaffold"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/syncer"
)

// scaffolded gera um projeto com o init (sem git) e devolve a raiz.
func scaffolded(t *testing.T, modules ...string) string {
	t.Helper()
	res, err := scaffold.Init(scaffold.Options{
		Name:           "biblioteca-bairro",
		Dir:            filepath.Join(t.TempDir(), "biblioteca-bairro"),
		Module:         "example.com/biblioteca-bairro",
		ToolkitVersion: "v0.3.0",
		Modules:        modules,
	})
	if err != nil {
		t.Fatalf("scaffold.Init: %v", err)
	}
	return res.Root
}

func writeRel(t *testing.T, root, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendRel(t *testing.T, root, rel, body string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(rel)), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func removeRel(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// setModules troca enabled_modules na configuração gerada.
func setModules(t *testing.T, root string, modules ...string) {
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
	m["enabled_modules"] = modules
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	agentDev    = ".claude/agents/dev-implementation.md"
	agentReview = ".claude/agents/code-reviewer.md"
	agentTenant = ".claude/agents/tenant-isolation.md"
	hash0       = "0000000000000000000000000000000000000000000000000000000000000000"
)

func TestCheckSync(t *testing.T) {
	tests := []struct {
		name    string
		modules []string
		mutate  func(t *testing.T, root string)
		status  Status
		msg     []string // trechos esperados na mensagem, nesta ordem
		noMsg   []string // trechos que não podem aparecer na mensagem
		action  string   // trecho esperado na ação ("" = ação vazia)
	}{
		{
			name:   "gerado pelo scaffold",
			status: StatusOK,
		},
		{
			name:    "gerado com módulo",
			modules: []string{"tenant-isolation"},
			status:  StatusOK,
		},
		{
			name:   "config ausente",
			mutate: func(t *testing.T, root string) { removeRel(t, root, config.FileName) },
			status: StatusWarn,
			msg:    []string{"não verificado"},
			action: "configuração",
		},
		{
			name:   "config inválida",
			mutate: func(t *testing.T, root string) { writeRel(t, root, config.FileName, "{") },
			status: StatusWarn,
			msg:    []string{"não verificado"},
			action: "configuração",
		},
		{
			name:   "lock ausente",
			mutate: func(t *testing.T, root string) { removeRel(t, root, manifest.FileName) },
			status: StatusWarn,
			msg:    []string{manifest.FileName},
			action: "pipeline-claude init",
		},
		{
			name:   "lock ilegível",
			mutate: func(t *testing.T, root string) { writeRel(t, root, manifest.FileName, "{") },
			status: StatusError,
			msg:    []string{manifest.FileName},
			action: manifest.FileName,
		},
		{
			name: "lock com caminho fora da área gerenciada",
			mutate: func(t *testing.T, root string) {
				writeRel(t, root, manifest.FileName, `{"version": 1, "toolkit_version": "v0.3.0", "files": {"cmd/x/main.go": "`+hash0+`"}, "released": []}`)
			},
			status: StatusError,
			msg:    []string{"cmd/x/main.go"},
			action: manifest.FileName,
		},
		{
			name:   "gerenciado editado",
			mutate: func(t *testing.T, root string) { appendRel(t, root, agentDev, "\nregra local\n") },
			status: StatusWarn,
			msg:    []string{agentDev, "editado"},
			noMsg:  []string{agentReview},
			action: "sync --release",
		},
		{
			name: "dois editados, em ordem",
			mutate: func(t *testing.T, root string) {
				appendRel(t, root, agentReview, "\nx\n")
				appendRel(t, root, agentDev, "\ny\n")
			},
			status: StatusWarn,
			msg:    []string{agentReview, agentDev},
			action: "sync --release",
		},
		{
			name: "gerenciado fora do lock já existente",
			mutate: func(t *testing.T, root string) {
				setModules(t, root, "tenant-isolation")
				writeRel(t, root, agentTenant, "local\n")
			},
			status: StatusWarn,
			msg:    []string{agentTenant, "não gerenciado"},
			action: "sync --release",
		},
		{
			name:   "gerenciado apagado",
			mutate: func(t *testing.T, root string) { removeRel(t, root, agentDev) },
			status: StatusWarn,
			msg:    []string{"pendente", agentDev},
			action: "pipeline-claude sync",
		},
		{
			name:   "módulo habilitado depois do init",
			mutate: func(t *testing.T, root string) { setModules(t, root, "tenant-isolation") },
			status: StatusWarn,
			msg:    []string{"pendente", agentTenant},
			action: "pipeline-claude sync",
		},
		{
			name:    "módulo desligado depois do init",
			modules: []string{"tenant-isolation"},
			mutate:  func(t *testing.T, root string) { setModules(t, root) },
			status:  StatusWarn,
			msg:     []string{"pendente", agentTenant},
			action:  "pipeline-claude sync",
		},
		{
			name: "template mudou",
			mutate: func(t *testing.T, root string) {
				// Lock e disco concordam num conteúdo antigo: o template "mudou".
				writeRel(t, root, agentDev, "versão antiga\n")
				l, err := manifest.Read(root)
				if err != nil {
					t.Fatal(err)
				}
				l.Files[agentDev] = manifest.Hash([]byte("versão antiga\n"))
				if err := manifest.Write(root, l); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusWarn,
			msg:    []string{"pendente", agentDev},
			action: "pipeline-claude sync",
		},
		{
			name: "editado e pendente: conflito primeiro, pendência junto",
			mutate: func(t *testing.T, root string) {
				appendRel(t, root, agentDev, "\nregra local\n")
				removeRel(t, root, agentReview)
			},
			status: StatusWarn,
			msg:    []string{agentDev, agentReview},
			action: "sync --release",
		},
		{
			name: "liberado e editado",
			mutate: func(t *testing.T, root string) {
				if err := syncer.Release(root, []string{agentDev}); err != nil {
					t.Fatal(err)
				}
				appendRel(t, root, agentDev, "\nregra local\n")
			},
			status: StatusOK,
			msg:    []string{"1 liberado"},
		},
		{
			name: "symlink no lugar do gerenciado",
			mutate: func(t *testing.T, root string) {
				removeRel(t, root, agentDev)
				if err := os.Symlink(filepath.Join(root, "go.mod"), filepath.Join(root, filepath.FromSlash(agentDev))); err != nil {
					t.Skipf("symlink indisponível: %v", err)
				}
			},
			status: StatusWarn,
			msg:    []string{agentDev, "symlink"},
			action: "sync --release",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := scaffolded(t, tt.modules...)
			if tt.mutate != nil {
				tt.mutate(t, root)
			}
			c := find(t, collect(root, healthyEnv().env()), checkSync)
			if c.Status != tt.status {
				t.Fatalf("status = %s, quero %s (%s → %s)", c.Status, tt.status, c.Message, c.Action)
			}
			if c.Message == "" {
				t.Error("mensagem vazia")
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
			switch {
			case tt.action == "" && c.Action != "":
				t.Errorf("ação = %q, quero vazia", c.Action)
			case !strings.Contains(c.Action, tt.action):
				t.Errorf("ação %q sem %q", c.Action, tt.action)
			}
		})
	}
}

// TestCheckSyncNaoGrava: o doctor só lê (TD-27): nem com pendência ele cria,
// apaga ou reescreve arquivo gerenciado ou o lock.
func TestCheckSyncNaoGrava(t *testing.T) {
	root := scaffolded(t)
	removeRel(t, root, agentDev)
	appendRel(t, root, agentReview, "\nlocal\n")
	setModules(t, root, "tenant-isolation")
	before := snapshot(t, root)
	collect(root, healthyEnv().env())
	after := snapshot(t, root)
	if len(before) != len(after) {
		t.Fatalf("arquivos mudaram: %d → %d", len(before), len(after))
	}
	for p, b := range before {
		if after[p] != b {
			t.Errorf("%s mudou", p)
		}
	}
}

// snapshot lê todos os arquivos regulares sob root (sem o temporário do
// caseFSCheck, que é removido).
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m[p] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCheckSyncOrdem(t *testing.T) {
	// sync entra logo depois de fmt-hook (TD-27).
	checks := collect(project(t, nil), healthyEnv().env())
	for i, c := range checks {
		if c.Name == checkFmtHook {
			if i+1 >= len(checks) || checks[i+1].Name != checkSync {
				t.Errorf("checagem depois de fmt-hook não é sync: %+v", checks)
			}
			return
		}
	}
	t.Errorf("checagem fmt-hook ausente: %+v", checks)
}
