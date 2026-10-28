package adopt

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/syncer"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

const (
	testVersion = "v0.1.0"
	testModule  = "example.com/biblioteca-bairro"
	githubURL   = "https://github.com/exemplo/biblioteca-bairro.git"

	reviewer  = ".claude/agents/code-reviewer.md" // gerenciado, sempre esperado
	dataModel = ".claude/agents/data-model.md"    // gerenciado, módulo data-model
	settings  = ".claude/settings.json"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("PIPELINE_CLAUDE_REQUIRE_GIT") == "1" {
			t.Fatalf("git não encontrado no PATH e PIPELINE_CLAUDE_REQUIRE_GIT=1: %v", err)
		}
		t.Skipf("git não encontrado no PATH: %v", err)
	}
}

// isolateGit desliga a configuração global e de sistema do git e fixa autor e
// committer, como em internal/gitdiff. Usa t.Setenv: os testes deste pacote
// não podem usar t.Parallel.
func isolateGit(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES",
		"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
	}
	env := map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Exemplo",
		"GIT_AUTHOR_EMAIL":    "exemplo@exemplo.invalid",
		"GIT_AUTHOR_DATE":     "2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME":  "Exemplo",
		"GIT_COMMITTER_EMAIL": "exemplo@exemplo.invalid",
		"GIT_COMMITTER_DATE":  "2026-01-01T00:00:00Z",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// gitIn roda um comando git de preparação no projeto de teste.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// baseFiles é o projeto Go fictício mínimo, sem nada do toolkit.
func baseFiles() map[string]string {
	return map[string]string{
		"go.mod":  "module " + testModule + "\n\ngo 1.24\n",
		"main.go": "package main\n\nfunc main() {}\n",
	}
}

// newProject cria um projeto com files (nil vale baseFiles), git init, remote
// origin (vazio: sem remote) e um commit com tudo.
func newProject(t *testing.T, files map[string]string, remote string) string {
	t.Helper()
	requireGit(t)
	isolateGit(t)
	if files == nil {
		files = baseFiles()
	}
	root := t.TempDir()
	for rel, content := range files {
		writeFile(t, root, rel, []byte(content))
	}
	gitIn(t, root, "init", "-q", "-b", "main")
	if remote != "" {
		gitIn(t, root, "remote", "add", "origin", remote)
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "inicial")
	return root
}

// with devolve baseFiles com os arquivos de extra acrescentados.
func with(extra map[string]string) map[string]string {
	m := baseFiles()
	maps.Copy(m, extra)
	return m
}

func opts(root string) Options { return Options{Root: root, ToolkitVersion: testVersion} }

// render devolve o conteúdo do template gerenciado rel com os módulos dados.
func render(t *testing.T, rel string, modules []string) []byte {
	t.Helper()
	for _, f := range templates.Catalog() {
		if f.Path == rel {
			out, err := templates.Render(f, templates.Data{Modules: modules})
			if err != nil {
				t.Fatalf("render %s: %v", rel, err)
			}
			return out
		}
	}
	t.Fatalf("%s fora do catálogo", rel)
	return nil
}

// snapshot devolve todo arquivo sob dir, fora de .git (o git atualiza o
// índice no status), caminho → modo e conteúdo, sem seguir symlink.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == ".git" {
			return filepath.SkipDir
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "-> " + target
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[rel] = info.Mode().String() + " " + string(data)
		case d.IsDir():
			out[rel+"/"] = ""
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshot(t, root)
	if maps.Equal(before, after) {
		return
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("criado: %s", k)
		} else if before[k] != after[k] {
			t.Errorf("alterado: %s", k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			t.Errorf("removido: %s", k)
		}
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink indisponível: %v", err)
	}
}

func itemOf(t *testing.T, a *Adoption, rel string) Item {
	t.Helper()
	for _, it := range a.Items {
		if it.Path == rel {
			return it
		}
	}
	t.Fatalf("%s fora do plano: %+v", rel, a.Items)
	return Item{}
}

func instructionsFor(a *Adoption, rel string) []Instruction {
	var out []Instruction
	for _, in := range a.Instructions {
		if in.Path == rel {
			out = append(out, in)
		}
	}
	return out
}

func mustPlan(t *testing.T, o Options) *Adoption {
	t.Helper()
	a, err := Plan(o)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return a
}

func mustApply(t *testing.T, a *Adoption) Result {
	t.Helper()
	res, err := Apply(a)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

// syncPlan roda o plano do sync sobre o projeto adotado.
func syncPlan(t *testing.T, root string) []syncer.Item {
	t.Helper()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load depois do adopt: %v", err)
	}
	lock, err := manifest.Read(root)
	if err != nil {
		t.Fatalf("manifest.Read depois do adopt: %v", err)
	}
	items, err := syncer.Plan(root, cfg, lock)
	if err != nil {
		t.Fatalf("syncer.Plan: %v", err)
	}
	return items
}

