package hookio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin fechado") }

func TestReadPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"objeto", `{"tool_name":"Write"}`, ""},
		{"objeto vazio", `{}`, ""},
		{"vazio", "", "objeto JSON"},
		{"JSON inválido", `{"tool_name":`, "objeto JSON"},
		{"null", `null`, "objeto JSON"},
		{"array", `[{"tool_name":"Write"}]`, "objeto JSON"},
		{"string", `"Write"`, "objeto JSON"},
		{"número", `2`, "objeto JSON"},
		{"objeto seguido de lixo", `{"tool_name":"Read"} x`, "objeto JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadPayload(strings.NewReader(tt.payload))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("erro inesperado: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("erro = %v, quer trecho %q", err, tt.wantErr)
			}
		})
	}
}

func TestReadPayloadStdinComErro(t *testing.T) {
	_, err := ReadPayload(errReader{})
	if err == nil || !strings.Contains(err.Error(), "falha ao ler stdin") {
		t.Errorf("erro = %v", err)
	}
}

func TestString(t *testing.T) {
	p, err := ReadPayload(strings.NewReader(`{"a":"x","vazio":"","num":7,"nulo":null,"Tool_Name":"Read"}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		key     string
		want    string
		present bool
		wantErr bool
	}{
		{"a", "x", true, false},
		{"vazio", "", true, false},
		{"ausente", "", false, false},
		{"num", "", true, true},
		// null vira "" sem erro, como no hook antes da extração (sem mudança
		// de comportamento); a ausência continua distinguível pela presença.
		{"nulo", "", true, false},
		// Chave por nome exato: "Tool_Name" não responde por "tool_name".
		{"tool_name", "", false, false},
	}
	for _, tt := range tests {
		v, present, err := p.String(tt.key)
		if v != tt.want || present != tt.present || (err != nil) != tt.wantErr {
			t.Errorf("String(%q) = %q, %v, %v; quer %q, %v, erro=%v", tt.key, v, present, err, tt.want, tt.present, tt.wantErr)
		}
	}
}

func TestObject(t *testing.T) {
	p, err := ReadPayload(strings.NewReader(`{"tool_input":{"file_path":"x.go"},"lista":[1],"nulo":null}`))
	if err != nil {
		t.Fatal(err)
	}
	in, err := p.Object("tool_input")
	if err != nil {
		t.Fatalf("Object(tool_input): %v", err)
	}
	if v, ok, err := in.String("file_path"); v != "x.go" || !ok || err != nil {
		t.Errorf("file_path = %q, %v, %v", v, ok, err)
	}
	for _, key := range []string{"lista", "nulo", "ausente"} {
		if _, err := p.Object(key); err == nil {
			t.Errorf("Object(%q) sem erro", key)
		}
	}
}

func TestPathKey(t *testing.T) {
	tests := []struct {
		tool string
		key  string
		ok   bool
	}{
		{"Edit", "file_path", true},
		{"Write", "file_path", true},
		{"MultiEdit", "file_path", true},
		{"NotebookEdit", "notebook_path", true},
		{"Read", "", false},
		{"edit", "", false},
	}
	for _, tt := range tests {
		if key, ok := PathKey(tt.tool); key != tt.key || ok != tt.ok {
			t.Errorf("PathKey(%q) = %q, %v", tt.tool, key, ok)
		}
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink indisponível: %v", err)
	}
}

func TestResolveInRoot(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, root, outside string) string
		rel     string // esperado quando dentro
		inside  bool
		wantErr bool
	}{
		{
			name:   "absoluto dentro, arquivo inexistente",
			setup:  func(_ *testing.T, root, _ string) string { return filepath.Join(root, "internal", "x.go") },
			rel:    "internal/x.go",
			inside: true,
		},
		{
			name:   "relativo contra a raiz",
			setup:  func(_ *testing.T, _, _ string) string { return "internal/x.go" },
			rel:    "internal/x.go",
			inside: true,
		},
		{
			name:   ".. que fica na raiz",
			setup:  func(_ *testing.T, root, _ string) string { return filepath.Join(root, "a", "..", "internal", "x.go") },
			rel:    "internal/x.go",
			inside: true,
		},
		{
			name:  ".. escapando da raiz",
			setup: func(_ *testing.T, _, _ string) string { return "../outro/x.go" },
		},
		{
			name:   "a própria raiz",
			setup:  func(_ *testing.T, root, _ string) string { return root },
			rel:    ".",
			inside: true,
		},
		{
			name: "link de fora para dentro",
			setup: func(t *testing.T, root, outside string) string {
				mkdir(t, filepath.Join(root, "internal"))
				symlinkOrSkip(t, filepath.Join(root, "internal"), filepath.Join(outside, "atalho"))
				return filepath.Join(outside, "atalho", "x.go")
			},
			rel:    "internal/x.go",
			inside: true,
		},
		{
			name: "link de dentro para fora",
			setup: func(t *testing.T, root, outside string) string {
				symlinkOrSkip(t, outside, filepath.Join(root, "fora"))
				return filepath.Join(root, "fora", "x.go")
			},
		},
		{
			name: "link quebrado",
			setup: func(t *testing.T, root, _ string) string {
				symlinkOrSkip(t, filepath.Join(root, "internal", "novo.go"), filepath.Join(root, "novo.go"))
				return filepath.Join(root, "novo.go")
			},
			wantErr: true,
		},
		{
			name: "raiz acessada por link",
			setup: func(t *testing.T, root, outside string) string {
				symlinkOrSkip(t, root, filepath.Join(outside, "proj"))
				return filepath.Join(outside, "proj", "internal", "x.go")
			},
			rel:    "internal/x.go",
			inside: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			target := tt.setup(t, root, outside)
			got, err := ResolveInRoot(root, target)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("esperava erro, obteve %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("erro: %v", err)
			}
			if got.Inside != tt.inside {
				t.Fatalf("Inside = %v, quer %v (%+v)", got.Inside, tt.inside, got)
			}
			if !tt.inside {
				return
			}
			if got.Rel != tt.rel {
				t.Errorf("Rel = %q, quer %q", got.Rel, tt.rel)
			}
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(realRoot, filepath.FromSlash(tt.rel)); got.Real != want {
				t.Errorf("Real = %q, quer %q", got.Real, want)
			}
		})
	}
}

func TestWithinSemCaixa(t *testing.T) {
	sep := string(filepath.Separator)
	root := sep + filepath.Join("Workspace", "Exemplo")
	tests := []struct {
		path   string
		rel    string
		inside bool
	}{
		{root, ".", true},
		{sep + filepath.Join("workspace", "exemplo", "internal", "x.go"), filepath.Join("internal", "x.go"), true},
		{sep + filepath.Join("workspace", "exemplo2", "x.go"), "", false},
		{sep + filepath.Join("outro", "x.go"), "", false},
	}
	for _, tt := range tests {
		rel, ok := within(root, tt.path)
		if ok != tt.inside || rel != tt.rel {
			t.Errorf("within(%q, %q) = %q, %v", root, tt.path, rel, ok)
		}
	}
}

func FuzzReadPayload(f *testing.F) {
	for _, s := range []string{`{}`, `{"tool_name":"Write","tool_input":{"file_path":"x"}}`, `null`, `[]`, `{"a":1} x`, ``} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ReadPayload(strings.NewReader(string(data)))
		if err != nil {
			return
		}
		if p.fields == nil {
			t.Fatalf("payload aceito sem objeto: %q", data)
		}
		for _, k := range []string{"tool_name", "cwd", "tool_input"} {
			_, _, _ = p.String(k)
			_, _ = p.Object(k)
		}
	})
}
