// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/doctor/doctor_test.go @ 0eac73a (MIT).
// Alterações: casos de versão do Go e de configuração mantidos e adaptados (versão
// mínima vem do go.mod do projeto; config só api, TD-07); casos de pre-push e
// .agents removidos com as checagens (Antigravity, TD-02; pre-push é ponto em
// aberto 6); comandos externos injetados em vez de depender da máquina; casos
// novos para hook no settings.json, binário e filesystem.

package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

const validConfig = `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "protected_paths": ["internal/"], "editor_agents": ["dev-implementation"]}`

const validSettings = `{"hooks": {"PreToolUse": [{"matcher": "Edit|Write|MultiEdit|NotebookEdit",
 "hooks": [{"type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/bin/pipeline-claude hook"}]}]}}`

// fakeEnv simula PATH e saída de comandos.
type fakeEnv struct {
	paths   map[string]bool   // binários no PATH
	outputs map[string]string // "nome args" → stdout
	fails   map[string]bool   // "nome args" que falham
}

func (f *fakeEnv) env() env {
	return env{
		lookPath: func(name string) (string, error) {
			if f.paths[name] {
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		output: func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := strings.TrimSpace(filepath.Base(name) + " " + strings.Join(args, " "))
			if f.fails[key] {
				return nil, errors.New("saída com erro")
			}
			out, ok := f.outputs[key]
			if !ok {
				return nil, exec.ErrNotFound
			}
			return []byte(out), nil
		},
	}
}

func healthyEnv() *fakeEnv {
	return &fakeEnv{
		paths: map[string]bool{"go": true, "git": true, "gh": true, "glab": true},
		outputs: map[string]string{
			"go version":              "go version go1.24.3 linux/amd64\n",
			"pipeline-claude version": "pipeline-claude v0.2.0\n",
		},
		fails: map[string]bool{},
	}
}

// project monta um projeto temporário saudável; files sobrescreve ou, com
// valor vazio, remove arquivos.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	base := map[string]string{
		config.FileName:         validConfig,
		"go.mod":                "module exemplo.dev/loja\n\ngo 1.24\n",
		".claude/settings.json": validSettings,
		"Makefile":              "PIPELINE_CLAUDE_VERSION := v0.2.0\n\ntools:\n\tGOBIN=$(CURDIR)/bin go install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@$(PIPELINE_CLAUDE_VERSION)\n",
		"bin/" + binaryName():   "binário falso",
	}
	for k, v := range files {
		base[k] = v
	}
	for name, body := range base {
		if body == "" {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q ausente em %+v", name, checks)
	return Check{}
}

func TestParseGoVersion(t *testing.T) {
	// Casos do original. O original fixava o mínimo em 1.24; agora o mínimo vem
	// do go.mod do projeto, então o caso só confere a leitura.
	tests := []struct {
		name   string
		output string
		want   [3]int
		wantOk bool
	}{
		{"go 1.24.0", "go version go1.24.0 linux/amd64", [3]int{1, 24, 0}, true},
		{"go 1.25beta1", "go version go1.25beta1 darwin/arm64", [3]int{1, 25, 0}, true},
		{"go 1.23.2 (abaixo do requisito)", "go version go1.23.2 linux/amd64", [3]int{1, 23, 2}, true},
		{"go 1.22", "go version go1.22 linux/amd64", [3]int{1, 22, 0}, true},
		{"formato desconhecido", "not a valid go version output", [3]int{}, false},
		// Novos.
		{"go 1.27.1", "go version go1.27.1 linux/amd64\n", [3]int{1, 27, 1}, true},
		{"go 1.25rc2", "go version go1.25rc2 windows/amd64", [3]int{1, 25, 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseGoVersion(tt.output)
			if ok != tt.wantOk || got != tt.want {
				t.Errorf("parseGoVersion(%q) = %v, %v; quer %v, %v", tt.output, got, ok, tt.want, tt.wantOk)
			}
		})
	}
}

func TestCheckGo(t *testing.T) {
	tests := []struct {
		name   string
		gomod  string // vazio remove go.mod
		goOut  string // vazio: go fora do PATH
		status Status
	}{
		{"versão igual ao go.mod", "module x\n\ngo 1.24\n", "go version go1.24.0 linux/amd64", StatusOK},
		{"versão maior", "module x\n\ngo 1.24\n", "go version go1.27.1 linux/amd64", StatusOK},
		{"versão menor", "module x\n\ngo 1.25\n", "go version go1.24.9 linux/amd64", StatusError},
		{"patch menor", "module x\n\ngo 1.24.5\n", "go version go1.24.3 linux/amd64", StatusError},
		{"patch maior", "module x\n\ngo 1.24.2\n", "go version go1.24.3 linux/amd64", StatusOK},
		{"go ausente", "module x\n\ngo 1.24\n", "", StatusError},
		{"saída irreconhecível", "module x\n\ngo 1.24\n", "versão desconhecida", StatusError},
		{"go.mod sem diretiva go", "module x\n", "go version go1.24.0 linux/amd64", StatusWarn},
		{"sem go.mod", "", "go version go1.24.0 linux/amd64", StatusWarn},
		{"diretiva go ilegível", "module x\n\ngo um.dois\n", "go version go1.24.0 linux/amd64", StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{"go.mod": tt.gomod})
			f := healthyEnv()
			if tt.goOut == "" {
				f.paths["go"] = false
			} else {
				f.outputs["go version"] = tt.goOut
			}
			c := find(t, collect(root, f.env()), checkGo)
			if c.Status != tt.status {
				t.Errorf("status = %s, quer %s (%s)", c.Status, tt.status, c.Message)
			}
			if c.Status != StatusOK && c.Action == "" {
				t.Error("check sem ação corretiva")
			}
		})
	}
}

