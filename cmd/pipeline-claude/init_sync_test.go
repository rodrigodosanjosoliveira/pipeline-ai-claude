package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// buildinfoVar e e2eVersion fixam a versão do binário de teste (TestMain).
const (
	buildinfoVar = "github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo.version"
	e2eVersion   = "v0.0.0-test"
)

const (
	e2eAgent  = ".claude/agents/dev-implementation.md"
	e2eTenant = ".claude/agents/tenant-isolation.md"
	e2eLock   = ".pipeline-claude.lock"
)

// initProject roda o init do binário num diretório novo e devolve a raiz.
func initProject(t *testing.T, args ...string) string {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "biblioteca-bairro")
	all := append([]string{"init", "biblioteca-bairro", "--dir", root, "--module", "example.com/biblioteca-bairro",
		"--summary", "API de empréstimos da biblioteca do bairro."}, args...)
	code, stdout, stderr := runBin(t, parent, "", all...)
	if code != exitOK {
		t.Fatalf("init: código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	return root
}

// syncOut é a saída --json do sync.
type syncOut struct {
	OK    bool `json:"ok"`
	Items []struct {
		Path   string `json:"path"`
		Action string `json:"action"`
		Reason string `json:"reason"`
	} `json:"items"`
}

func runSyncJSON(t *testing.T, root string, args ...string) (int, syncOut) {
	t.Helper()
	code, stdout, stderr := runBin(t, root, "", append([]string{"sync", "--json"}, args...)...)
	var out syncOut
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("sync --json não é JSON: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return code, out
}

func (o syncOut) action(path string) string {
	for _, it := range o.Items {
		if it.Path == path {
			return it.Action
		}
	}
	return ""
}

func setConfigModules(t *testing.T, root string, modules ...string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, root, ".pipeline-claude.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["enabled_modules"] = modules
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".pipeline-claude.json", string(data))
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func TestE2EInit(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "biblioteca-bairro")
	code, stdout, stderr := runBin(t, parent, "", "init", "biblioteca-bairro", "--json", "--modules", "tenant-isolation,data-model")
	if code != exitOK {
		t.Fatalf("init --json: código %d\nstderr:\n%s", code, stderr)
	}
	var out struct {
		Root  string   `json:"root"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("saída não é JSON: %v\n%s", err, stdout)
	}
	// Destino padrão: ./<nome>. O TempDir pode ter symlink no caminho (macOS).
	if want, _ := filepath.EvalSymlinks(root); out.Root != root && out.Root != want {
		t.Errorf("root = %q, quero %q", out.Root, root)
	}
	for _, f := range []string{".pipeline-claude.json", e2eLock, "Makefile", e2eAgent, e2eTenant, ".claude/agents/data-model.md", "cmd/biblioteca-bairro/main.go"} {
		if !slices.Contains(out.Files, f) {
			t.Errorf("files sem %s: %v", f, out.Files)
		}
		if !exists(root, f) {
			t.Errorf("%s não criado", f)
		}
	}
	// A versão do binário vai para o Makefile e para o lock (TD-06, TD-23).
	if mk := readFile(t, root, "Makefile"); !strings.Contains(mk, e2eVersion) {
		t.Errorf("Makefile sem a versão %s", e2eVersion)
	}
	if lock := readFile(t, root, e2eLock); !strings.Contains(lock, `"toolkit_version": "`+e2eVersion+`"`) {
		t.Errorf("lock sem a versão %s:\n%s", e2eVersion, lock)
	}

	t.Run("módulo que não termina no nome avisa", func(t *testing.T) {
		parent := t.TempDir()
		code, stdout, stderr := runBin(t, parent, "", "init", "biblioteca-bairro", "--module", "github.com/exemplo/biblioteca-bairo", "--toolkit-version", "v0.3.0", "--json")
		if code != exitOK {
			t.Fatalf("código %d\nstderr:\n%s", code, stderr)
		}
		for _, want := range []string{"aviso", `"biblioteca-bairo"`, `"biblioteca-bairro"`} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr sem %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stdout, "aviso") {
			t.Errorf("aviso no stdout (quebra o --json):\n%s", stdout)
		}
	})

	t.Run("módulo que termina no nome não avisa", func(t *testing.T) {
		parent := t.TempDir()
		code, _, stderr := runBin(t, parent, "", "init", "biblioteca-bairro", "--module", "github.com/exemplo/biblioteca-bairro", "--toolkit-version", "v0.3.0")
		if code != exitOK || strings.Contains(stderr, "aviso") {
			t.Errorf("código %d, stderr:\n%s", code, stderr)
		}
	})

	t.Run("saída humana com próximos passos", func(t *testing.T) {
		parent := t.TempDir()
		code, stdout, stderr := runBin(t, parent, "", "init", "loja-bairro", "--toolkit-version", "v0.3.0")
		if code != exitOK {
			t.Fatalf("código %d\nstderr:\n%s", code, stderr)
		}
		for _, want := range []string{"loja-bairro", "make tools", "bin/pipeline-claude doctor", "git init"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout sem %q:\n%s", want, stdout)
			}
		}
		if mk := readFile(t, filepath.Join(parent, "loja-bairro"), "Makefile"); !strings.Contains(mk, "v0.3.0") {
			t.Error("--toolkit-version não foi para o Makefile")
		}
	})
	t.Run("destino com conteúdo é uso inválido e nada muda", func(t *testing.T) {
		code, _, stderr := runBin(t, parent, "", "init", "biblioteca-bairro")
		if code != exitBlocked || !strings.Contains(stderr, "não está vazio") {
			t.Errorf("código %d, stderr %q", code, stderr)
		}
	})
	t.Run("validação é uso inválido", func(t *testing.T) {
		dir := t.TempDir()
		for _, args := range [][]string{
			{"init", "Biblioteca"},
			{"init", "loja", "--provider", "bitbucket"},
			{"init", "loja", "--modules", "pagamentos"},
			{"init", "loja", "--toolkit-version", "0.3.0"},
		} {
			if code, _, stderr := runBin(t, dir, "", args...); code != exitBlocked || !strings.Contains(stderr, "pipeline-claude init") {
				t.Errorf("%v: código %d, stderr %q", args, code, stderr)
			}
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("init inválido deixou %d entrada(s)", len(entries))
		}
	})
	t.Run("pai inexistente é falha (1)", func(t *testing.T) {
		code, _, stderr := runBin(t, t.TempDir(), "", "init", "loja", "--dir", filepath.Join(t.TempDir(), "nao-existe", "loja"))
		if code != exitFail || stderr == "" {
			t.Errorf("código %d, stderr %q", code, stderr)
		}
	})
}

// TestE2EInitCI: o init grava o CI do provedor (e só ele) e o .gitignore com a
// config local do Claude Code; o sync não toca no CI (TD-22).
func TestE2EInitCI(t *testing.T) {
	const gh, gl = ".github/workflows/delivery.yml", ".gitlab-ci.yml"
	tests := []struct {
		provider    string
		want, other string
		marker      string
	}{
		{"github", gh, gl, "bin/pipeline-claude sensors"},
		{"gitlab", gl, gh, `GIT_DEPTH: "0"`},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			root := initProject(t, "--provider", tt.provider)
			if !exists(root, tt.want) {
				t.Fatalf("%s não criado", tt.want)
			}
			if exists(root, tt.other) {
				t.Errorf("%s do outro provedor criado", tt.other)
			}
			ci := readFile(t, root, tt.want)
			if !strings.Contains(ci, tt.marker) || !strings.Contains(ci, "make tools") {
				t.Errorf("%s sem %q ou make tools:\n%s", tt.want, tt.marker, ci)
			}
			if !slices.Contains(strings.Split(readFile(t, root, ".gitignore"), "\n"), ".claude/settings.local.json") {
				t.Error(".gitignore sem .claude/settings.local.json")
			}
			if lock := readFile(t, root, e2eLock); strings.Contains(lock, tt.want) {
				t.Errorf("CI no lock (é do projeto):\n%s", lock)
			}
			// O CI é do projeto: editado, o sync não o menciona nem o reverte.
			writeFile(t, root, tt.want, ci+"# ajuste do projeto\n")
			code, out := runSyncJSON(t, root)
			if code != exitOK || out.action(tt.want) != "" {
				t.Errorf("sync: código %d, ação em %s = %q", code, tt.want, out.action(tt.want))
			}
			if got := readFile(t, root, tt.want); !strings.HasSuffix(got, "# ajuste do projeto\n") {
				t.Error("sync alterou o CI do projeto")
			}
		})
	}
}

func TestE2EInitGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git indisponível")
	}
	parent := t.TempDir()
	code, stdout, stderr := runBin(t, parent, "", "init", "loja", "--git", "--json")
	if code != exitOK {
		t.Fatalf("código %d\nstderr:\n%s", code, stderr)
	}
	// A saída do git não pode sujar o JSON.
	var out struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout não é JSON: %v\n%s", err, stdout)
	}
	if !exists(filepath.Join(parent, "loja"), ".git") {
		t.Error(".git não criado")
	}
	// Com --git o repositório já existe: os próximos passos não mandam criá-lo.
	code, stdout, stderr = runBin(t, parent, "", "init", "loja-2", "--git")
	if code != exitOK || strings.Contains(stdout, "git init") || !strings.Contains(stdout, "make tools") {
		t.Errorf("init --git humano: código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// TestE2EGeneratedProject percorre o projeto gerado: doctor, delivery,
// validate e o ciclo do sync (TD-25).
func TestE2EGeneratedProject(t *testing.T) {
	root := initProject(t)

	t.Run("doctor", func(t *testing.T) {
		_, stdout, stderr := runBin(t, root, "", "doctor", "--json")
		var out struct {
			Checks []struct {
				Name    string `json:"name"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"checks"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("doctor --json: %v\n%s\n%s", err, stdout, stderr)
		}
		got := map[string]string{}
		for _, c := range out.Checks {
			got[c.Name] = c.Status + ": " + c.Message
		}
		// binário pode falhar: o teste não instala bin/.
		for _, name := range []string{"configuração", "hook", "fmt-hook", "sync"} {
			if !strings.HasPrefix(got[name], "ok: ") {
				t.Errorf("doctor %s = %q, quero ok", name, got[name])
			}
		}
	})

	t.Run("delivery e validate", func(t *testing.T) {
		code, stdout, stderr := runBin(t, root, "", "delivery", "new", "--json", "--title", "Reserva de livros")
		if code != exitOK {
			t.Fatalf("delivery new: código %d\n%s", code, stderr)
		}
		var out struct {
			ID     string `json:"id"`
			Report string `json:"report"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatal(err)
		}
		// Recém-criada reprova por conteúdo, nunca por índice ou esquema.
		code, stdout, _ = runBin(t, root, "", "validate", "--no-git", out.ID)
		if code != exitFail || strings.Contains(stdout, "índice") {
			t.Errorf("validate recém-criada: código %d\n%s", code, stdout)
		}
		report := readFile(t, root, out.Report)
		report = strings.ReplaceAll(report, "<preencher>", "conferido na revisão")
		report = strings.ReplaceAll(report, "- [ ]", "- [x]")
		writeFile(t, root, out.Report, report)
		if code, stdout, _ := runBin(t, root, "", "validate", "--no-git", out.ID); code != exitOK {
			t.Errorf("validate preenchida: código %d\n%s", code, stdout)
		}
	})

	t.Run("sync sem mudança", func(t *testing.T) {
		lock := readFile(t, root, e2eLock)
		code, out := runSyncJSON(t, root)
		if code != exitOK || !out.OK || len(out.Items) == 0 {
			t.Fatalf("código %d, %+v", code, out)
		}
		for _, it := range out.Items {
			if it.Action != "nada" {
				t.Errorf("%s: %s, quero nada", it.Path, it.Action)
			}
		}
		if readFile(t, root, e2eLock) != lock {
			t.Error("sync sem mudança reescreveu o lock")
		}
		code, stdout, _ := runBin(t, root, "", "sync")
		if code != exitOK || strings.Contains(stdout, e2eAgent) || !strings.HasPrefix(stdout, "Resumo: 0 mudança") {
			t.Errorf("sync humano: código %d, item nada listado ou linha sobrando:\n%s", code, stdout)
		}
	})

	edited := readFile(t, root, e2eAgent) + "\nRegra local da biblioteca.\n"
	t.Run("editado não é tocado", func(t *testing.T) {
		writeFile(t, root, e2eAgent, edited)
		code, out := runSyncJSON(t, root)
		if code != exitFail || out.OK || out.action(e2eAgent) != "editado" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if readFile(t, root, e2eAgent) != edited {
			t.Error("sync alterou o arquivo editado")
		}
		code, stdout, _ := runBin(t, root, "", "sync")
		if code != exitFail || !strings.Contains(stdout, e2eAgent) || !strings.Contains(stdout, "--release") {
			t.Errorf("sync humano: código %d\n%s", code, stdout)
		}
	})

	t.Run("apagado é recriado", func(t *testing.T) {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(e2eAgent))); err != nil {
			t.Fatal(err)
		}
		code, out := runSyncJSON(t, root)
		if code != exitOK || out.action(e2eAgent) != "recriar" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if got := readFile(t, root, e2eAgent); got == edited || got == "" {
			t.Error("subagente não foi recriado com o conteúdo gerado")
		}
		if code, out := runSyncJSON(t, root); code != exitOK || out.action(e2eAgent) != "nada" {
			t.Errorf("segundo sync: código %d, %+v", code, out)
		}
	})

	t.Run("dry-run não grava", func(t *testing.T) {
		writeFile(t, root, e2eAgent, edited)
		if err := os.Remove(filepath.Join(root, ".claude", "agents", "code-reviewer.md")); err != nil {
			t.Fatal(err)
		}
		lock := readFile(t, root, e2eLock)
		code, out := runSyncJSON(t, root, "--dry-run")
		if code != exitFail || out.action(".claude/agents/code-reviewer.md") != "recriar" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if exists(root, ".claude/agents/code-reviewer.md") || readFile(t, root, e2eLock) != lock {
			t.Error("dry-run gravou")
		}
	})

	t.Run("release libera o editado", func(t *testing.T) {
		code, out := runSyncJSON(t, root, "--release", e2eAgent)
		if code != exitOK || !out.OK || out.action(e2eAgent) != "liberado" || out.action(".claude/agents/code-reviewer.md") != "recriar" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if readFile(t, root, e2eAgent) != edited {
			t.Error("release alterou o arquivo")
		}
		if code, out := runSyncJSON(t, root); code != exitOK || out.action(e2eAgent) != "liberado" {
			t.Errorf("sync depois do release: código %d, %+v", code, out)
		}
		code, _, stderr := runBin(t, root, "", "sync", "--release", ".claude/agents/inexistente.md")
		if code != exitBlocked || !strings.Contains(stderr, "não está no lock") {
			t.Errorf("release fora do lock: código %d, %q", code, stderr)
		}
	})

	t.Run("não gerenciado não é tocado", func(t *testing.T) {
		setConfigModules(t, root, "data-model")
		writeFile(t, root, ".claude/agents/data-model.md", "subagente local\n")
		code, out := runSyncJSON(t, root)
		if code != exitFail || out.OK || out.action(".claude/agents/data-model.md") != "nao-gerenciado" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if readFile(t, root, ".claude/agents/data-model.md") != "subagente local\n" {
			t.Error("sync alterou o não gerenciado")
		}
		mustRemove(t, root, ".claude/agents/data-model.md")
		setConfigModules(t, root)
	})

	t.Run("módulo ligado cria o subagente", func(t *testing.T) {
		setConfigModules(t, root, "tenant-isolation")
		code, out := runSyncJSON(t, root)
		if code != exitOK || out.action(e2eTenant) != "criar" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if !exists(root, e2eTenant) {
			t.Error("subagente do módulo não criado")
		}
		setConfigModules(t, root)
		if code, out := runSyncJSON(t, root); code != exitOK || out.action(e2eTenant) != "apagar" || exists(root, e2eTenant) {
			t.Errorf("módulo desligado: código %d, %+v", code, out)
		}
	})
}

func TestE2ESyncBlocked(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
		want   string
	}{
		{"config ausente", func(t *testing.T, root string) { mustRemove(t, root, ".pipeline-claude.json") }, "doctor"},
		{"config inválida", func(t *testing.T, root string) { writeFile(t, root, ".pipeline-claude.json", "{") }, "doctor"},
		{"lock ausente", func(t *testing.T, root string) { mustRemove(t, root, e2eLock) }, "doctor"},
		{"lock inválido", func(t *testing.T, root string) { writeFile(t, root, e2eLock, "{") }, "doctor"},
		{"versão do Makefile diferente", func(t *testing.T, root string) {
			writeFile(t, root, "Makefile", strings.ReplaceAll(readFile(t, root, "Makefile"), e2eVersion, "v9.9.9"))
		}, "v9.9.9"},
		{"Makefile sem versão", func(t *testing.T, root string) { mustRemove(t, root, "Makefile") }, "Makefile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := initProject(t)
			mustRemove(t, root, e2eAgent) // pendência que o sync gravaria
			tt.mutate(t, root)
			code, stdout, stderr := runBin(t, root, "", "sync")
			if code != exitBlocked || !strings.Contains(stderr, tt.want) {
				t.Errorf("código %d, stderr %q, stdout %q", code, stderr, stdout)
			}
			if exists(root, e2eAgent) {
				t.Error("sync bloqueado gravou")
			}
		})
	}
}

func mustRemove(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func TestPinnable(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"v0.3.0", true},
		{"v0.4.0-rc.1", true},
		{"v0.0.0-20261027120000-abcdef123456", true},
		{"v0.0.0-20261027120000-abcdef123456+dirty", false},
		{"v0.3.0+meta", false},
		{"dev", false},
		{"", false},
		{"(devel)", false},
	}
	for _, tt := range tests {
		if got := pinnable(tt.v); got != tt.want {
			t.Errorf("pinnable(%q) = %v, quero %v", tt.v, got, tt.want)
		}
	}
}