func TestAdoptEmptyProject(t *testing.T) {
	root := newProject(t, nil, githubURL)
	before := snapshot(t, root)
	a := mustPlan(t, opts(root))

	managed := templates.Expected(templates.Managed, nil)
	if len(a.Items) != len(managed)+3 {
		t.Errorf("plano com %d itens, quer %d gerenciados + 3 do projeto", len(a.Items), len(managed))
	}
	for _, f := range managed {
		if it := itemOf(t, a, f.Path); it.Action != Criar {
			t.Errorf("%s: %s, quer criar", f.Path, it.Action)
		}
		if a.Lock.Files[f.Path] != manifest.Hash(render(t, f.Path, nil)) {
			t.Errorf("lock: %s sem o hash do template", f.Path)
		}
	}
	for _, rel := range []string{settings, mcpFile, config.FileName} {
		if it := itemOf(t, a, rel); it.Action != Criar {
			t.Errorf("%s: %s, quer criar", rel, it.Action)
		}
	}
	if len(a.Lock.Released) != 0 || a.Lock.ToolkitVersion != testVersion || a.Lock.Version != manifest.Version {
		t.Errorf("lock = %+v", a.Lock)
	}
	if a.Config == nil || a.Config.GitProvider != "github" || len(a.Config.EnabledModules) != 0 {
		t.Errorf("config = %+v", a.Config)
	}
	assertUnchanged(t, root, before) // Plan não grava

	// Projeto sem Makefile, CLAUDE.md, .gitignore nem core.hooksPath: uma
	// instrução para cada; settings e .mcp.json são criados, sem instrução.
	for _, rel := range []string{"Makefile", "CLAUDE.md", ".gitignore", hooksPathKey} {
		if len(instructionsFor(a, rel)) != 1 {
			t.Errorf("instruções para %s: %+v", rel, instructionsFor(a, rel))
		}
	}
	for _, rel := range []string{settings, mcpFile} {
		if got := instructionsFor(a, rel); len(got) != 0 {
			t.Errorf("instrução para %s criado: %+v", rel, got)
		}
	}

	res := mustApply(t, a)
	want := []string{manifest.FileName, settings, mcpFile, config.FileName}
	for _, f := range managed {
		want = append(want, f.Path)
	}
	slices.Sort(want)
	if !slices.Equal(res.Created, want) {
		t.Errorf("Created = %v, quer %v", res.Created, want)
	}
	for _, f := range managed {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil || !bytes.Equal(got, render(t, f.Path, nil)) {
			t.Errorf("%s gravado diferente do template (%v)", f.Path, err)
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil || info.Mode().Perm() != f.Mode.Perm() {
			t.Errorf("%s: modo %v, quer %v (%v)", f.Path, info.Mode().Perm(), f.Mode.Perm(), err)
		}
	}
	for rel, content := range baseFiles() {
		if got, _ := os.ReadFile(filepath.Join(root, rel)); string(got) != content {
			t.Errorf("%s alterado", rel)
		}
	}
	for _, it := range syncPlan(t, root) {
		if it.Action != syncer.Nada {
			t.Errorf("sync depois do adopt: %s %s (%s)", it.Path, it.Action, it.Reason)
		}
	}
	lock, err := manifest.Read(root)
	if err != nil || !maps.Equal(lock.Files, a.Lock.Files) || lock.ToolkitVersion != testVersion {
		t.Errorf("lock gravado = %+v (%v)", lock, err)
	}
}

func TestAdoptManagedTable(t *testing.T) {
	custom := "---\nname: code-reviewer\ntools: Read, Grep\n---\nRevisor com regras da biblioteca.\n"
	cases := []struct {
		name       string
		content    func(t *testing.T) string
		wantAction Action
		wantFiles  bool
	}{
		{"igual ao template", func(t *testing.T) string { return string(render(t, reviewer, nil)) }, Manter, true},
		{"igual com CRLF (hash TD-23)", func(t *testing.T) string {
			return strings.ReplaceAll(string(render(t, reviewer, nil)), "\n", "\r\n")
		}, Manter, true},
		{"customizado", func(*testing.T) string { return custom }, DoProjeto, false},
		{"vazio", func(*testing.T) string { return "" }, DoProjeto, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content(t)
			root := newProject(t, with(map[string]string{reviewer: content}), githubURL)
			a := mustPlan(t, opts(root))
			if it := itemOf(t, a, reviewer); it.Action != tc.wantAction {
				t.Fatalf("%s: %s, quer %s", reviewer, it.Action, tc.wantAction)
			}
			_, inFiles := a.Lock.Files[reviewer]
			inReleased := slices.Contains(a.Lock.Released, reviewer)
			if inFiles != tc.wantFiles || inReleased == tc.wantFiles {
				t.Errorf("lock: files=%v released=%v", inFiles, inReleased)
			}
			if inFiles && a.Lock.Files[reviewer] != manifest.Hash(render(t, reviewer, nil)) {
				t.Errorf("lock: hash de %s não é o do template", reviewer)
			}
			res := mustApply(t, a)
			if slices.Contains(res.Created, reviewer) {
				t.Errorf("adopt recriou %s", reviewer)
			}
			if got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(reviewer))); string(got) != content {
				t.Errorf("%s alterado pelo adopt", reviewer)
			}
			for _, it := range syncPlan(t, root) {
				switch {
				case it.Path == reviewer && !tc.wantFiles && it.Action != syncer.Liberado:
					t.Errorf("sync: %s %s, quer liberado", it.Path, it.Action)
				case it.Path != reviewer || tc.wantFiles:
					if it.Action != syncer.Nada {
						t.Errorf("sync: %s %s (%s), quer nada", it.Path, it.Action, it.Reason)
					}
				}
			}
		})
	}
}

// Subagente do projeto fora do catálogo não entra no plano nem no lock; nem
// .claude/commands/ nem docs-ai/.
func TestAdoptIgnoresOutsideCatalog(t *testing.T) {
	extra := map[string]string{
		".claude/agents/biblioteca-acervo.md": "---\nname: biblioteca-acervo\ntools: Read\n---\n",
		".claude/commands/emprestimo.md":      "comando do projeto\n",
		"docs-ai/DECISOES.md":                 "# Decisões\n",
	}
	root := newProject(t, with(extra), githubURL)
	a := mustPlan(t, opts(root))
	for _, it := range a.Items {
		if _, ok := extra[it.Path]; ok {
			t.Errorf("%s entrou no plano", it.Path)
		}
	}
	for rel := range extra {
		if _, ok := a.Lock.Files[rel]; ok || slices.Contains(a.Lock.Released, rel) {
			t.Errorf("%s entrou no lock", rel)
		}
	}
	before := snapshot(t, root)
	mustApply(t, a)
	after := snapshot(t, root)
	for rel := range extra {
		k := filepath.FromSlash(rel)
		if before[k] != after[k] {
			t.Errorf("%s alterado", rel)
		}
	}
}

