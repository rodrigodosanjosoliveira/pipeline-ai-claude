package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T, root string, commits ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git indisponível")
	}
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.name=Exemplo", "-c", "user.email=exemplo@exemplo.invalid", "-c", "commit.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "chore: inicial")
	for _, m := range commits {
		run("commit", "-q", "--allow-empty", "-m", m)
	}
}

// TD-42: pipeline_index_sync promove e respeita dry_run.
func TestIndexSync(t *testing.T) {
	root := project(t, `["internal/"]`)
	write(t, root, "docs-ai/deliveries/INDEX.md", indexHead+"| DEL-001 | Multa | em revisão | 2026-11-05 |\n")
	write(t, root, "docs-ai/deliveries/DEL-001/report.md", "---\ndelivery_id: DEL-001\nstatus: em revisão\n---\n")
	gitRepo(t, root, "feat: multa [DEL-001]")
	got := serve(t, root,
		call(1, "pipeline_index_sync", `{"base":"main","dry_run":true}`),
		call(2, "pipeline_index_sync", `{"base":"main"}`),
		call(3, "pipeline_index_sync", `{"base":"main"}`),
		call(4, "pipeline_index_sync", `{"base":"origin/nao-existe"}`),
		call(5, "pipeline_index_sync", `{"dry_run":"sim"}`),
	)
	want := []string{
		`{"id":1,"result":{"isError":false,"content":[{"type":"text","text":"~DEL-001"}]}}`,
		`{"id":2,"result":{"isError":false,"content":[{"type":"text","text":"~merged"}]}}`,
		`{"id":3,"result":{"isError":false,"content":[{"type":"text","text":"~Nenhuma"}]}}`,
		`{"id":4,"result":{"isError":true,"content":[{"type":"text","text":"~origin/nao-existe"}]}}`,
		`{"id":5,"error":{"code":-32602}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("respostas = %d: %v", len(got), got)
	}
	for i, w := range want {
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("resposta %d: %v\n%v", i, err, got[i])
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "docs-ai", "deliveries", "INDEX.md"))
	if err != nil || !strings.Contains(string(data), "| DEL-001 | Multa | merged |") {
		t.Errorf("índice: %s %v", data, err)
	}
}

// TD-16: com o índice protegido, o MCP não grava.
func TestIndexSyncRecusadoEmCaminhoProtegido(t *testing.T) {
	root := project(t, `["docs-ai/"]`)
	write(t, root, "docs-ai/deliveries/INDEX.md", indexHead+"| DEL-001 | Multa | em revisão | 2026-11-05 |\n")
	write(t, root, "docs-ai/deliveries/DEL-001/report.md", "---\ndelivery_id: DEL-001\nstatus: em revisão\n---\n")
	gitRepo(t, root, "feat: multa [DEL-001]")
	before := snapshot(t, root)
	got := serve(t, root, call(1, "pipeline_index_sync", `{"base":"main"}`))
	if err := match(obj(t, `{"id":1,"result":{"isError":true,"content":[{"type":"text","text":"~protegido"}]}}`), got[0]); err != nil {
		t.Errorf("%v\n%v", err, got[0])
	}
	if after := snapshot(t, root); after != before {
		t.Errorf("disco mudou")
	}
}
