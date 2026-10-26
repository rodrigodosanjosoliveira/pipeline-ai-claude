package fmthook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// fixturesDir é a raiz das fixtures reais do Claude Code.
const fixturesDir = "../../testdata/hooks/claude"

// fixtureRoot é a raiz do projeto nos payloads sanitizados; o teste a troca
// pela raiz temporária.
const fixtureRoot = "/workspace/exemplo"

const testConfig = `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "protected_paths": ["internal/", "docs-ai/"],
 "no_agent_paths": ["docs-ai/restrito/"],
 "editor_agents": ["dev-implementation"]}`

const (
	unformatted = "package exemplo\nfunc  Soma(a,b int)int{return a+b}\n"
	formatted   = "package exemplo\n\nfunc Soma(a, b int) int { return a + b }\n"
	broken      = "package exemplo\nfunc Soma(a, b int int {\n"
	markdown    = "#  título\n\n*  item\n"
)

func newProject(t testing.TB, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(testConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return dir
}

// loadFixture lê uma fixture e troca a raiz sanitizada por root.
func loadFixture(t testing.TB, rel, root string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixturesDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	quoted, err := json.Marshal(filepath.ToSlash(root))
	if err != nil {
		t.Fatalf("marshal root: %v", err)
	}
	raw = bytes.ReplaceAll(raw, []byte(fixtureRoot), bytes.Trim(quoted, `"`))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
	return m
}

func marshal(t testing.TB, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func toolInput(m map[string]any) map[string]any {
	return m["tool_input"].(map[string]any)
}

func getwdErr() (string, error) { return "", errors.New("getwd indisponível") }

func run(t *testing.T, payload []byte) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := Run(bytes.NewReader(payload), &stderr, getwdErr)
	return code, stderr.String()
}

func writeFile(t testing.TB, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink indisponível: %v", err)
	}
}

// fixtureCase prepara o projeto para uma fixture e confere o resultado.
type fixtureCase struct {
	name  string
	file  string // relativo à raiz; vazio quando não há arquivo a preparar
	setup string // conteúdo inicial
	code  int
	want  string // conteúdo esperado depois; vazio = intacto
}

// fixtureCases declara, para cada fixture real, os cenários e a decisão
// esperada. Uma fixture nova em testdata/hooks/claude sem entrada aqui reprova
// (TestFixturesTodasDeclaradas).
var fixtureCases = map[string][]fixtureCase{
	"2.1.309/posttooluse-edit-main.json": {
		{name: "desformatado", file: "internal/exemplo/alvo.go", setup: unformatted, code: ExitOK, want: formatted},
		{name: "já formatado", file: "internal/exemplo/alvo.go", setup: formatted, code: ExitOK},
		{name: "erro de sintaxe", file: "internal/exemplo/alvo.go", setup: broken, code: ExitBlock},
	},
	"2.1.309/posttooluse-write-dev-implementation.json": {
		{name: "desformatado", file: "internal/exemplo/novo.go", setup: unformatted, code: ExitOK, want: formatted},
	},
	"2.1.309/posttooluse-write-main.json": {
		{name: "markdown", file: "docs-ai/exemplo.md", setup: markdown, code: ExitOK},
	},
	"2.1.309/posttooluse-write-pm-triage.json": {
		{name: "markdown", file: "docs-ai/exemplo-triage.md", setup: markdown, code: ExitOK},
	},
	"2.1.309/posttooluse-notebookedit-main.json": {
		{name: "notebook", code: ExitOK},
	},
	"2.1.309/pretooluse-edit-main.json": {
		{name: "evento errado", file: "internal/exemplo/alvo.go", setup: unformatted, code: ExitBlock},
	},
	"2.1.309/pretooluse-notebookedit-main.json":        {{name: "evento errado", code: ExitBlock}},
	"2.1.309/pretooluse-write-dev-implementation.json": {{name: "evento errado", code: ExitBlock}},
	"2.1.309/pretooluse-write-main.json":               {{name: "evento errado", code: ExitBlock}},
	"2.1.309/pretooluse-write-pm-triage.json":          {{name: "evento errado", code: ExitBlock}},
}

