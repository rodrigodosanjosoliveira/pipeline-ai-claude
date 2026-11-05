package delivery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

const syncIndex = "# Deliveries\n\n" +
	"| ID | Título | Status | Data |\n" +
	"|---|---|---|---|\n" +
	"| DEL-001 | Multa por atraso | merged | 2026-11-01 |\n" +
	"| DEL-002 | Reserva de livros | em revisão | 2026-11-02 |\n" +
	"| DEL-003 | Renovação | draft | 2026-11-03 |\n" +
	"| DEL-004 | Catálogo | cancelada | 2026-11-03 |\n" +
	"| DEL-010 | Leitores | draft | 2026-11-04 |\n" +
	"| DEL-011 | Sem commit | em revisão | 2026-11-05 |\n"

func syncReport(id, status string) string {
	return "---\ndelivery_id: " + id + "\nstatus: " + status + "\nrequire_security: false\n---\n\n# " + id + "\n\nstatus: " + status + " (no corpo não muda)\n"
}

// syncProject cria o projeto fictício num repositório git com um commit por
// mensagem em commits, na main.
func syncProject(t *testing.T, index string, reports map[string]string, commits ...string) *config.Config {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git indisponível")
	}
	files := map[string]string{indexRel: index}
	for id, status := range reports {
		files[deliveriesDir+"/"+id+"/report.md"] = syncReport(id, status)
	}
	cfg := project(t, `"delivery_id_pattern": "^DEL-[0-9]{3}$", "delivery_prefixes": ["DEL"], "base_ref": "main"`, files)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cfg.Root()
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Biblioteca", "GIT_AUTHOR_EMAIL=dev@biblioteca.example",
			"GIT_COMMITTER_NAME=Biblioteca", "GIT_COMMITTER_EMAIL=dev@biblioteca.example", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "maintenance.auto", "false")
	git("config", "gc.auto", "0")
	git("add", "-A")
	git("commit", "-q", "-m", "chore: projeto inicial")
	for _, msg := range commits {
		git("commit", "-q", "--allow-empty", "-m", msg)
	}
	return cfg
}

func allReports() map[string]string {
	return map[string]string{"DEL-001": "merged", "DEL-002": "em revisão", "DEL-003": "draft", "DEL-004": "cancelada", "DEL-010": "draft", "DEL-011": "em revisão"}
}