func TestCheckGit(t *testing.T) {
	root := project(t, nil)
	f := healthyEnv()
	if c := find(t, collect(root, f.env()), checkGit); c.Status != StatusOK {
		t.Errorf("git presente: status = %s", c.Status)
	}
	f.paths["git"] = false
	if c := find(t, collect(root, f.env()), checkGit); c.Status != StatusError || c.Action == "" {
		t.Errorf("git ausente: %+v, quer erro com ação", c)
	}
}

func TestCheckProvider(t *testing.T) {
	gitlab := strings.Replace(validConfig, `"github"`, `"gitlab"`, 1)
	tests := []struct {
		name    string
		config  string
		missing string
		status  Status
		mention string
	}{
		{"github com gh", validConfig, "", StatusOK, "gh"},
		{"github sem gh", validConfig, "gh", StatusWarn, "gh"},
		{"github sem glab não importa", validConfig, "glab", StatusOK, "gh"},
		{"gitlab com glab", gitlab, "", StatusOK, "glab"},
		{"gitlab sem glab", gitlab, "glab", StatusWarn, "glab"},
		{"config inválida", `{"version":1}`, "", StatusWarn, "configuração"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{config.FileName: tt.config})
			f := healthyEnv()
			if tt.missing != "" {
				f.paths[tt.missing] = false
			}
			c := find(t, collect(root, f.env()), checkProvider)
			if c.Status != tt.status {
				t.Errorf("status = %s, quer %s (%s)", c.Status, tt.status, c.Message)
			}
			if !strings.Contains(c.Message+c.Action, tt.mention) {
				t.Errorf("check não menciona %q: %+v", tt.mention, c)
			}
		})
	}
}

func TestCheckConfig(t *testing.T) {
	// Casos do original: sem config, válida, inválida. "desktop" virou "api" (TD-07).
	tests := []struct {
		name   string
		config string
		status Status
	}{
		{"sem config", "", StatusError},
		{"config válida", validConfig, StatusOK},
		{"config inválida", `{"version": 1, "project_type": "invalido"}`, StatusError},
		{"campo desconhecido", strings.Replace(validConfig, `"version": 1`, `"version": 1, "extra": 1`, 1), StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{config.FileName: tt.config})
			if tt.config == "" {
				if _, err := config.Find(root); err == nil {
					t.Skip("existe um " + config.FileName + " acima de " + root)
				}
			}
			c := find(t, collect(root, healthyEnv().env()), checkConfig)
			if c.Status != tt.status {
				t.Errorf("status = %s, quer %s (%s)", c.Status, tt.status, c.Message)
			}
			if c.Status != StatusOK && c.Action == "" {
				t.Error("check sem ação corretiva")
			}
		})
	}
}