func TestFixturesReais(t *testing.T) {
	for rel, cases := range fixtureCases {
		for _, tc := range cases {
			t.Run(rel+"/"+tc.name, func(t *testing.T) {
				root := newProject(t, t.TempDir())
				var path string
				var before time.Time
				if tc.file != "" {
					path = filepath.Join(root, filepath.FromSlash(tc.file))
					writeFile(t, path, tc.setup, 0o644)
					// mtime no passado para detectar regravação.
					before = time.Now().Add(-time.Hour).Truncate(time.Second)
					if err := os.Chtimes(path, before, before); err != nil {
						t.Fatal(err)
					}
				}
				code, stderr := run(t, marshal(t, loadFixture(t, rel, root)))
				if code != tc.code {
					t.Fatalf("código = %d, quer %d; stderr=%q", code, tc.code, stderr)
				}
				if path == "" {
					return
				}
				got := readFile(t, path)
				if tc.want == "" {
					if got != tc.setup {
						t.Errorf("arquivo alterado: %q", got)
					}
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if !info.ModTime().Equal(before) {
						t.Errorf("mtime mudou: %v, quer %v", info.ModTime(), before)
					}
					return
				}
				if got != tc.want {
					t.Errorf("conteúdo = %q, quer %q", got, tc.want)
				}
				if !strings.Contains(stderr, "formatado "+tc.file) {
					t.Errorf("stderr sem aviso de formatação: %q", stderr)
				}
			})
		}
	}
}

