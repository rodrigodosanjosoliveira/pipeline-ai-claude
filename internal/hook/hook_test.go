package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// newProject cria um projeto temporário com a configuração de teste e devolve
// a raiz.
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

func run(t *testing.T, payload []byte, getwd func() (string, error)) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := Run(bytes.NewReader(payload), &stderr, getwd)
	return code, stderr.String()
}

// fixtureCases declara a decisão esperada para cada fixture real. Uma fixture
// nova em testdata/hooks/claude sem linha aqui reprova
// TestFixturesTodasDeclaradas.
var fixtureCases = map[string]int{
	// Sessão principal bloqueada em internal/ (TD-10).
	"2.1.309/pretooluse-edit-main.json": ExitBlock,
	// Idem em docs-ai/.
	"2.1.309/pretooluse-write-main.json": ExitBlock,
	// notebook_path é lido; no pipeline-ai esse caso passa sem checagem (TD-09).
	"2.1.309/pretooluse-notebookedit-main.json": ExitBlock,
	// Editor liberado por agent_type.
	"2.1.309/pretooluse-write-dev-implementation.json": ExitAllow,
	// Agente não editor bloqueado.
	"2.1.309/pretooluse-write-pm-triage.json": ExitBlock,
	// PostToolUse: hook registrado no evento errado (TD-08, regra 1a).
	"2.1.309/posttooluse-edit-main.json":                ExitBlock,
	"2.1.309/posttooluse-notebookedit-main.json":        ExitBlock,
	"2.1.309/posttooluse-write-dev-implementation.json": ExitBlock,
	"2.1.309/posttooluse-write-main.json":               ExitBlock,
	"2.1.309/posttooluse-write-pm-triage.json":          ExitBlock,
}

func TestFixturesReais(t *testing.T) {
	for rel, want := range fixtureCases {
		t.Run(rel, func(t *testing.T) {
			root := newProject(t, t.TempDir())
			code, msg := run(t, marshal(t, loadFixture(t, rel, root)), getwdErr)
			if code != want {
				t.Errorf("Run = %d, quer %d; stderr:\n%s", code, want, msg)
			}
			if code == ExitBlock && msg == "" {
				t.Error("bloqueio sem mensagem em stderr")
			}
		})
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
		t.Fatalf("walk: %v", err)
	}
	if found != len(fixtureCases) {
		t.Errorf("fixtures encontradas = %d, declaradas = %d", found, len(fixtureCases))
	}
}

func TestCasosDerivados(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		apply func(m map[string]any, root string)
		want  int
	}{
		{
			name: "Edit por dev-implementation (derivado de edit-main + agent_type de write-dev-implementation)",
			base: "2.1.309/pretooluse-edit-main.json",
			apply: func(m map[string]any, _ string) {
				m["agent_type"] = "dev-implementation"
			},
			want: ExitAllow,
		},
		{
			name: "dev-implementation em docs-ai/restrito (derivado): no_agent_paths vale para editor",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, root string) {
				toolInput(m)["file_path"] = root + "/docs-ai/restrito/x.md"
			},
			want: ExitBlock,
		},
		{
			name: "sintético: MultiEdit pela sessão principal em internal/",
			base: "2.1.309/pretooluse-edit-main.json",
			apply: func(m map[string]any, _ string) {
				m["tool_name"] = "MultiEdit"
			},
			want: ExitBlock,
		},
		{
			name: "sessão principal fora de caminho protegido",
			base: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, root string) {
				toolInput(m)["file_path"] = root + "/README.md"
			},
			want: ExitAllow,
		},
		{
			name: "ferramenta de leitura",
			base: "2.1.309/pretooluse-edit-main.json",
			apply: func(m map[string]any, _ string) {
				m["tool_name"] = "Read"
			},
			want: ExitAllow,
		},
		{
			name: "hook_event_name ausente segue a decisão normal",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				delete(m, "hook_event_name")
			},
			want: ExitAllow,
		},
		{
			name: "hook_event_name vazio é evento errado",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				m["hook_event_name"] = ""
			},
			want: ExitBlock,
		},
		{
			name: "caminho ausente",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				delete(toolInput(m), "file_path")
			},
			want: ExitBlock,
		},
		{
			name: "caminho vazio",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				toolInput(m)["file_path"] = ""
			},
			want: ExitBlock,
		},
		{
			name: "tool_input ausente",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				delete(m, "tool_input")
			},
			want: ExitBlock,
		},
		{
			name: "NotebookEdit só com file_path",
			base: "2.1.309/pretooluse-notebookedit-main.json",
			apply: func(m map[string]any, root string) {
				in := toolInput(m)
				delete(in, "notebook_path")
				in["file_path"] = root + "/README.md"
			},
			want: ExitBlock,
		},
		{
			name: "caminho relativo resolvido contra a raiz, não contra o cwd (TD-09)",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, root string) {
				delete(m, "agent_type")
				m["cwd"] = root + "/scripts"
				toolInput(m)["file_path"] = "internal/x.go"
			},
			want: ExitBlock,
		},
		{
			name: "caixa diferente (TD-09)",
			base: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, root string) {
				toolInput(m)["file_path"] = root + "/Internal/X.go"
			},
			want: ExitBlock,
		},
		{
			name: ".. que fica na raiz",
			base: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, root string) {
				toolInput(m)["file_path"] = root + "/scripts/../internal/x.go"
			},
			want: ExitBlock,
		},
		{
			name: ".. escapando da raiz é outro repositório",
			base: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, root string) {
				toolInput(m)["file_path"] = root + "/internal/../../outro/internal/x.go"
			},
			want: ExitAllow,
		},
		{
			name: "relativo com .. escapando da raiz",
			base: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, _ string) {
				toolInput(m)["file_path"] = "../outro/internal/x.go"
			},
			want: ExitAllow,
		},
		{
			name: "cwd ausente usa getwd",
			base: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, _ string) {
				delete(m, "cwd")
			},
			want: ExitBlock, // getwd falha neste teste: bloqueia
		},
		{
			name: "tool_name com tipo errado",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				m["tool_name"] = 7
			},
			want: ExitBlock,
		},
		{
			name: "file_path com tipo errado",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				toolInput(m)["file_path"] = []any{"x"}
			},
			want: ExitBlock,
		},
		{
			name: "agent_type com tipo errado",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, _ string) {
				m["agent_type"] = true
			},
			want: ExitBlock,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newProject(t, t.TempDir())
			m := loadFixture(t, tt.base, root)
			tt.apply(m, root)
			code, msg := run(t, marshal(t, m), getwdErr)
			if code != tt.want {
				t.Errorf("Run = %d, quer %d; stderr:\n%s", code, tt.want, msg)
			}
		})
	}
}