func TestCheckHook(t *testing.T) {
	entry := func(event, matcher, command string) string {
		return `{"hooks": {"` + event + `": [{"matcher": "` + matcher + `", "hooks": [{"type": "command", "command": "` + command + `"}]}]}}`
	}
	tests := []struct {
		name     string
		settings string
		status   Status
		mention  string
	}{
		{"registrado", validSettings, StatusOK, ""},
		{"comando no PATH", entry("PreToolUse", "Edit|Write|MultiEdit|NotebookEdit", "pipeline-claude hook"), StatusOK, ""},
		{"comando relativo", entry("PreToolUse", "Edit|Write|MultiEdit|NotebookEdit", "bin/pipeline-claude hook"), StatusOK, ""},
		{"matcher em outra ordem", entry("PreToolUse", "NotebookEdit|MultiEdit|Write|Edit", "pipeline-claude hook"), StatusOK, ""},
		{"matcher curinga", entry("PreToolUse", "*", "pipeline-claude hook"), StatusOK, ""},
		{"matcher vazio", entry("PreToolUse", "", "pipeline-claude hook"), StatusOK, ""},
		{"matcher regex", entry("PreToolUse", "Edit|Write|.*Edit", "pipeline-claude hook"), StatusOK, ""},
		{"matcher sem NotebookEdit", entry("PreToolUse", "Edit|Write|MultiEdit", "pipeline-claude hook"), StatusError, "NotebookEdit"},
		{"matcher só Edit casa prefixo não", entry("PreToolUse", "Edit", "pipeline-claude hook"), StatusError, "MultiEdit"},
		{"evento errado", entry("PostToolUse", "Edit|Write|MultiEdit|NotebookEdit", "pipeline-claude hook"), StatusError, "PreToolUse"},
		{"outro comando", entry("PreToolUse", "Edit|Write|MultiEdit|NotebookEdit", "pipeline-claude validate"), StatusError, "pipeline-claude hook"},
		{"comando com argumento extra", entry("PreToolUse", "Edit|Write|MultiEdit|NotebookEdit", "pipeline-claude hook || true"), StatusError, "pipeline-claude hook"},
		{"sem hooks", `{}`, StatusError, "pipeline-claude hook"},
		{"settings ausente", "", StatusError, "settings.json"},
		{"JSON inválido", `{"hooks":`, StatusError, "settings.json"},
		{"hooks com tipo errado", `{"hooks": []}`, StatusError, "settings.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{".claude/settings.json": tt.settings})
			c := find(t, collect(root, healthyEnv().env()), checkHook)
			if c.Status != tt.status {
				t.Errorf("status = %s, quer %s (%s)", c.Status, tt.status, c.Message)
			}
			if !strings.Contains(c.Message+" "+c.Action, tt.mention) {
				t.Errorf("check não menciona %q: %+v", tt.mention, c)
			}
		})
	}
}

func TestCheckBinary(t *testing.T) {
	tests := []struct {
		name     string
		makefile string
		noBinary bool
		out      string // vazio: comando falha
		status   Status
	}{
		{"versão igual", "PIPELINE_CLAUDE_VERSION := v0.2.0\n", false, "pipeline-claude v0.2.0\n", StatusOK},
		{"versão diferente", "PIPELINE_CLAUDE_VERSION := v0.3.0\n", false, "pipeline-claude v0.2.0\n", StatusError},
		{"versão no go install", "tools:\n\tgo install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@v0.2.0\n", false, "pipeline-claude v0.2.0\n", StatusOK},
		{"versão no go install diferente", "tools:\n\tgo install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@v0.1.0\n", false, "pipeline-claude v0.2.0\n", StatusError},
		{"sem versão declarada", "build:\n\tgo build ./...\n", false, "pipeline-claude dev\n", StatusOK},
		{"sem Makefile", "", false, "pipeline-claude v0.2.0\n", StatusOK},
		{"binário ausente", "PIPELINE_CLAUDE_VERSION := v0.2.0\n", true, "", StatusError},
		{"binário falha", "PIPELINE_CLAUDE_VERSION := v0.2.0\n", false, "", StatusError},
		{"saída irreconhecível", "PIPELINE_CLAUDE_VERSION := v0.2.0\n", false, "outra coisa\n", StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"Makefile": tt.makefile}
			if tt.noBinary {
				files["bin/"+binaryName()] = ""
			}
			root := project(t, files)
			f := healthyEnv()
			if tt.out == "" {
				f.fails["pipeline-claude version"] = true
			} else {
				f.outputs["pipeline-claude version"] = tt.out
			}
			c := find(t, collect(root, f.env()), checkBinary)
			if c.Status != tt.status {
				t.Errorf("status = %s, quer %s (%s)", c.Status, tt.status, c.Message)
			}
			if c.Status != StatusOK && c.Action == "" {
				t.Error("check sem ação corretiva")
			}
		})
	}
}

