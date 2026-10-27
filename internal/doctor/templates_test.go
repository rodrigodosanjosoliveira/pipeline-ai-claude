package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

// TestGeneratedProjectChecks: o .claude/settings.json e o Makefile gerados
// pelos templates (TD-26) passam nas checagens hook e fmt-hook e fixam a
// versão que o binaryCheck lê (TD-06). Fica no pacote doctor para usar as
// funções internas sem exportá-las; templates não importa doctor, então não
// há ciclo.
func TestGeneratedProjectChecks(t *testing.T) {
	d := templates.Data{
		Name: "biblioteca-bairro", Module: "example.com/biblioteca-bairro",
		Summary: "API de empréstimos da biblioteca do bairro.", Provider: "github",
		ToolkitVersion: "v0.3.0",
	}
	root := t.TempDir()
	for _, f := range templates.Expected(templates.Project, nil) {
		if f.Path != ".claude/settings.json" && f.Path != "Makefile" {
			continue
		}
		out, err := templates.Render(f, d)
		if err != nil {
			t.Fatalf("Render(%s): %v", f.Path, err)
		}
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, out, f.Mode); err != nil {
			t.Fatal(err)
		}
	}

	st := readSettings(root)
	if c := hookCheck(st); c.Status != StatusOK {
		t.Errorf("hook no settings.json gerado: %+v", c)
	}
	if c := fmtHookCheck(st); c.Status != StatusOK {
		t.Errorf("fmt-hook no settings.json gerado: %+v", c)
	}
	v, err := DeclaredVersion(root)
	if err != nil || v != d.ToolkitVersion {
		t.Errorf("versão no Makefile gerado = %q, %v; esperado %q", v, err, d.ToolkitVersion)
	}
}