// Enabled_modules sem config e sem --modules: os módulos cujo subagente já
// existe.
func TestAdoptModulesFromAgents(t *testing.T) {
	root := newProject(t, with(map[string]string{dataModel: "subagente do projeto\n"}), githubURL)
	a := mustPlan(t, opts(root))
	if !slices.Equal(a.Config.EnabledModules, []string{"data-model"}) {
		t.Errorf("enabled_modules = %v, quer [data-model]", a.Config.EnabledModules)
	}
	if it := itemOf(t, a, dataModel); it.Action != DoProjeto {
		t.Errorf("%s: %s, quer do-projeto", dataModel, it.Action)
	}

	// --modules explícito vence a detecção.
	root = newProject(t, with(map[string]string{dataModel: "subagente do projeto\n"}), githubURL)
	o := opts(root)
	o.Modules = []string{"release-ops"}
	a = mustPlan(t, o)
	if !slices.Equal(a.Config.EnabledModules, []string{"release-ops"}) {
		t.Errorf("enabled_modules = %v, quer [release-ops]", a.Config.EnabledModules)
	}
	if it := itemOf(t, a, ".claude/agents/release-ops.md"); it.Action != Criar {
		t.Errorf("release-ops: %s, quer criar", it.Action)
	}
	for _, it := range a.Items {
		if it.Path == dataModel {
			t.Errorf("%s no plano sem o módulo ligado", dataModel)
		}
	}
}