func TestCheckCaseFS(t *testing.T) {
	root := project(t, nil)
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	c := find(t, collect(root, healthyEnv().env()), checkCaseFS)
	if c.Status != StatusOK && c.Status != StatusWarn {
		t.Errorf("status = %s, quer ok ou aviso (%s)", c.Status, c.Message)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("checagem deixou arquivos na raiz: antes %d, depois %d", len(before), len(after))
	}
}

func TestCaseInsensitive(t *testing.T) {
	// Em diretório sem permissão de escrita a checagem não conclui: aviso, não ok.
	if os.Geteuid() == 0 {
		t.Skip("root ignora permissão de diretório")
	}
	root := project(t, nil)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	c := find(t, collect(root, healthyEnv().env()), checkCaseFS)
	if c.Status != StatusWarn {
		t.Errorf("status = %s, quer aviso (%s)", c.Status, c.Message)
	}
}

func TestCollectSaudavel(t *testing.T) {
	root := project(t, nil)
	for _, c := range collect(root, healthyEnv().env()) {
		if c.Status == StatusError {
			t.Errorf("projeto saudável com erro: %+v", c)
		}
		if c.Name == "" || c.Message == "" {
			t.Errorf("check incompleto: %+v", c)
		}
	}
}

func TestCollectSubdiretorio(t *testing.T) {
	// O doctor rodado de um subdiretório usa a raiz onde está a configuração.
	root := project(t, nil)
	sub := filepath.Join(root, "internal", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range collect(sub, healthyEnv().env()) {
		if c.Status == StatusError {
			t.Errorf("erro a partir de subdiretório: %+v", c)
		}
	}
}

func TestRun(t *testing.T) {
	t.Run("humano ok", func(t *testing.T) {
		root := project(t, nil)
		var out bytes.Buffer
		code := run(root, false, &out, healthyEnv().env())
		if code != 0 {
			t.Errorf("código = %d, quer 0\n%s", code, out.String())
		}
		for _, w := range []string{checkGo, checkHook, "[ok]"} {
			if !strings.Contains(out.String(), w) {
				t.Errorf("saída sem %q:\n%s", w, out.String())
			}
		}
	})
	t.Run("humano com erro", func(t *testing.T) {
		root := project(t, map[string]string{".claude/settings.json": `{}`})
		var out bytes.Buffer
		code := run(root, false, &out, healthyEnv().env())
		if code != 1 {
			t.Errorf("código = %d, quer 1", code)
		}
		if !strings.Contains(out.String(), "[erro]") {
			t.Errorf("saída sem [erro]:\n%s", out.String())
		}
	})
	t.Run("JSON", func(t *testing.T) {
		root := project(t, map[string]string{".claude/settings.json": `{}`})
		var out bytes.Buffer
		code := run(root, true, &out, healthyEnv().env())
		if code != 1 {
			t.Errorf("código = %d, quer 1", code)
		}
		var rep struct {
			OK     bool    `json:"ok"`
			Checks []Check `json:"checks"`
		}
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("saída não é JSON: %v\n%s", err, out.String())
		}
		if rep.OK || len(rep.Checks) == 0 {
			t.Errorf("rep = %+v, quer ok=false e checks", rep)
		}
		for _, c := range rep.Checks {
			if c.Name == "" || c.Status == "" {
				t.Errorf("check sem name/status: %+v", c)
			}
		}
	})
	t.Run("aviso não reprova", func(t *testing.T) {
		root := project(t, nil)
		f := healthyEnv()
		f.paths["gh"] = false
		var out bytes.Buffer
		if code := run(root, false, &out, f.env()); code != 0 {
			t.Errorf("código = %d, quer 0 com só aviso\n%s", code, out.String())
		}
	})
}

// O go version roda com GOTOOLCHAIN=local: sem isso, um go.mod com versão
// maior faria o go baixar outro toolchain e a checagem mentiria.
func TestGoVersionSemTrocaDeToolchain(t *testing.T) {
	cmd := newCmd(context.Background(), "go", "version")
	found := false
	for _, e := range cmd.Env {
		if e == "GOTOOLCHAIN=local" {
			found = true
		}
	}
	if !found {
		t.Error("go version sem GOTOOLCHAIN=local")
	}
}

// Com o ambiente real: roda sem pânico e devolve todas as checagens.
func TestCollectAmbienteReal(t *testing.T) {
	root := project(t, nil)
	checks := Collect(root)
	if len(checks) != 7 {
		t.Fatalf("checks = %d, quer 7: %+v", len(checks), checks)
	}
	for _, c := range checks {
		t.Logf("[%s] %s: %s", c.Status, c.Name, c.Message)
	}
}
