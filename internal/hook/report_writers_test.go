package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// writersConfig tem docs-ai/ fora de protected_paths: report_writers exige
// deliveries_dir editável (TD-35).
const writersConfig = `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "protected_paths": ["internal/"],
 "editor_agents": ["dev-implementation"],
 "report_writers": ["code-reviewer"]}`

func TestReportWriters(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		rel   string
		want  int
		msg   string // trecho do stderr em bloqueio
	}{
		{"revisor no report", "code-reviewer", "docs-ai/deliveries/FEAT-001/report.md", ExitAllow, ""},
		{"revisor no índice", "code-reviewer", "docs-ai/deliveries/INDEX.md", ExitAllow, ""},
		{"revisor fora de deliveries_dir", "code-reviewer", "README.md", ExitBlock, "registre o achado no report"},
		{"revisor em diretório irmão com o mesmo prefixo", "code-reviewer", "docs-ai/deliveries-extra/x.md", ExitBlock, "deliveries_dir"},
		{"revisor subindo com ..", "code-reviewer", "docs-ai/deliveries/../../README.md", ExitBlock, "deliveries_dir"},
		{"revisor em caminho protegido", "code-reviewer", "internal/x.go", ExitBlock, "agente sem permissão"},
		{"revisor sem diferenciar caixa", "Code-Reviewer", "README.md", ExitBlock, "deliveries_dir"},
		{"revisor na config", "code-reviewer", config.FileName, ExitBlock, ""},
		{"sessão principal fora de deliveries_dir", "", "README.md", ExitAllow, ""},
		{"agente fora das listas fora de deliveries_dir", "qa-validation", "README.md", ExitAllow, ""},
		{"editor fora de deliveries_dir", "dev-implementation", "README.md", ExitAllow, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(writersConfig), 0o644); err != nil {
				t.Fatal(err)
			}
			m := loadFixture(t, "2.1.309/pretooluse-write-main.json", root)
			m["agent_type"] = tt.agent
			toolInput(m)["file_path"] = root + "/" + tt.rel
			code, stderr := run(t, marshal(t, m), getwdErr)
			if code != tt.want {
				t.Fatalf("Run = %d, quero %d\n%s", code, tt.want, stderr)
			}
			if tt.msg != "" && !strings.Contains(stderr, tt.msg) {
				t.Errorf("stderr sem %q:\n%s", tt.msg, stderr)
			}
		})
	}
}

func TestReportWriterSymlink(t *testing.T) {
	// Symlink dentro de deliveries_dir apontando para fora: vale o alvo.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(writersConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs-ai", "deliveries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("leia\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(root, "README.md"), filepath.Join(root, "docs-ai", "deliveries", "atalho.md"))
	m := loadFixture(t, "2.1.309/pretooluse-write-main.json", root)
	m["agent_type"] = "code-reviewer"
	toolInput(m)["file_path"] = root + "/docs-ai/deliveries/atalho.md"
	if code, stderr := run(t, marshal(t, m), getwdErr); code != ExitBlock {
		t.Errorf("Run = %d, quero 2 (alvo fora de deliveries_dir)\n%s", code, stderr)
	}
}