func TestFixturesTodasDeclaradas(t *testing.T) {
	found := 0
	err := filepath.WalkDir(fixturesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(fixturesDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		found++
		if _, ok := fixtureCases[rel]; !ok {
			t.Errorf("fixture %s sem decisão declarada em fixtureCases", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found != len(fixtureCases) {
		t.Errorf("fixtures encontradas = %d, declaradas = %d", found, len(fixtureCases))
	}
}

func TestErroDeSintaxeAponta(t *testing.T) {
	root := newProject(t, t.TempDir())
	path := filepath.Join(root, "internal", "exemplo", "alvo.go")
	writeFile(t, path, broken, 0o644)
	code, stderr := run(t, marshal(t, loadFixture(t, "2.1.309/posttooluse-edit-main.json", root)))
	if code != ExitBlock {
		t.Fatalf("código = %d", code)
	}
	if !strings.Contains(stderr, "internal/exemplo/alvo.go:2:") {
		t.Errorf("stderr sem arquivo:linha:coluna: %q", stderr)
	}
}

func TestEventoErradoApontaDoctor(t *testing.T) {
	root := newProject(t, t.TempDir())
	_, stderr := run(t, marshal(t, loadFixture(t, "2.1.309/pretooluse-edit-main.json", root)))
	if !strings.Contains(stderr, "PreToolUse") || !strings.Contains(stderr, "doctor") {
		t.Errorf("stderr = %q", stderr)
	}
}

// edit monta um payload PostToolUse derivado da fixture de Edit.
func edit(t *testing.T, root, tool, target string) []byte {
	t.Helper()
	m := loadFixture(t, "2.1.309/posttooluse-edit-main.json", root)
	m["tool_name"] = tool
	toolInput(m)["file_path"] = target
	return marshal(t, m)
}

func TestCasosDerivados(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		file   string // arquivo preparado, relativo à raiz
		target string // caminho no payload; vazio = file absoluto
		setup  string
		code   int
		want   string // vazio = intacto
	}{
		{name: "MultiEdit em .go", tool: "MultiEdit", file: "internal/x.go", setup: unformatted, code: ExitOK, want: formatted},
		{name: ".go em no_agent_paths", tool: "Edit", file: "docs-ai/restrito/x.go", setup: unformatted, code: ExitOK},
		{name: ".go em no_agent_paths, caixa diferente", tool: "Edit", file: "Docs-AI/Restrito/x.go", setup: unformatted, code: ExitOK},
		{name: "extensão .GO", tool: "Edit", file: "internal/x.GO", setup: unformatted, code: ExitOK, want: formatted},
		{name: "fora de caminho protegido", tool: "Write", file: "cmd/x.go", setup: unformatted, code: ExitOK, want: formatted},
		{name: "caminho relativo contra a raiz", tool: "Edit", file: "internal/x.go", target: "internal/x.go", setup: unformatted, code: ExitOK, want: formatted},
		{name: "arquivo inexistente", tool: "Edit", target: "internal/sumiu.go", code: ExitOK},
		{name: "NotebookEdit em .go", tool: "NotebookEdit", file: "internal/x.go", setup: unformatted, code: ExitOK},
		{name: "Read", tool: "Read", file: "internal/x.go", setup: unformatted, code: ExitOK},
		{name: "caminho vazio", tool: "Edit", target: "", code: ExitBlock},
		{name: "fora da raiz", tool: "Edit", target: "../outro/x.go", code: ExitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newProject(t, t.TempDir())
			var path string
			if tt.file != "" {
				path = filepath.Join(root, filepath.FromSlash(tt.file))
				writeFile(t, path, tt.setup, 0o644)
			}
			target := tt.target
			if target == "" && tt.file != "" {
				target = path
			}
			code, stderr := run(t, edit(t, root, tt.tool, target))
			if code != tt.code {
				t.Fatalf("código = %d, quer %d; stderr=%q", code, tt.code, stderr)
			}
			if path == "" {
				return
			}
			want := tt.want
			if want == "" {
				want = tt.setup
			}
			if got := readFile(t, path); got != want {
				t.Errorf("conteúdo = %q, quer %q", got, want)
			}
		})
	}
}

func TestPayloadInvalido(t *testing.T) {
	for _, payload := range []string{"", "null", "[]", `"x"`, `{"tool_name":`, `{"tool_name":"Edit"} x`,
		`{"hook_event_name":"PostToolUse","tool_name":7}`,
		`{"hook_event_name":"PostToolUse"}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit"}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":[]}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":3}}`,
		`{"hook_event_name":"Stop","tool_name":"Edit","tool_input":{"file_path":"x.go"}}`,
		`{"hook_event_name":7,"tool_name":"Edit","tool_input":{"file_path":"x.go"}}`,
	} {
		if code, _ := run(t, []byte(payload)); code != ExitBlock {
			t.Errorf("payload %q: código = %d, quer %d", payload, code, ExitBlock)
		}
	}
}

func TestConfiguracao(t *testing.T) {
	t.Run("ausente", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "x.go")
		writeFile(t, path, unformatted, 0o644)
		code, stderr := run(t, edit(t, root, "Edit", path))
		if code != ExitBlock || !strings.Contains(stderr, "doctor") {
			t.Errorf("código = %d, stderr=%q", code, stderr)
		}
		if readFile(t, path) != unformatted {
			t.Error("arquivo alterado sem configuração")
		}
	})
	t.Run("inválida", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, config.FileName), `{"version": 2}`, 0o644)
		if code, _ := run(t, edit(t, root, "Edit", filepath.Join(root, "x.go"))); code != ExitBlock {
			t.Errorf("código = %d", code)
		}
	})
	t.Run("getwd quando sem cwd", func(t *testing.T) {
		root := newProject(t, t.TempDir())
		path := filepath.Join(root, "x.go")
		writeFile(t, path, unformatted, 0o644)
		m := loadFixture(t, "2.1.309/posttooluse-edit-main.json", root)
		delete(m, "cwd")
		toolInput(m)["file_path"] = path
		var stderr bytes.Buffer
		code := Run(bytes.NewReader(marshal(t, m)), &stderr, func() (string, error) { return root, nil })
		if code != ExitOK || readFile(t, path) != formatted {
			t.Errorf("código = %d, stderr=%q", code, stderr.String())
		}
		if code := Run(bytes.NewReader(marshal(t, m)), &stderr, getwdErr); code != ExitBlock {
			t.Errorf("getwd com erro: código = %d", code)
		}
	})
}

