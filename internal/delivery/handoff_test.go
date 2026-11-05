package delivery

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// handoffProject cria o projeto fictício com a delivery DEL-001 e os
// subagentes pm-triage e code-reviewer.
func handoffProject(t *testing.T, extra map[string]string) *config.Config {
	t.Helper()
	files := map[string]string{
		indexRel:                               indexHead + "| DEL-001 | Multa por atraso | draft | 2026-11-05 |\n",
		deliveriesDir + "/DEL-001/report.md":   "---\ndelivery_id: DEL-001\nstatus: draft\n---\n",
		".claude/agents/pm-triage.md":          "---\nname: pm-triage\n---\n",
		".claude/agents/code-reviewer.md":      "---\nname: code-reviewer\n---\n",
		".claude/agents/dev-implementation.md": "---\nname: dev-implementation\n---\n",
	}
	return project(t, `"delivery_id_pattern": "^DEL-[0-9]{3}$", "delivery_prefixes": ["DEL"]`, mergeFiles(files, extra))
}

func handoffsDir(cfg *config.Config) string {
	return filepath.Join(cfg.Root(), filepath.FromSlash(deliveriesDir), "DEL-001", HandoffsDir)
}

func TestPutHandoffGravaEmSequencia(t *testing.T) {
	cfg := handoffProject(t, nil)
	for i, c := range []struct{ agent, content, want string }{
		{"pm-triage", "STATUS: ok\nPROXIMO: architecture-guardrails\n", "docs-ai/deliveries/DEL-001/handoffs/01-pm-triage.md"},
		{"code-reviewer", "STATUS: bloqueado\n", "docs-ai/deliveries/DEL-001/handoffs/02-code-reviewer.md"},
		{"code-reviewer", "STATUS: ok\n", "docs-ai/deliveries/DEL-001/handoffs/03-code-reviewer.md"},
	} {
		got, err := PutHandoff(cfg, "DEL-001", c.agent, c.content)
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if got != c.want {
			t.Errorf("put %d = %q, quer %q", i, got, c.want)
		}
		data, err := os.ReadFile(filepath.Join(cfg.Root(), filepath.FromSlash(got)))
		if err != nil || string(data) != c.content {
			t.Errorf("conteúdo de %s = %q (%v), quer %q", got, data, err, c.content)
		}
	}
}

func TestPutHandoffSegueODoMaiorNumero(t *testing.T) {
	// Lacuna não é reaproveitada: a ordem dos arquivos é a ordem das gravações.
	cfg := handoffProject(t, map[string]string{
		deliveriesDir + "/DEL-001/handoffs/01-pm-triage.md":  "a",
		deliveriesDir + "/DEL-001/handoffs/07-pm-triage.md":  "b",
		deliveriesDir + "/DEL-001/handoffs/notas-soltas.txt": "fora do padrão",
	})
	got, err := PutHandoff(cfg, "DEL-001", "code-reviewer", "STATUS: ok\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "docs-ai/deliveries/DEL-001/handoffs/08-code-reviewer.md"; got != want {
		t.Errorf("put = %q, quer %q", got, want)
	}
}

func TestPutHandoffRecusa(t *testing.T) {
	big := strings.Repeat("a", MaxHandoffSize+1)
	cases := []struct {
		name, id, agent, content, want string
		extra                          map[string]string
	}{
		{"delivery inexistente", "DEL-002", "pm-triage", "x", "não existe", nil},
		{"ID vazio", "", "pm-triage", "x", "ID", nil},
		{"ID com barra", "DEL-001/../DEL-001", "pm-triage", "x", "ID", nil},
		{"ID ponto-ponto", "..", "pm-triage", "x", "ID", nil},
		{"ID fora do padrão", "FEAT-001", "pm-triage", "x", "delivery_id_pattern", map[string]string{deliveriesDir + "/FEAT-001/report.md": "x"}},
		{"agente com maiúscula", "DEL-001", "PM-triage", "x", "agent", nil},
		{"agente com barra", "DEL-001", "../pm-triage", "x", "agent", nil},
		{"agente vazio", "DEL-001", "", "x", "agent", nil},
		{"agente sem arquivo", "DEL-001", "release-ops", "x", ".claude/agents/release-ops.md", nil},
		{"conteúdo vazio", "DEL-001", "pm-triage", " \n\t", "vazio", nil},
		{"conteúdo com NUL", "DEL-001", "pm-triage", "a\x00b", "NUL", nil},
		{"conteúdo grande", "DEL-001", "pm-triage", big, "64 KiB", nil},
		{"conteúdo UTF-8 inválido", "DEL-001", "pm-triage", "a\xffb", "UTF-8", nil},
		{"report é diretório", "DEL-003", "pm-triage", "x", "não é arquivo", map[string]string{deliveriesDir + "/DEL-003/report.md/x": "x"}},
		{"handoffs é arquivo", "DEL-001", "pm-triage", "x", "não é diretório", map[string]string{deliveriesDir + "/DEL-001/handoffs": "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := handoffProject(t, c.extra)
			before := snapshot(t, cfg.Root())
			_, err := PutHandoff(cfg, c.id, c.agent, c.content)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erro = %v, quer conter %q", err, c.want)
			}
			if after := snapshot(t, cfg.Root()); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("recusa gravou algo no projeto")
			}
		})
	}
}