const otherHookSettings = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "scripts/guarda-acervo.sh"}]}
    ]
  }
}
`

const fullSettings = `{
  "permissions": {"deny": ["Edit(/.claude/**)", "Edit(/.mcp.json)", "Edit(/.pipeline-claude.json)", "Edit(/.pipeline-claude.lock)", "Bash(rm:*)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Edit|Write|MultiEdit|NotebookEdit", "hooks": [{"type": "command", "command": "\"${CLAUDE_PROJECT_DIR}\"/bin/pipeline-claude hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "bin/pipeline-claude fmt-hook"}]}
    ]
  }
}
`

func TestAdoptSettingsInstructions(t *testing.T) {
	t.Run("outro hook", func(t *testing.T) {
		root := newProject(t, with(map[string]string{settings: otherHookSettings}), githubURL)
		a := mustPlan(t, opts(root))
		if it := itemOf(t, a, settings); it.Action != Manter {
			t.Fatalf("%s: %s, quer manter", settings, it.Action)
		}
		got := instructionsFor(a, settings)
		if len(got) != 3 {
			t.Fatalf("instruções = %+v, quer hook, fmt-hook e deny", got)
		}
		hook, fmtHook, deny := got[0], got[1], got[2]
		if !strings.Contains(hook.Snippet, `"PreToolUse"`) || !strings.Contains(hook.Snippet, `/bin/pipeline-claude hook`) ||
			!strings.Contains(hook.Snippet, `"Edit|Write|MultiEdit|NotebookEdit"`) {
			t.Errorf("trecho do hook = %s", hook.Snippet)
		}
		if !strings.Contains(hook.Reason, "scripts/guarda-acervo.sh") {
			t.Errorf("motivo do hook não menciona o outro hook: %s", hook.Reason)
		}
		if !strings.Contains(fmtHook.Snippet, `"PostToolUse"`) || !strings.Contains(fmtHook.Snippet, `/bin/pipeline-claude fmt-hook`) {
			t.Errorf("trecho do fmt-hook = %s", fmtHook.Snippet)
		}
		for _, rule := range []string{"Edit(/.claude/**)", "Edit(/.mcp.json)", "Edit(/.pipeline-claude.json)", "Edit(/.pipeline-claude.lock)"} {
			if !strings.Contains(deny.Snippet, rule) {
				t.Errorf("trecho do deny sem %s: %s", rule, deny.Snippet)
			}
		}
		res := mustApply(t, a)
		if slices.Contains(res.Created, settings) {
			t.Error("settings.json recriado")
		}
		if got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(settings))); string(got) != otherHookSettings {
			t.Error("settings.json alterado")
		}
	})
	t.Run("completo", func(t *testing.T) {
		root := newProject(t, with(map[string]string{settings: fullSettings}), githubURL)
		a := mustPlan(t, opts(root))
		if got := instructionsFor(a, settings); len(got) != 0 {
			t.Errorf("instruções para settings completo: %+v", got)
		}
	})
	t.Run("deny parcial", func(t *testing.T) {
		partial := strings.Replace(fullSettings, `"Edit(/.mcp.json)", `, "", 1)
		root := newProject(t, with(map[string]string{settings: partial}), githubURL)
		got := instructionsFor(mustPlan(t, opts(root)), settings)
		if len(got) != 1 || !strings.Contains(got[0].Snippet, "Edit(/.mcp.json)") || strings.Contains(got[0].Snippet, "Edit(/.claude/**)") {
			t.Errorf("instruções = %+v, quer só a regra que falta", got)
		}
	})
	t.Run("hook em evento errado", func(t *testing.T) {
		wrong := strings.Replace(fullSettings, `"PreToolUse"`, `"Stop"`, 1)
		root := newProject(t, with(map[string]string{settings: wrong}), githubURL)
		got := instructionsFor(mustPlan(t, opts(root)), settings)
		if len(got) != 1 || !strings.Contains(got[0].Reason, "Stop") {
			t.Errorf("instruções = %+v, quer a do hook citando o evento", got)
		}
	})
	t.Run("ilegível", func(t *testing.T) {
		root := newProject(t, with(map[string]string{settings: "{"}), githubURL)
		before := snapshot(t, root)
		if _, err := Plan(opts(root)); err == nil || !strings.Contains(err.Error(), settings) {
			t.Errorf("Plan com settings ilegível: %v", err)
		}
		assertUnchanged(t, root, before)
	})
}

func TestAdoptProjectInstructions(t *testing.T) {
	makefileOK := "PIPELINE_CLAUDE_VERSION := " + testVersion + "\n\nbuild:\n\tgo build ./...\n"
	cases := []struct {
		name    string
		files   map[string]string
		hooks   string // core.hooksPath; vazio: não configura
		path    string
		want    []string // trechos esperados; nil: sem instrução
		notWant string
	}{
		{"Makefile sem versão", map[string]string{"Makefile": "build:\n\tgo build ./...\n"}, "", "Makefile",
			[]string{"PIPELINE_CLAUDE_VERSION := " + testVersion, "tools:", "cmd/pipeline-claude@$(PIPELINE_CLAUDE_VERSION)"}, ""},
		{"Makefile com versão", map[string]string{"Makefile": makefileOK}, "", "Makefile", nil, ""},
		{"Makefile com outra versão", map[string]string{"Makefile": "PIPELINE_CLAUDE_VERSION := v0.0.9\n"}, "", "Makefile",
			[]string{"PIPELINE_CLAUDE_VERSION := " + testVersion}, ""},
		{"CLAUDE.md sem a seção", map[string]string{"CLAUDE.md": "# Biblioteca do bairro\n"}, "", "CLAUDE.md",
			[]string{"## Como o trabalho acontece aqui", "pm-triage", "Pull Request"}, "# biblioteca"},
		{"CLAUDE.md com a seção", map[string]string{"CLAUDE.md": "# Biblioteca\n\n## Como o trabalho acontece aqui\n\nTexto.\n"}, "", "CLAUDE.md", nil, ""},
		{".gitignore sem bin", map[string]string{".gitignore": "*.log\n"}, "", ".gitignore", []string{"bin/"}, ""},
		{".gitignore com bin/", map[string]string{".gitignore": "*.log\nbin/\n"}, "", ".gitignore", nil, ""},
		{".gitignore com /bin/", map[string]string{".gitignore": "/bin/\n"}, "", ".gitignore", nil, ""},
		{".gitignore com bin/ comentado", map[string]string{".gitignore": "# bin/\n"}, "", ".gitignore", []string{"bin/"}, ""},
		{"hooksPath ausente", nil, "", hooksPathKey, []string{"git config core.hooksPath .githooks"}, ""},
		{"hooksPath outro", nil, ".husky", hooksPathKey, []string{"git config core.hooksPath .githooks"}, ""},
		{"hooksPath certo", nil, ".githooks", hooksPathKey, nil, ""},
		{".mcp.json sem o servidor", map[string]string{mcpFile: `{"mcpServers": {"acervo": {"command": "acervo-mcp"}}}`}, "", mcpFile,
			[]string{`"pipeline-claude"`, `/bin/pipeline-claude`, `"mcp"`}, ""},
		{".mcp.json com o servidor", map[string]string{mcpFile: `{"mcpServers": {"pc": {"command": "bin/pipeline-claude", "args": ["mcp"]}}}`}, "", mcpFile, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newProject(t, with(tc.files), githubURL)
			if tc.hooks != "" {
				gitIn(t, root, "config", "core.hooksPath", tc.hooks)
			}
			a := mustPlan(t, opts(root))
			got := instructionsFor(a, tc.path)
			if tc.want == nil {
				if len(got) != 0 {
					t.Errorf("instrução inesperada: %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("instruções para %s = %+v, quer 1", tc.path, got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got[0].Snippet, w) {
					t.Errorf("trecho sem %q:\n%s", w, got[0].Snippet)
				}
			}
			if tc.notWant != "" && strings.Contains(got[0].Snippet, tc.notWant) {
				t.Errorf("trecho com %q:\n%s", tc.notWant, got[0].Snippet)
			}
			if got[0].Reason == "" {
				t.Error("instrução sem motivo")
			}
		})
	}
}

func TestAdoptExistingConfig(t *testing.T) {
	cfgBody := `{
  "version": 1,
  "project_type": "api",
  "database": "postgres",
  "git_provider": "gitlab",
  "protected_paths": ["internal/"],
  "enabled_modules": ["release-ops"]
}
`
	t.Run("válida é usada", func(t *testing.T) {
		root := newProject(t, with(map[string]string{config.FileName: cfgBody}), "") // sem remote: a config já diz o provedor
		a := mustPlan(t, opts(root))
		if it := itemOf(t, a, config.FileName); it.Action != Manter {
			t.Errorf("config: %s, quer manter", it.Action)
		}
		if a.Config.GitProvider != "gitlab" || !slices.Equal(a.Config.EnabledModules, []string{"release-ops"}) {
			t.Errorf("config = %+v", a.Config)
		}
		if it := itemOf(t, a, ".claude/agents/release-ops.md"); it.Action != Criar {
			t.Errorf("release-ops: %s, quer criar", it.Action)
		}
		res := mustApply(t, a)
		if slices.Contains(res.Created, config.FileName) {
			t.Error("config recriada")
		}
		if got, _ := os.ReadFile(filepath.Join(root, config.FileName)); string(got) != cfgBody {
			t.Error("config alterada")
		}
	})
	t.Run("provedor diferente do argumento", func(t *testing.T) {
		root := newProject(t, with(map[string]string{config.FileName: cfgBody}), "")
		o := opts(root)
		o.Provider = "github"
		if _, err := Plan(o); err == nil || !strings.Contains(err.Error(), "git_provider") {
			t.Errorf("Plan: %v", err)
		}
	})
	t.Run("módulos diferentes do argumento", func(t *testing.T) {
		root := newProject(t, with(map[string]string{config.FileName: cfgBody}), "")
		o := opts(root)
		o.Modules = []string{"data-model"}
		if _, err := Plan(o); err == nil || !strings.Contains(err.Error(), "enabled_modules") {
			t.Errorf("Plan: %v", err)
		}
		o.Modules = []string{"release-ops"} // igual: aceito
		mustPlan(t, o)
	})
}

// TestPlanPreconditions: cada pré-condição do TD-33 reprova com erro próprio e
// nada é criado.
func TestPlanPreconditions(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		remote  string
		setup   func(t *testing.T, root string) Options // nil: opts(root)
		wantErr string
	}{
		{"raiz relativa", nil, githubURL, func(t *testing.T, root string) Options {
			return Options{Root: "biblioteca-bairro", ToolkitVersion: testVersion}
		}, "precisa ser caminho absoluto"},
		{"raiz não limpa", nil, githubURL, func(t *testing.T, root string) Options {
			return Options{Root: root + "/../" + filepath.Base(root), ToolkitVersion: testVersion}
		}, "forma limpa"},
		{"raiz symlink", nil, githubURL, func(t *testing.T, root string) Options {
			link := filepath.Join(t.TempDir(), "atalho")
			symlinkOrSkip(t, root, link)
			return Options{Root: link, ToolkitVersion: testVersion}
		}, "é symlink; informe o diretório real"},
		{"raiz é arquivo", nil, githubURL, func(t *testing.T, root string) Options {
			return Options{Root: filepath.Join(root, "main.go"), ToolkitVersion: testVersion}
		}, "não é diretório"},
		{"raiz inexistente", nil, githubURL, func(t *testing.T, root string) Options {
			return Options{Root: filepath.Join(root, "nada"), ToolkitVersion: testVersion}
		}, "não existe"},
		{"sem go.mod", map[string]string{"main.go": "package main\n"}, githubURL, nil, "não tem go.mod"},
		{"go.mod é diretório", map[string]string{"main.go": "package main\n", "go.mod/x": "x"}, githubURL, nil, "go.mod não é arquivo regular"},
		{"fora do git", nil, githubURL, func(t *testing.T, root string) Options {
			if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
			return opts(root)
		}, "working tree do git"},
		{"working tree sujo", nil, githubURL, func(t *testing.T, root string) Options {
			writeFile(t, root, "novo.go", []byte("package main\n"))
			return opts(root)
		}, "não está limpo"},
		{"arquivo rastreado alterado", nil, githubURL, func(t *testing.T, root string) Options {
			writeFile(t, root, "main.go", []byte("package main\n\nfunc main() { _ = 1 }\n"))
			return opts(root)
		}, "não está limpo"},
		{"lock existe", with(map[string]string{manifest.FileName: "{}\n"}), githubURL, nil, manifest.FileName + " já existe"},
		{"versão inválida", nil, githubURL, func(t *testing.T, root string) Options {
			return Options{Root: root, ToolkitVersion: "dev"}
		}, "versão do toolkit"},
		{"versão vazia", nil, githubURL, func(t *testing.T, root string) Options {
			return Options{Root: root}
		}, "versão do toolkit"},
		{"config inválida", with(map[string]string{config.FileName: `{"version": 1}`}), githubURL, nil, config.FileName + " inválido"},
		{"config é diretório", with(map[string]string{config.FileName + "/x": "x"}), githubURL, nil, config.FileName + " não é arquivo regular"},
		{"sem remote e sem provedor", nil, "", nil, "sem remote origin"},
		{"host desconhecido", nil, "https://git.exemplo.invalid/biblioteca.git", nil, "não é github.com nem GitLab"},
		{"provedor inválido", nil, githubURL, func(t *testing.T, root string) Options {
			o := opts(root)
			o.Provider = "bitbucket"
			return o
		}, `provedor "bitbucket"`},
		{"módulo desconhecido", nil, githubURL, func(t *testing.T, root string) Options {
			o := opts(root)
			o.Modules = []string{"acervo"}
			return o
		}, `"acervo" desconhecido`},
		{"módulo repetido", nil, githubURL, func(t *testing.T, root string) Options {
			o := opts(root)
			o.Modules = []string{"data-model", "data-model"}
			return o
		}, "repetido"},
		{"go.mod sem module", map[string]string{"go.mod": "go 1.24\n"}, githubURL, nil, "sem diretiva module"},
		{"nome não normalizável", map[string]string{"go.mod": "module example.com/_\n"}, githubURL, nil, "à mão"},
		{"subagente gerenciado é diretório", with(map[string]string{reviewer + "/x": "x"}), githubURL, nil, reviewer + " não é arquivo regular"},
		{"subagente gerenciado é symlink", with(map[string]string{"outro.md": "x"}), githubURL, func(t *testing.T, root string) Options {
			symlinkOrSkip(t, "../../outro.md", filepath.Join(root, filepath.FromSlash(reviewer)))
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "link")
			return opts(root)
		}, reviewer + " é symlink"},
		{"área gerenciada por symlink", nil, githubURL, func(t *testing.T, root string) Options {
			symlinkOrSkip(t, t.TempDir(), filepath.Join(root, ".claude", "agents"))
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "link")
			return opts(root)
		}, ".claude/agents é symlink"},
		{".claude por symlink", nil, githubURL, func(t *testing.T, root string) Options {
			symlinkOrSkip(t, t.TempDir(), filepath.Join(root, ".claude"))
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "link")
			return opts(root)
		}, ".claude é symlink"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newProject(t, tc.files, tc.remote)
			o := opts(root)
			if tc.setup != nil {
				o = tc.setup(t, root)
			}
			before := snapshot(t, root)
			a, err := Plan(o)
			if err == nil {
				t.Fatalf("Plan aceitou: %+v", a.Items)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("erro = %v, quer %q", err, tc.wantErr)
			}
			assertUnchanged(t, root, before)
		})
	}
}

func TestPlanProviderFromRemote(t *testing.T) {
	cases := []struct {
		remote string
		want   string
	}{
		{"https://github.com/exemplo/biblioteca-bairro.git", "github"},
		{"git@github.com:exemplo/biblioteca-bairro.git", "github"},
		{"ssh://git@GitHub.com/exemplo/biblioteca-bairro", "github"},
		{"https://gitlab.com/exemplo/biblioteca-bairro.git", "gitlab"},
		{"git@gitlab.com:exemplo/biblioteca-bairro.git", "gitlab"},
		{"https://gitlab.exemplo.invalid/exemplo/biblioteca-bairro.git", "gitlab"},
		{"ssh://git@git.gitlab-interno.invalid:2222/exemplo/biblioteca-bairro.git", "gitlab"},
	}
	for _, tc := range cases {
		t.Run(tc.remote, func(t *testing.T) {
			root := newProject(t, nil, tc.remote)
			a := mustPlan(t, opts(root))
			if a.Config.GitProvider != tc.want {
				t.Errorf("provedor = %q, quer %q", a.Config.GitProvider, tc.want)
			}
		})
	}
	t.Run("argumento vence o remote", func(t *testing.T) {
		root := newProject(t, nil, "https://git.exemplo.invalid/x.git")
		o := opts(root)
		o.Provider = "gitlab"
		if a := mustPlan(t, o); a.Config.GitProvider != "gitlab" {
			t.Errorf("provedor = %q", a.Config.GitProvider)
		}
	})
}

func TestProviderFromURL(t *testing.T) {
	cases := []struct {
		url, want string
	}{
		{"https://github.com/a/b.git", "github"},
		{"https://user:token@github.com/a/b.git", "github"},
		{"http://github.com:443/a/b", "github"},
		{"git@github.com:a/b.git", "github"},
		{"github.com:a/b.git", "github"},
		{"https://gitlab.com/a/b", "gitlab"},
		{"https://code.gitlab.example.invalid/a/b", "gitlab"},
		{"ssh://git@MyGitLab.invalid/a/b", "gitlab"},
		{"https://github.com.evil.invalid/a/b", ""},
		{"https://bitbucket.org/a/b", ""},
		{"/srv/git/biblioteca.git", ""},
		{"file:///srv/git/gitlab/b.git", ""},
		{"../gitlab/b.git", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got, err := providerFromURL(tc.url)
		if tc.want == "" {
			if err == nil {
				t.Errorf("providerFromURL(%q) = %q, quer erro", tc.url, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("providerFromURL(%q) = %q, %v; quer %q", tc.url, got, err, tc.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	cases := []struct {
		module, want string
	}{
		{"example.com/biblioteca-bairro", "biblioteca-bairro"},
		{"biblioteca", "biblioteca"},
		{"example.com/Biblioteca_Bairro", "biblioteca-bairro"},
		{"example.com/biblioteca.bairro", "biblioteca-bairro"},
		{"example.com/biblioteca-bairro/v2", "biblioteca-bairro"},
		{"example.com/acervo--central_", "acervo-central"},
		{"example.com/v2", "v2"},
		{"example.com/2biblioteca", ""},
		{"example.com/_", ""},
		{"example.com/ação", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got, err := normalizeName(tc.module)
		if tc.want == "" {
			if err == nil {
				t.Errorf("normalizeName(%q) = %q, quer erro", tc.module, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("normalizeName(%q) = %q, %v; quer %q", tc.module, got, err, tc.want)
		}
	}
}

func TestModuleFromGoMod(t *testing.T) {
	cases := []struct {
		gomod, want string
	}{
		{"module example.com/biblioteca\n\ngo 1.24\n", "example.com/biblioteca"},
		{"// comentário\nmodule \"example.com/biblioteca\" // fim\n", "example.com/biblioteca"},
		{"\ufeffmodule   example.com/biblioteca\r\n", "example.com/biblioteca"},
		{"go 1.24\n", ""},
		{"module\n", ""},
		{"module a b\n", ""},
		{"module \"a\n", ""},
		{"// module example.com/x\n", ""},
	}
	for _, tc := range cases {
		got, err := moduleFromGoMod([]byte(tc.gomod))
		if tc.want == "" {
			if err == nil {
				t.Errorf("moduleFromGoMod(%q) = %q, quer erro", tc.gomod, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("moduleFromGoMod(%q) = %q, %v; quer %q", tc.gomod, got, err, tc.want)
		}
	}
}

// failingDeps faz a n-ésima escrita (arquivo, config ou lock, nessa ordem)
// falhar; as anteriores gravam de verdade.
func failingDeps(n int, writes *[]string) deps {
	d := defaultDeps()
	count := 0
	write := d.writeFile
	d.writeFile = func(name string, data []byte, mode fs.FileMode) error {
		count++
		*writes = append(*writes, name)
		if count == n {
			return errors.New("falha injetada")
		}
		return write(name, data, mode)
	}
	lock := d.writeLock
	d.writeLock = func(root string, l *manifest.Lock) error {
		count++
		*writes = append(*writes, manifest.FileName)
		if count == n {
			return errors.New("falha injetada no lock")
		}
		return lock(root, l)
	}
	return d
}

func TestApplyFailureRollsBack(t *testing.T) {
	// Um subagente igual ao template, um customizado e um settings.json existem
	// antes: nunca são removidos nem alterados.
	files := func(t *testing.T) map[string]string {
		m := with(map[string]string{dataModel: "subagente do projeto\n", settings: otherHookSettings})
		m[reviewer] = string(render(t, reviewer, []string{"data-model"}))
		return m
	}
	a0 := mustPlan(t, opts(newProject(t, files(t), githubURL)))
	total := 1 // lock
	for _, it := range a0.Items {
		if it.Action == Criar {
			total++
		}
	}
	if total < 5 {
		t.Fatalf("poucas escritas no plano: %d", total)
	}
	for n := 1; n <= total; n++ {
		t.Run(fmt.Sprintf("escrita %d", n), func(t *testing.T) {
			root := newProject(t, files(t), githubURL)
			before := snapshot(t, root)
			a := mustPlan(t, opts(root))
			var writes []string
			_, err := applyWith(a, failingDeps(n, &writes))
			if err == nil {
				t.Fatalf("Apply sem erro com falha na escrita %d de %d", n, total)
			}
			if !strings.Contains(err.Error(), "falha injetada") {
				t.Errorf("erro sem a causa: %v", err)
			}
			if n > 1 && !strings.Contains(err.Error(), "removido") {
				t.Errorf("erro sem a lista do que foi removido: %v", err)
			}
			assertUnchanged(t, root, before)
		})
	}
}

// TestApplyOrder: arquivos, depois a config, por último o lock.
func TestApplyOrder(t *testing.T) {
	root := newProject(t, nil, githubURL)
	a := mustPlan(t, opts(root))
	var writes []string
	if _, err := applyWith(a, failingDeps(-1, &writes)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	n := len(writes)
	if n < 3 || writes[n-1] != manifest.FileName || filepath.Base(writes[n-2]) != config.FileName {
		t.Errorf("ordem das escritas: %v", writes)
	}
}

func TestApplyDiskChanged(t *testing.T) {
	cases := []struct {
		name    string
		change  func(t *testing.T, root string)
		wantErr string
	}{
		{"arquivo apareceu num criar", func(t *testing.T, root string) {
			writeFile(t, root, ".claude/skills/fluxo/SKILL.md", []byte("do projeto\n"))
		}, ".claude/skills/fluxo/SKILL.md"},
		{"config apareceu", func(t *testing.T, root string) {
			writeFile(t, root, config.FileName, []byte("{}\n"))
		}, config.FileName},
		{"lock apareceu", func(t *testing.T, root string) {
			writeFile(t, root, manifest.FileName, []byte("{}\n"))
		}, manifest.FileName},
		{"subagente mantido foi editado", func(t *testing.T, root string) {
			writeFile(t, root, reviewer, []byte("editado depois do plano\n"))
		}, reviewer},
		{"subagente do projeto foi apagado", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(dataModel))); err != nil {
				t.Fatal(err)
			}
		}, dataModel},
		{"diretório virou symlink", func(t *testing.T, root string) {
			symlinkOrSkip(t, t.TempDir(), filepath.Join(root, ".claude", "skills"))
		}, ".claude/skills"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := with(map[string]string{dataModel: "subagente do projeto\n"})
			files[reviewer] = string(render(t, reviewer, []string{"data-model"}))
			root := newProject(t, files, githubURL)
			a := mustPlan(t, opts(root))
			tc.change(t, root)
			before := snapshot(t, root)
			if _, err := Apply(a); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Apply: erro %v, quer %q", err, tc.wantErr)
			}
			assertUnchanged(t, root, before)
		})
	}
}

// TestApplyRaceDuringWrites: o disco muda entre a conferência do plano e as
// escritas (algo aparece no caminho de um criar seguinte, ou o lock aparece).
// Apply para, remove o que criou e não toca no que apareceu.
func TestApplyRaceDuringWrites(t *testing.T) {
	cases := []struct {
		name     string
		intruder string
		wantErr  string
	}{
		{"arquivo num criar seguinte", ".githooks/pre-push", ".githooks/pre-push apareceu no disco"},
		{"lock", manifest.FileName, manifest.FileName + " já existe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newProject(t, nil, githubURL)
			before := snapshot(t, root)
			a := mustPlan(t, opts(root))
			d := defaultDeps()
			calls := 0
			d.writeFile = func(name string, data []byte, mode fs.FileMode) error {
				calls++
				if calls == 1 {
					writeFile(t, root, tc.intruder, []byte("apareceu\n"))
				}
				return createFile(name, data, mode)
			}
			_, err := applyWith(a, d)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "removido") {
				t.Fatalf("Apply: %v, quer %q e a lista do removido", err, tc.wantErr)
			}
			intruder := filepath.Join(root, filepath.FromSlash(tc.intruder))
			if got, err := os.ReadFile(intruder); err != nil || string(got) != "apareceu\n" {
				t.Fatalf("o que apareceu foi tocado: %q, %v", got, err)
			}
			// Tirado o que apareceu (e o diretório que veio com ele), o
			// projeto está como antes do Apply.
			if err := os.Remove(intruder); err != nil {
				t.Fatal(err)
			}
			if dir := filepath.Dir(intruder); dir != root {
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
			}
			assertUnchanged(t, root, before)
		})
	}
}

func TestMkdirsRefusesSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(root, ".claude"))
	created, err := mkdirs(root, ".claude/agents")
	if err == nil || !strings.Contains(err.Error(), "é symlink") || len(created) != 0 {
		t.Errorf("mkdirs através de symlink: %v, criou %v", err, created)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("mkdirs criou fora da raiz: %v", entries)
	}
	writeFile(t, root, "arquivo", []byte("x"))
	if _, err := mkdirs(root, "arquivo/dentro"); err == nil {
		t.Error("mkdirs através de arquivo")
	}
	created, err = mkdirs(root, "a/b")
	if err != nil || len(created) != 2 {
		t.Errorf("mkdirs a/b: %v, %v", created, err)
	}
}

func TestApplyRejectsForeignPlan(t *testing.T) {
	root := newProject(t, nil, githubURL)
	if _, err := Apply(nil); err == nil {
		t.Error("Apply(nil) sem erro")
	}
	a := mustPlan(t, opts(root))
	before := snapshot(t, root)
	forged := *a
	forged.Items = append(slices.Clone(a.Items), Item{Path: "cmd/fora.go", Action: Criar})
	if _, err := Apply(&forged); err == nil {
		t.Error("Apply aceitou item que não veio do Plan")
	}
	forged = *a
	forged.Lock = nil
	if _, err := Apply(&forged); err == nil {
		t.Error("Apply aceitou plano sem lock")
	}
	forged = *a
	forged.Root = t.TempDir()
	if _, err := Apply(&forged); err == nil {
		t.Error("Apply aceitou plano com outra raiz")
	}
	assertUnchanged(t, root, before)
}

func TestCreateFileExclusive(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a.md")
	if err := createFile(name, []byte("novo"), 0o755); err != nil {
		t.Fatalf("createFile: %v", err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("modo = %v (%v), quer 0755", info.Mode().Perm(), err)
	}
	if err := createFile(name, []byte("outro"), 0o644); !errors.Is(err, fs.ErrExist) {
		t.Errorf("createFile sobre existente: %v, quer ErrExist", err)
	}
	if got, _ := os.ReadFile(name); string(got) != "novo" {
		t.Errorf("conteúdo sobrescrito: %q", got)
	}
	link := filepath.Join(dir, "link.md")
	symlinkOrSkip(t, filepath.Join(dir, "alvo.md"), link)
	if err := createFile(link, []byte("x"), 0o644); err == nil {
		t.Error("createFile seguiu symlink pendente")
	}
	if _, err := os.Lstat(filepath.Join(dir, "alvo.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("createFile criou o alvo do symlink")
	}
}

// fakeGit responde como um repositório limpo com remote no GitHub e sem
// core.hooksPath.
func fakeGit(_ string, args ...string) (gitResult, error) {
	switch strings.Join(args, " ") {
	case "rev-parse --is-inside-work-tree":
		return gitResult{stdout: "true\n"}, nil
	case "status --porcelain":
		return gitResult{}, nil
	case "config --get remote.origin.url":
		return gitResult{stdout: githubURL + "\n"}, nil
	case "config --get core.hooksPath":
		return gitResult{code: 1}, nil
	}
	return gitResult{code: 128, stderr: "comando inesperado"}, nil
}

// FuzzPlan: para árvores arbitrárias (inclusive na área gerenciada), Plan
// nunca entra em pânico, nunca propõe caminho fora da raiz e nunca marca
// criar em caminho existente.
func FuzzPlan(f *testing.F) {
	f.Add(reviewer+"\n"+dataModel, "", []byte("x"), uint8(0))
	f.Add(".claude/agents/biblioteca.md\n.githooks/pre-commit", ".claude/skills", []byte{}, uint8(1))
	f.Add("../fora.md\n.claude/agents", reviewer, []byte("\r\n"), uint8(255))
	f.Add(settings+"\n"+mcpFile, ".githooks", []byte(otherHookSettings), uint8(3))
	f.Add(".claude/skills/fluxo/SKILL.md/x", ".claude/agents/data-model.md", []byte("{}"), uint8(2))
	f.Fuzz(func(t *testing.T, diskPaths, links string, content []byte, mods uint8) {
		root := t.TempDir()
		writeFile(t, root, "go.mod", []byte("module "+testModule+"\n"))
		for _, p := range strings.Split(diskPaths, "\n") {
			if !filepath.IsLocal(p) || strings.ContainsRune(p, 0) || p == "go.mod" {
				continue
			}
			full := filepath.Join(root, p)
			if os.MkdirAll(filepath.Dir(full), 0o755) != nil {
				continue
			}
			_ = os.WriteFile(full, content, 0o644)
		}
		outside := t.TempDir()
		for _, p := range strings.Split(links, "\n") {
			if !filepath.IsLocal(p) || strings.ContainsRune(p, 0) {
				continue
			}
			full := filepath.Join(root, p)
			if os.MkdirAll(filepath.Dir(full), 0o755) != nil {
				continue
			}
			_ = os.Symlink(outside, full)
		}
		all := slices.Sorted(maps.Keys(config.Modules))
		var modules []string
		for i, m := range all {
			if mods&(1<<i) != 0 {
				modules = append(modules, m)
			}
		}
		a, err := planWith(Options{Root: root, ToolkitVersion: testVersion, Modules: modules}, deps{git: fakeGit})
		if err != nil {
			return
		}
		for _, it := range a.Items {
			if path.Clean(it.Path) != it.Path || !filepath.IsLocal(filepath.FromSlash(it.Path)) {
				t.Fatalf("plano propôs %q fora da raiz", it.Path)
			}
			if it.Action != Criar {
				continue
			}
			cur := root
			for _, part := range strings.Split(it.Path, "/") {
				cur = filepath.Join(cur, part)
				info, err := os.Lstat(cur)
				if errors.Is(err, fs.ErrNotExist) {
					break
				}
				if err != nil || info.Mode()&fs.ModeSymlink != 0 || cur == filepath.Join(root, filepath.FromSlash(it.Path)) {
					t.Fatalf("plano marcou criar em %q, que existe ou passa por symlink", it.Path)
				}
			}
		}
		for p := range a.Lock.Files {
			if !manifest.ManagedArea(p) {
				t.Fatalf("lock com %q fora da área gerenciada", p)
			}
		}
		for _, p := range a.Lock.Released {
			if !manifest.ManagedArea(p) {
				t.Fatalf("released com %q fora da área gerenciada", p)
			}
		}
	})
}

// TestAdoptNoCI: o adopt não cria CI em projeto existente, em nenhum provedor;
// o CI só nasce com o init (o projeto adotado já tem o seu).
func TestAdoptNoCI(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			root := newProject(t, nil, githubURL)
			o := opts(root)
			o.Provider = provider
			a := mustPlan(t, o)
			for _, it := range a.Items {
				if strings.HasPrefix(it.Path, ".github/") || strings.HasPrefix(it.Path, ".gitlab") {
					t.Errorf("plano com CI %s (%s)", it.Path, it.Action)
				}
			}
			res := mustApply(t, a)
			for _, rel := range res.Created {
				if strings.HasPrefix(rel, ".github/") || strings.HasPrefix(rel, ".gitlab") {
					t.Errorf("adopt criou %s", rel)
				}
			}
			for _, rel := range []string{".github", ".gitlab-ci.yml"} {
				if _, err := os.Lstat(filepath.Join(root, rel)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s existe depois do adopt: %v", rel, err)
				}
			}
		})
	}
}
