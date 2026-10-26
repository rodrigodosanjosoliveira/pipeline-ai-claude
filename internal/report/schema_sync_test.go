package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// FuzzSchemaSincronizado garante que config.Load e Check concordam sobre o
// esquema: um título que o config aceita nunca faz Check falhar, e um que o
// Check recusaria o config já recusa. As regras ficam duplicadas porque o
// config não pode importar o report.
func FuzzSchemaSincronizado(f *testing.F) {
	for _, s := range []string{"Dev", "", " ", ":", " .: ", "Revisão: Segurança.", "é", "́", "\t", "QA."} {
		f.Add(s)
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, title string) {
		body, err := json.Marshal(map[string]any{
			"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
			"protected_paths": []string{"internal/"},
			"report_schema": map[string]any{
				"section_level":     2,
				"sections_required": []string{title},
				"fields_required":   map[string][]string{"Dev": {title}},
				"flag_sections":     map[string]any{"require_security": map[string]any{"section": "Security", "fields": []string{title}}},
			},
		})
		if err != nil {
			t.Skip() // string que o JSON não representa
		}
		if err := os.WriteFile(filepath.Join(dir, config.FileName), body, 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, loadErr := config.Load(dir)
		blank := NormalizeTitle(title) == ""
		if blank != (loadErr != nil) {
			t.Fatalf("título %q: NormalizeTitle vazio=%v, config.Load erro=%v", title, blank, loadErr)
		}
		if loadErr != nil {
			if !strings.Contains(loadErr.Error(), "report_schema") {
				t.Fatalf("erro fora do report_schema: %v", loadErr)
			}
			return
		}
		r, err := Parse([]byte("---\ndelivery_id: DEL-001\n---\n"), 2)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Check(r, *cfg.ReportSchema); err != nil {
			t.Fatalf("config aceitou e Check recusou %q: %v", title, err)
		}
	})
}