func TestPermissaoPreservada(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissões POSIX")
	}
	root := newProject(t, t.TempDir())
	path := filepath.Join(root, "internal", "x.go")
	writeFile(t, path, unformatted, 0o600)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if code, stderr := run(t, edit(t, root, "Edit", path)); code != ExitOK {
		t.Fatalf("código = %d, stderr=%q", code, stderr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("permissão = %v, quer 0640", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("sobrou arquivo temporário: %v", entries)
	}
}

func TestNaoRegular(t *testing.T) {
	root := newProject(t, t.TempDir())
	dir := filepath.Join(root, "internal", "x.go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(t, edit(t, root, "Edit", dir)); code != ExitBlock {
		t.Errorf("diretório x.go: código = %d, quer %d", code, ExitBlock)
	}
}

func TestSymlinks(t *testing.T) {
	t.Run("para .go dentro da raiz formata o alvo", func(t *testing.T) {
		root := newProject(t, t.TempDir())
		real := filepath.Join(root, "internal", "real.go")
		writeFile(t, real, unformatted, 0o644)
		link := filepath.Join(root, "atalho.go")
		symlinkOrSkip(t, real, link)
		if code, stderr := run(t, edit(t, root, "Edit", link)); code != ExitOK {
			t.Fatalf("código = %d, stderr=%q", code, stderr)
		}
		if got := readFile(t, real); got != formatted {
			t.Errorf("alvo = %q", got)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("link substituído: %v, %v", info, err)
		}
	})
	t.Run("para fora da raiz fica intacto", func(t *testing.T) {
		root, outside := newProject(t, t.TempDir()), t.TempDir()
		real := filepath.Join(outside, "fora.go")
		writeFile(t, real, unformatted, 0o644)
		link := filepath.Join(root, "fora.go")
		symlinkOrSkip(t, real, link)
		if code, stderr := run(t, edit(t, root, "Edit", link)); code != ExitOK {
			t.Fatalf("código = %d, stderr=%q", code, stderr)
		}
		if got := readFile(t, real); got != unformatted {
			t.Errorf("arquivo fora da raiz alterado: %q", got)
		}
	})
	t.Run("diretório por link para no_agent_paths", func(t *testing.T) {
		root := newProject(t, t.TempDir())
		real := filepath.Join(root, "docs-ai", "restrito", "x.go")
		writeFile(t, real, unformatted, 0o644)
		symlinkOrSkip(t, filepath.Join(root, "docs-ai", "restrito"), filepath.Join(root, "atalho"))
		if code, _ := run(t, edit(t, root, "Edit", filepath.Join(root, "atalho", "x.go"))); code != ExitOK {
			t.Fatalf("código = %d", code)
		}
		if got := readFile(t, real); got != unformatted {
			t.Errorf("arquivo em no_agent_paths alterado: %q", got)
		}
	})
}

var fuzzTools = []string{"Edit", "Write", "MultiEdit"}

func FuzzRun(f *testing.F) {
	base := f.TempDir()
	if err := os.Mkdir(filepath.Join(base, "proj"), 0o755); err != nil {
		f.Fatal(err)
	}
	root := newProject(f, filepath.Join(base, "proj"))
	outside := filepath.Join(base, "fora")
	outsideFile := filepath.Join(outside, "fora.go")
	writeFile(f, outsideFile, unformatted, 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "fora")); err != nil {
		f.Logf("symlink indisponível: %v", err)
	}

	entries, err := filepath.Glob(filepath.Join(fixturesDir, "*", "*.json"))
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range entries {
		rel, _ := filepath.Rel(fixturesDir, p)
		f.Add(marshal(f, loadFixture(f, filepath.ToSlash(rel), root)))
	}
	for _, tool := range fuzzTools {
		for _, target := range []string{outsideFile, "../fora/fora.go", "fora/fora.go", "internal/x.go"} {
			f.Add([]byte(`{"hook_event_name":"PostToolUse","tool_name":"` + tool + `","tool_input":{"file_path":` + quote(target) + `}}`))
		}
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"tool_name":"Read"}`))

	getwd := func() (string, error) { return root, nil }
	f.Fuzz(func(t *testing.T, data []byte) {
		var stderr bytes.Buffer
		code := Run(bytes.NewReader(data), &stderr, getwd)
		if code != ExitOK && code != ExitBlock {
			t.Fatalf("código %d fora de {0, 2}", code)
		}
		if got := readFile(t, outsideFile); got != unformatted {
			t.Fatalf("gravou fora da raiz com %q", data)
		}
		if code != ExitOK {
			return
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil || m == nil {
			t.Fatalf("devolveu 0 para payload que não é objeto JSON: %q", data)
		}
		if ev, ok := m["hook_event_name"]; ok && ev != "PostToolUse" {
			t.Fatalf("devolveu 0 para evento %v", ev)
		}
	})
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