func readRel(t *testing.T, cfg *config.Config, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.Root(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSyncIndexPromoveSoComCommit(t *testing.T) {
	cfg := syncProject(t, syncIndex, allReports(),
		"feat(reserva): reserva de livros [DEL-002]",
		"feat(leitores): cadastro [DEL-010]",
		"docs: cita [DEL-004] cancelada",
	)
	changes, err := SyncIndex(context.Background(), cfg, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		if len(c.Commit) != 40 {
			t.Errorf("%s: commit %q não é hash completo", c.ID, c.Commit)
		}
		got = append(got, c.ID+" "+c.From+" -> "+c.To)
	}
	// DEL-001 já está merged; DEL-003 e DEL-011 não têm commit; DEL-004 tem
	// commit, mas status cancelada não é promovido.
	want := "DEL-002 em revisão -> merged\nDEL-010 draft -> merged"
	if strings.Join(got, "\n") != want {
		t.Errorf("mudanças =\n%s\nquer\n%s", strings.Join(got, "\n"), want)
	}
	wantIndex := strings.NewReplacer(
		"| DEL-002 | Reserva de livros | em revisão |", "| DEL-002 | Reserva de livros | merged |",
		"| DEL-010 | Leitores | draft |", "| DEL-010 | Leitores | merged |",
	).Replace(syncIndex)
	if got := readRel(t, cfg, indexRel); got != wantIndex {
		t.Errorf("índice =\n%s\nquer\n%s", got, wantIndex)
	}
	for id, want := range map[string]string{
		"DEL-001": syncReport("DEL-001", "merged"),
		"DEL-002": strings.Replace(syncReport("DEL-002", "em revisão"), "status: em revisão\nrequire", "status: merged\nrequire", 1),
		"DEL-003": syncReport("DEL-003", "draft"),
		"DEL-004": syncReport("DEL-004", "cancelada"),
		"DEL-010": strings.Replace(syncReport("DEL-010", "draft"), "status: draft\nrequire", "status: merged\nrequire", 1),
		"DEL-011": syncReport("DEL-011", "em revisão"),
	} {
		if got := readRel(t, cfg, deliveriesDir+"/"+id+"/report.md"); got != want {
			t.Errorf("report %s =\n%s\nquer\n%s", id, got, want)
		}
	}

	// Idempotente: a segunda execução não muda nada.
	again, err := SyncIndex(context.Background(), cfg, SyncOptions{})
	if err != nil || len(again) != 0 {
		t.Errorf("segunda execução = %v, %v; quer nenhuma mudança", again, err)
	}
}

func TestSyncIndexIDPrefixoDeOutro(t *testing.T) {
	index := "| ID | Status |\n|---|---|\n| DEL-001 | draft |\n| DEL-010 | draft |\n"
	cfg := syncProject(t, index, map[string]string{"DEL-001": "draft", "DEL-010": "draft"}, "feat: x [DEL-010]")
	changes, err := SyncIndex(context.Background(), cfg, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].ID != "DEL-010" {
		t.Errorf("mudanças = %+v; quer só DEL-010", changes)
	}
}

func TestSyncIndexDryRun(t *testing.T) {
	cfg := syncProject(t, syncIndex, allReports(), "feat: reserva [DEL-002]")
	before := snapshot(t, cfg.Root())
	changes, err := SyncIndex(context.Background(), cfg, SyncOptions{DryRun: true})
	if err != nil || len(changes) != 1 {
		t.Fatalf("dry-run = %v, %v; quer uma mudança", changes, err)
	}
	if after := snapshot(t, cfg.Root()); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("--dry-run gravou")
	}
}

func TestSyncIndexCRLF(t *testing.T) {
	index := strings.ReplaceAll(syncIndex, "\n", "\r\n")
	cfg := syncProject(t, index, map[string]string{"DEL-002": "em revisão"}, "feat: reserva [DEL-002]")
	report := strings.ReplaceAll(syncReport("DEL-002", "em revisão"), "\n", "\r\n")
	if err := os.WriteFile(filepath.Join(cfg.Root(), filepath.FromSlash(deliveriesDir+"/DEL-002/report.md")), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	// Só DEL-002 tem commit; os outros reports não precisam existir.
	if _, err := SyncIndex(context.Background(), cfg, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	wantIndex := strings.Replace(index, "| em revisão | 2026-11-02 |", "| merged | 2026-11-02 |", 1)
	if got := readRel(t, cfg, indexRel); got != wantIndex {
		t.Errorf("índice CRLF =\n%q\nquer\n%q", got, wantIndex)
	}
	wantReport := strings.Replace(report, "status: em revisão\r\nrequire", "status: merged\r\nrequire", 1)
	if got := readRel(t, cfg, deliveriesDir+"/DEL-002/report.md"); got != wantReport {
		t.Errorf("report CRLF =\n%q\nquer\n%q", got, wantReport)
	}
}

func TestSyncIndexFalhaSemGravar(t *testing.T) {
	cases := []struct {
		name    string
		index   string
		reports map[string]string
		opts    SyncOptions
		setup   func(t *testing.T, cfg *config.Config)
		want    string
	}{
		{name: "base inexistente", index: syncIndex, reports: allReports(), opts: SyncOptions{Base: "origin/nao-existe"}, want: "origin/nao-existe"},
		{name: "base com hífen", index: syncIndex, reports: allReports(), opts: SyncOptions{Base: "--output=x"}, want: "-"},
		{name: "índice sem tabela", index: "# Deliveries\n\nnada aqui\n", want: "tabela"},
		{name: "índice sem coluna Status", index: "| ID | Título |\n|---|---|\n| DEL-002 | Reserva |\n", want: "Status"},
		{name: "report ausente", index: syncIndex, reports: map[string]string{"DEL-002": "em revisão"}, want: "DEL-010"},
		{
			name: "report sem status no front matter", index: syncIndex, reports: allReports(),
			setup: func(t *testing.T, cfg *config.Config) {
				p := filepath.Join(cfg.Root(), filepath.FromSlash(deliveriesDir+"/DEL-010/report.md"))
				if err := os.WriteFile(p, []byte("---\ndelivery_id: DEL-010\n---\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "status",
		},
		{
			name: "report sem front matter", index: syncIndex, reports: allReports(),
			setup: func(t *testing.T, cfg *config.Config) {
				p := filepath.Join(cfg.Root(), filepath.FromSlash(deliveriesDir+"/DEL-002/report.md"))
				if err := os.WriteFile(p, []byte("status: em revisão\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "front matter",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := syncProject(t, c.index, c.reports, "feat: reserva [DEL-002]", "feat: leitores [DEL-010]")
			if c.setup != nil {
				c.setup(t, cfg)
			}
			before := snapshot(t, cfg.Root())
			_, err := SyncIndex(context.Background(), cfg, c.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erro = %v, quer conter %q", err, c.want)
			}
			if after := snapshot(t, cfg.Root()); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("falha gravou algo")
			}
		})
	}
}

func TestSyncIndexIgnoraComentarioEBloco(t *testing.T) {
	index := "| ID | Status |\n|---|---|\n| DEL-002 | em revisão |\n\n<!--\n| DEL-003 | draft |\n-->\n\n```\n| DEL-010 | draft |\n```\n"
	cfg := syncProject(t, index, map[string]string{"DEL-002": "em revisão"}, "feat: [DEL-002]", "feat: [DEL-003]", "feat: [DEL-010]")
	changes, err := SyncIndex(context.Background(), cfg, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].ID != "DEL-002" {
		t.Errorf("mudanças = %+v; quer só DEL-002 (as outras estão em comentário e bloco)", changes)
	}
	if got, want := readRel(t, cfg, indexRel), strings.Replace(index, "| DEL-002 | em revisão |", "| DEL-002 | merged |", 1); got != want {
		t.Errorf("índice =\n%s\nquer\n%s", got, want)
	}
}

func FuzzSetFrontStatus(f *testing.F) {
	f.Add("---\nstatus: draft\n---\n")
	f.Add("---\r\nstatus: em revisão\r\n---\r\ncorpo\r\n")
	f.Add("sem front matter")
	f.Add("---\nstatus:\n---")
	f.Fuzz(func(t *testing.T, s string) {
		out, err := setFrontStatus(s, "merged")
		if err != nil {
			return
		}
		if !strings.Contains(out, "status: merged") {
			t.Fatalf("sem o status novo: %q", out)
		}
		// Só a linha do status muda.
		if a, b := strings.Count(s, "\n"), strings.Count(out, "\n"); a != b {
			t.Fatalf("número de linhas mudou: %d -> %d", a, b)
		}
	})
}

