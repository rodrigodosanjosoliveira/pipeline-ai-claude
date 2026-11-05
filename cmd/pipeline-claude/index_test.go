package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// indexProject é o e2eProject num repositório git: DEL-001 merged, DEL-002 em
// revisão com commit [DEL-002] na main, e DEL-003 draft sem commit.
func indexProject(t *testing.T) string {
	t.Helper()
	root := e2eProject(t)
	writeFile(t, root, "docs-ai/deliveries/INDEX.md", "| ID | Status |\n|---|---|\n| DEL-001 | merged |\n| DEL-002 | em revisão |\n| DEL-003 | draft |\n")
	writeFile(t, root, "docs-ai/deliveries/DEL-002/report.md", strings.Replace(readFile(t, root, "docs-ai/deliveries/DEL-002/report.md"), "status: done", "status: em revisão", 1))
	writeFile(t, root, "docs-ai/deliveries/DEL-003/report.md", "---\ndelivery_id: DEL-003\nstatus: draft\n---\n")
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "chore: inicial")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "feat(reserva): reserva de livros [DEL-002]")
	return root
}

func TestE2EIndexSync(t *testing.T) {
	root := indexProject(t)
	index := readFile(t, root, "docs-ai/deliveries/INDEX.md")

	code, stdout, stderr := runBin(t, root, "", "index", "sync", "--base", "main", "--dry-run")
	if code != exitOK || !strings.Contains(stdout, "DEL-002") || !strings.Contains(stdout, "--dry-run") {
		t.Fatalf("dry-run: código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := readFile(t, root, "docs-ai/deliveries/INDEX.md"); got != index {
		t.Errorf("--dry-run gravou o índice:\n%s", got)
	}

	code, stdout, stderr = runBin(t, root, "", "index", "sync", "--json", "--base", "main")
	if code != exitOK {
		t.Fatalf("sync: código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var out struct {
		OK      bool `json:"ok"`
		Changes []struct {
			ID, From, To, Commit string
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("saída não é JSON: %v\n%s", err, stdout)
	}
	if !out.OK || len(out.Changes) != 1 || out.Changes[0].ID != "DEL-002" || out.Changes[0].From != "em revisão" || out.Changes[0].To != "merged" || len(out.Changes[0].Commit) != 40 {
		t.Errorf("json = %+v", out)
	}
	if got := readFile(t, root, "docs-ai/deliveries/INDEX.md"); !strings.Contains(got, "| DEL-002 | merged |") || !strings.Contains(got, "| DEL-003 | draft |") {
		t.Errorf("índice:\n%s", got)
	}
	if got := readFile(t, root, "docs-ai/deliveries/DEL-002/report.md"); !strings.Contains(got, "\nstatus: merged\n") {
		t.Errorf("report DEL-002:\n%s", got)
	}

	code, stdout, _ = runBin(t, root, "", "index", "sync", "--base", "main")
	if code != exitOK || !strings.Contains(stdout, "Nenhuma") {
		t.Errorf("segunda execução: código %d\n%s", code, stdout)
	}
}

func TestE2EIndexSyncErros(t *testing.T) {
	root := indexProject(t)
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"index", "sync", "--base", "origin/nao-existe"}, exitFail, "origin/nao-existe"},
		{[]string{"index"}, exitBlocked, "Uso: pipeline-claude index sync"},
		{[]string{"index", "listar"}, exitBlocked, "subcomando desconhecido"},
		{[]string{"index", "sync", "extra"}, exitBlocked, "argumento inesperado"},
		{[]string{"index", "sync", "--nada"}, exitBlocked, "opção desconhecida --nada"},
	}
	for _, c := range cases {
		code, _, stderr := runBin(t, root, "", c.args...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: código %d, quer %d; stderr:\n%s", c.args, code, c.code, stderr)
		}
	}
	code, stdout, _ := runBin(t, root, "", "index", "sync", "--json", "--base", "origin/nao-existe")
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if code != exitFail || json.Unmarshal([]byte(stdout), &out) != nil || out.OK || !strings.Contains(out.Error, "origin/nao-existe") {
		t.Errorf("--json com erro: código %d\n%s", code, stdout)
	}
}