func TestPutHandoffLimiteDeNoventaENove(t *testing.T) {
	cfg := handoffProject(t, map[string]string{deliveriesDir + "/DEL-001/handoffs/99-pm-triage.md": "x"})
	if _, err := PutHandoff(cfg, "DEL-001", "pm-triage", "x"); err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("erro = %v, quer limite de 99", err)
	}
}

func TestPutHandoffSymlinkParaFora(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink exige privilégio no Windows (TD-39)")
	}
	cfg := handoffProject(t, nil)
	outside := t.TempDir()
	if err := os.Symlink(outside, handoffsDir(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := PutHandoff(cfg, "DEL-001", "pm-triage", "x"); err == nil || !strings.Contains(err.Error(), "fora") {
		t.Fatalf("erro = %v, quer recusa por symlink para fora", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("gravou fora da raiz: %v", entries)
	}
}

func TestPutHandoffConcorrente(t *testing.T) {
	cfg := handoffProject(t, nil)
	const n = 12
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = PutHandoff(cfg, "DEL-001", "code-reviewer", fmt.Sprintf("revisor %d\n", i))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	slices.Sort(paths)
	if len(slices.Compact(slices.Clone(paths))) != n {
		t.Fatalf("caminhos repetidos: %v", paths)
	}
	entries, err := os.ReadDir(handoffsDir(cfg))
	if err != nil || len(entries) != n {
		t.Fatalf("arquivos = %d (%v), quer %d", len(entries), err, n)
	}
}

func TestHandoffs(t *testing.T) {
	cfg := handoffProject(t, map[string]string{
		deliveriesDir + "/DEL-001/handoffs/02-code-reviewer.md": "revisão\n",
		deliveriesDir + "/DEL-001/handoffs/01-pm-triage.md":     "triagem\n",
		deliveriesDir + "/DEL-001/handoffs/10-code-reviewer.md": "re-revisão\n",
		deliveriesDir + "/DEL-001/handoffs/LEIAME.txt":          "ignorado",
	})
	all, err := Handoffs(cfg, "DEL-001", "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range all {
		got = append(got, fmt.Sprintf("%02d %s %s %q", h.N, h.Agent, h.Path, h.Content))
	}
	want := []string{
		`01 pm-triage docs-ai/deliveries/DEL-001/handoffs/01-pm-triage.md "triagem\n"`,
		`02 code-reviewer docs-ai/deliveries/DEL-001/handoffs/02-code-reviewer.md "revisão\n"`,
		`10 code-reviewer docs-ai/deliveries/DEL-001/handoffs/10-code-reviewer.md "re-revisão\n"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("Handoffs =\n%s\nquer\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	only, err := Handoffs(cfg, "DEL-001", "code-reviewer")
	if err != nil || len(only) != 2 || only[0].N != 2 || only[1].N != 10 {
		t.Errorf("Handoffs(code-reviewer) = %+v, %v", only, err)
	}
}

func TestHandoffsVazio(t *testing.T) {
	cfg := handoffProject(t, nil)
	got, err := Handoffs(cfg, "DEL-001", "")
	if err != nil || len(got) != 0 {
		t.Errorf("Handoffs sem pasta = %v, %v; quer lista vazia sem erro", got, err)
	}
}

func TestHandoffsRecusa(t *testing.T) {
	cfg := handoffProject(t, nil)
	for _, c := range []struct{ id, agent, want string }{
		{"DEL-009", "", "não existe"},
		{"../DEL-001", "", "ID"},
		{"DEL-001", "Code Reviewer", "agent"},
	} {
		if _, err := Handoffs(cfg, c.id, c.agent); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Handoffs(%q, %q) erro = %v, quer conter %q", c.id, c.agent, err, c.want)
		}
	}
}

func TestPutHandoffEHandoffsIdaEVolta(t *testing.T) {
	cfg := handoffProject(t, nil)
	content := "STATUS: ok\nPROXIMO: dev-implementation\nSINAIS: SECURITY=nao\n\nÇ acentuação, CRLF\r\nfim\n"
	p, err := PutHandoff(cfg, "DEL-001", "pm-triage", content)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Handoffs(cfg, "DEL-001", "pm-triage")
	if err != nil || len(got) != 1 || got[0].Content != content || got[0].Path != p {
		t.Errorf("ida e volta = %+v, %v", got, err)
	}
}

func FuzzPutHandoff(f *testing.F) {
	f.Add("DEL-001", "pm-triage", "STATUS: ok\n")
	f.Add("../x", "a", "b")
	f.Add("DEL-001", "a-b-", "\x00")
	f.Add("DEL-001/handoffs", "..", "x")
	f.Fuzz(func(t *testing.T, id, agent, content string) {
		cfg := handoffProject(t, nil)
		outside := filepath.Dir(cfg.Root())
		before := snapshotDir(t, outside, cfg.Root())
		p, err := PutHandoff(cfg, id, agent, content)
		if err == nil {
			want := "docs-ai/deliveries/DEL-001/handoffs/"
			if !strings.HasPrefix(p, want) {
				t.Fatalf("gravou em %q, fora de %q", p, want)
			}
		}
		if after := snapshotDir(t, outside, cfg.Root()); after != before {
			t.Fatalf("gravou fora da raiz do projeto")
		}
	})
}

// snapshotDir lista as entradas de dir, exceto skip (a raiz do projeto).
func snapshotDir(t *testing.T, dir, skip string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if filepath.Join(dir, e.Name()) != skip {
			names = append(names, e.Name())
		}
	}
	return strings.Join(names, "\n")
}