func TestGetwdQuandoSemCwd(t *testing.T) {
	root := newProject(t, t.TempDir())
	m := loadFixture(t, "2.1.309/pretooluse-write-dev-implementation.json", root)
	delete(m, "cwd")
	code, msg := run(t, marshal(t, m), func() (string, error) { return root, nil })
	if code != ExitAllow {
		t.Errorf("Run = %d, quer 0; stderr:\n%s", code, msg)
	}
}

func TestPayloadIlegivel(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"vazio", ""},
		{"JSON inválido", `{"tool_name":`},
		{"null", `null`},
		{"array", `[{"tool_name":"Write"}]`},
		{"string", `"Write"`},
		{"número", `2`},
		{"objeto seguido de lixo", `{"tool_name":"Read"} x`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := run(t, []byte(tt.payload), getwdErr)
			if code != ExitBlock {
				t.Errorf("Run = %d, quer 2", code)
			}
			if msg == "" {
				t.Error("bloqueio sem mensagem")
			}
		})
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin fechado") }

func TestStdinComErro(t *testing.T) {
	var stderr bytes.Buffer
	if code := Run(errReader{}, &stderr, getwdErr); code != ExitBlock {
		t.Errorf("Run = %d, quer 2", code)
	}
}

func TestConfiguracao(t *testing.T) {
	t.Run("ausente", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := config.Find(dir); err == nil {
			t.Skip("existe um " + config.FileName + " acima de " + dir)
		}
		m := loadFixture(t, "2.1.309/pretooluse-write-dev-implementation.json", dir)
		code, msg := run(t, marshal(t, m), getwdErr)
		if code != ExitBlock {
			t.Errorf("Run = %d, quer 2", code)
		}
		if !strings.Contains(msg, "pipeline-claude doctor") {
			t.Errorf("mensagem sem apontar o doctor:\n%s", msg)
		}
	})
	t.Run("inválida", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(`{"version":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
		m := loadFixture(t, "2.1.309/pretooluse-write-dev-implementation.json", dir)
		code, msg := run(t, marshal(t, m), getwdErr)
		if code != ExitBlock {
			t.Errorf("Run = %d, quer 2", code)
		}
		if !strings.Contains(msg, "pipeline-claude doctor") {
			t.Errorf("mensagem sem apontar o doctor:\n%s", msg)
		}
	})
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink indisponível: %v", err)
	}
}

func TestSymlinks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root, outside string) string // devolve o caminho editado
		want  int
	}{
		{
			name: "de fora para dentro de caminho protegido",
			setup: func(t *testing.T, root, outside string) string {
				mkdir(t, filepath.Join(root, "internal"))
				symlinkOrSkip(t, filepath.Join(root, "internal"), filepath.Join(outside, "atalho"))
				return filepath.Join(outside, "atalho", "x.go")
			},
			want: ExitBlock,
		},
		{
			name: "de caminho livre para protegido dentro da raiz",
			setup: func(t *testing.T, root, _ string) string {
				mkdir(t, filepath.Join(root, "internal"))
				symlinkOrSkip(t, "internal", filepath.Join(root, "atalho"))
				return filepath.Join(root, "atalho", "x.go")
			},
			want: ExitBlock,
		},
		{
			name: "arquivo existente que é link para protegido",
			setup: func(t *testing.T, root, _ string) string {
				mkdir(t, filepath.Join(root, "internal"))
				if err := os.WriteFile(filepath.Join(root, "internal", "x.go"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				symlinkOrSkip(t, filepath.Join(root, "internal", "x.go"), filepath.Join(root, "x.go"))
				return filepath.Join(root, "x.go")
			},
			want: ExitBlock,
		},
		{
			name: "link quebrado bloqueia",
			setup: func(t *testing.T, root, _ string) string {
				symlinkOrSkip(t, filepath.Join(root, "internal", "novo.go"), filepath.Join(root, "novo.go"))
				return filepath.Join(root, "novo.go")
			},
			want: ExitBlock,
		},
		{
			name: "link de dentro para fora da raiz é outro repositório",
			setup: func(t *testing.T, root, outside string) string {
				symlinkOrSkip(t, outside, filepath.Join(root, "fora"))
				return filepath.Join(root, "fora", "x.go")
			},
			want: ExitAllow,
		},
		{
			name: "raiz acessada por link",
			setup: func(t *testing.T, root, outside string) string {
				symlinkOrSkip(t, root, filepath.Join(outside, "proj"))
				return filepath.Join(outside, "proj", "internal", "x.go")
			},
			want: ExitBlock,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newProject(t, t.TempDir())
			outside := t.TempDir()
			target := tt.setup(t, root, outside)
			m := loadFixture(t, "2.1.309/pretooluse-write-main.json", root)
			toolInput(m)["file_path"] = target
			code, msg := run(t, marshal(t, m), getwdErr)
			if code != tt.want {
				t.Errorf("Run = %d, quer %d; stderr:\n%s", code, tt.want, msg)
			}
		})
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestMensagemDeBloqueio(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		apply func(m map[string]any, root string)
		want  []string
	}{
		{
			name: "sessão principal",
			base: "2.1.309/pretooluse-edit-main.json",
			want: []string{"internal/exemplo/alvo.go", "sessão principal", "agente sem permissão", "dev-implementation", "Próximo passo"},
		},
		{
			name: "agente não editor",
			base: "2.1.309/pretooluse-write-pm-triage.json",
			want: []string{"docs-ai/exemplo-triage.md", `agente "pm-triage"`, "agente sem permissão", "dev-implementation", "Próximo passo"},
		},
		{
			name: "no_agent_paths",
			base: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, root string) {
				toolInput(m)["file_path"] = root + "/docs-ai/restrito/x.md"
			},
			want: []string{"docs-ai/restrito/x.md", `agente "dev-implementation"`, "caminho sem edição por agentes", "Próximo passo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newProject(t, t.TempDir())
			m := loadFixture(t, tt.base, root)
			if tt.apply != nil {
				tt.apply(m, root)
			}
			code, msg := run(t, marshal(t, m), getwdErr)
			if code != ExitBlock {
				t.Fatalf("Run = %d, quer 2", code)
			}
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("mensagem sem %q:\n%s", w, msg)
				}
			}
			// Nada do payload além do caminho e do agente.
			for _, leak := range []string{"<removido>", "toolu_0001", "00000000-0000", "sessao.jsonl", "scratchpad", root} {
				if strings.Contains(msg, leak) {
					t.Errorf("mensagem vaza %q:\n%s", leak, msg)
				}
			}
		})
	}
}

// editTool espelha as ferramentas de edição para a propriedade do fuzz.
var fuzzEditTools = map[string]string{
	"Edit":         "file_path",
	"Write":        "file_path",
	"MultiEdit":    "file_path",
	"NotebookEdit": "notebook_path",
}

func FuzzRun(f *testing.F) {
	root := newProject(f, f.TempDir())
	entries, err := filepath.Glob(filepath.Join(fixturesDir, "*", "*.json"))
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range entries {
		rel, _ := filepath.Rel(fixturesDir, p)
		f.Add(marshal(f, loadFixture(f, filepath.ToSlash(rel), root)))
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"tool_name":"Write","tool_input":{"file_path":""}}`))
	f.Add([]byte(`{"tool_name":"Write","tool_input":{"file_path":"internal/x.go"}}`))
	f.Add([]byte(`{"tool_name":"Read"}`))

	getwd := func() (string, error) { return root, nil }
	f.Fuzz(func(t *testing.T, data []byte) {
		var stderr bytes.Buffer
		code := Run(bytes.NewReader(data), &stderr, getwd)
		if code != ExitAllow && code != ExitBlock {
			t.Fatalf("código %d fora de {0, 2}", code)
		}
		if code != ExitAllow {
			return
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil || m == nil {
			t.Fatalf("liberou payload que não é objeto JSON: %q", data)
		}
		if ev, ok := m["hook_event_name"]; ok && ev != "PreToolUse" {
			t.Fatalf("liberou evento %v", ev)
		}
		tool, _ := m["tool_name"].(string)
		key, isEdit := fuzzEditTools[tool]
		if !isEdit {
			return
		}
		in, _ := m["tool_input"].(map[string]any)
		if p, _ := in[key].(string); p == "" {
			t.Fatalf("liberou %s sem caminho: %q", tool, data)
		}
	})
}
