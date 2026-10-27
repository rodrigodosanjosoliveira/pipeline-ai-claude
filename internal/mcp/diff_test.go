package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// gitProject monta o projeto de teste num repositório git: main com a base,
// branch task com um commit, uma alteração não commitada e um arquivo novo.
func gitProject(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("PIPELINE_CLAUDE_REQUIRE_GIT") == "1" {
			t.Fatalf("git não encontrado: %v", err)
		}
		t.Skipf("git não encontrado: %v", err)
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Exemplo", "GIT_AUTHOR_EMAIL": "exemplo@exemplo.invalid",
		"GIT_COMMITTER_NAME": "Exemplo", "GIT_COMMITTER_EMAIL": "exemplo@exemplo.invalid",
		"GIT_DIR": "", "GIT_WORK_TREE": "", "GIT_INDEX_FILE": "",
	} {
		t.Setenv(k, v)
	}
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		os.Unsetenv(k)
	}
	root := project(t, `["internal/"]`)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(t, root, "internal/emprestimo/multa.go", "package emprestimo\n\nconst Multa = 1\n")
	write(t, root, "migrations/001_livros.sql", "create table livros (id bigint);\n")
	git("-c", "init.defaultBranch=main", "init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "task")
	write(t, root, "internal/emprestimo/multa.go", "package emprestimo\n\nconst Multa = 2\n")
	git("commit", "-q", "-am", "multa")
	write(t, root, "migrations/001_livros.sql", "create table livros (id bigint, titulo text);\n")
	write(t, root, "internal/emprestimo/reserva.go", "package emprestimo\n")
	return root
}

func diffText(t *testing.T, root, args string) (string, bool) {
	t.Helper()
	got := serve(t, root, call(1, "pipeline_diff", args))
	if len(got) != 1 {
		t.Fatalf("respostas = %v", got)
	}
	res, ok := got[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("sem result: %v", got[0])
	}
	content := res["content"].([]any)[0].(map[string]any)
	isErr, _ := res["isError"].(bool)
	return content["text"].(string), isErr
}

func TestDiff(t *testing.T) {
	root := gitProject(t)
	tests := []struct {
		name    string
		args    string
		isError bool
		want    []string
		unwant  []string
	}{
		{
			name: "diff completo contra base",
			args: `{"base":"main"}`,
			want: []string{
				"Base: main (merge-base ", "Arquivos alterados (3)",
				"internal/emprestimo/multa.go", "migrations/001_livros.sql",
				"internal/emprestimo/reserva.go (não rastreado: leia com Read)",
				"-const Multa = 1", "+const Multa = 2", "+create table livros (id bigint, titulo text);",
			},
			unwant: []string{"diff cortado"},
		},
		{
			name:   "filtro por caminho",
			args:   `{"base":"main","paths":["migrations/"]}`,
			want:   []string{"Arquivos alterados (1)", "migrations/001_livros.sql", "titulo text"},
			unwant: []string{"multa.go", "Multa"},
		},
		{
			name:   "só nomes",
			args:   `{"base":"main","name_only":true}`,
			want:   []string{"Arquivos alterados (3)", "multa.go"},
			unwant: []string{"diff --git", "+const"},
		},
		{
			name: "nada alterado no filtro",
			args: `{"base":"main","paths":["cmd"]}`,
			want: []string{"Nenhum arquivo alterado contra main"},
		},
		{
			name:    "base como opção",
			args:    `{"base":"--output=x"}`,
			isError: true,
			want:    []string{"base"},
		},
		{
			name:    "base inexistente",
			args:    `{"base":"nao-existe"}`,
			isError: true,
			want:    []string{"nao-existe"},
		},
		{
			name:    "caminho fora da raiz",
			args:    `{"base":"main","paths":["../fora"]}`,
			isError: true,
			want:    []string{"caminho"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := diffText(t, root, tt.args)
			if isErr != tt.isError {
				t.Fatalf("isError = %v, quero %v:\n%s", isErr, tt.isError, text)
			}
			for _, w := range tt.want {
				if !strings.Contains(text, w) {
					t.Errorf("texto sem %q:\n%s", w, text)
				}
			}
			for _, w := range tt.unwant {
				if strings.Contains(text, w) {
					t.Errorf("texto com %q:\n%s", w, text)
				}
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Error("--output virou opção do git")
	}
}

func TestDiffBasePadrao(t *testing.T) {
	// Sem base, vale o base_ref da configuração.
	root := gitProject(t)
	cfg := strings.Replace(readFile(t, root, config.FileName), `"version": 1,`, `"version": 1, "base_ref": "main",`, 1)
	write(t, root, config.FileName, cfg)
	text, isErr := diffText(t, root, `{}`)
	if isErr || !strings.Contains(text, "Base: main") || !strings.Contains(text, "+const Multa = 2") {
		t.Errorf("isError = %v:\n%s", isErr, text)
	}
}

func TestDiffCortado(t *testing.T) {
	root := gitProject(t)
	write(t, root, "internal/emprestimo/grande.txt", strings.Repeat("linha de teste do diff grande\n", 4000))
	cmd := exec.Command("git", "add", "internal/emprestimo/grande.txt")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	text, isErr := diffText(t, root, `{"base":"main"}`)
	if isErr || !strings.Contains(text, "diff cortado") || !strings.Contains(text, "paths") {
		t.Errorf("isError = %v, sem aviso de corte:\n%.400s", isErr, text)
	}
	if len(text) > maxDiff+4096 {
		t.Errorf("texto com %d bytes; limite do patch é %d", len(text), maxDiff)
	}
}

func TestDiffSemGitOuConfig(t *testing.T) {
	root := project(t, `["internal/"]`)
	if text, isErr := diffText(t, root, `{"base":"main"}`); !isErr {
		t.Errorf("fora de repositório não deu isError:\n%s", text)
	}
	if err := os.Remove(filepath.Join(root, config.FileName)); err != nil {
		t.Fatal(err)
	}
	if text, isErr := diffText(t, root, `{"base":"main"}`); !isErr || !strings.Contains(text, "doctor") {
		t.Errorf("sem config: isError = %v:\n%s", isErr, text)
	}
	got := serve(t, root, call(2, "pipeline_diff", `{"paths":"internal"}`))
	if err := match(obj(t, `{"id":2,"error":{"code":-32602}}`), got[0]); err != nil {
		t.Errorf("paths com tipo errado: %v", err)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
