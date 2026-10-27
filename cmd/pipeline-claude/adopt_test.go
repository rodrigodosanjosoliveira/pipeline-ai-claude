package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// customReviewer é um subagente do projeto, diferente do template e fora da
// regra do TD-29 (revisor com Edit).
const customReviewer = `---
name: code-reviewer
description: Revisor da biblioteca do bairro, com as regras locais de empréstimo.
tools:
  - Read
  - Grep
  - Edit
---

Revise o diff contra as regras de empréstimo da biblioteca.
`

const otherHookSettings = `{"hooks": {"PreToolUse": [{"matcher": "Edit|Write",
 "hooks": [{"type": "command", "command": "sh scripts/bloqueia.sh"}]}]}}
`

// adoptProject cria um projeto Go existente, fictício, com git limpo.
func adoptProject(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git indisponível")
	}
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/biblioteca-bairro\n\ngo 1.24\n")
	writeFile(t, root, "cmd/biblioteca-bairro/main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, root, ".claude/agents/code-reviewer.md", customReviewer)
	writeFile(t, root, ".claude/settings.json", otherHookSettings)
	writeFile(t, root, ".gitignore", "/bin/\n")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "git@gitlab.com:exemplo/biblioteca-bairro.git"},
		{"add", "-A"},
		{"commit", "-q", "-m", "projeto existente"},
	} {
		gitIn(t, root, args...)
	}
	return root
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=Exemplo", "-c", "user.email=exemplo@exemplo.invalid", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

type adoptOut struct {
	OK    bool `json:"ok"`
	Items []struct {
		Path   string `json:"path"`
		Action string `json:"action"`
	} `json:"items"`
	Instructions []struct {
		Path    string `json:"path"`
		Reason  string `json:"reason"`
		Snippet string `json:"snippet"`
	} `json:"instructions"`
	Created []string `json:"created"`
}

func (o adoptOut) action(path string) string {
	for _, it := range o.Items {
		if it.Path == path {
			return it.Action
		}
	}
	return ""
}

func (o adoptOut) snippet(path string) string {
	for _, in := range o.Instructions {
		if in.Path == path {
			return in.Snippet
		}
	}
	return ""
}

func TestE2EAdopt(t *testing.T) {
	root := adoptProject(t)
	const agent = ".claude/agents/code-reviewer.md"

	// --dry-run: plano e instruções, nada gravado.
	code, stdout, stderr := runBin(t, root, "", "adopt", "--dry-run", "--json")
	if code != exitOK {
		t.Fatalf("adopt --dry-run: código %d\n%s", code, stderr)
	}
	var plan adoptOut
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("saída não é JSON: %v\n%s", err, stdout)
	}
	if !plan.OK || plan.action(agent) != "do-projeto" || plan.action(".pipeline-claude.json") != "criar" ||
		plan.action(".claude/settings.json") != "manter" || len(plan.Created) != 0 {
		t.Fatalf("plano: %+v", plan)
	}
	if plan.snippet("Makefile") == "" || !strings.Contains(plan.snippet(".claude/settings.json"), "pipeline-claude") {
		t.Errorf("instruções sem Makefile ou settings.json: %+v", plan.Instructions)
	}
	if st := gitIn(t, root, "status", "--porcelain"); st != "" {
		t.Fatalf("dry-run gravou:\n%s", st)
	}

	// adopt: cria o resto e o lock; o subagente do projeto fica igual.
	code, stdout, stderr = runBin(t, root, "", "adopt")
	if code != exitOK {
		t.Fatalf("adopt: código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"[criar]", "[do-projeto]", agent, "Instruções", "Makefile", "make tools", "bin/pipeline-claude doctor"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("saída sem %q:\n%s", want, stdout)
		}
	}
	if got := readFile(t, root, agent); got != customReviewer {
		t.Error("adopt alterou o subagente do projeto")
	}
	if got := readFile(t, root, ".claude/settings.json"); got != otherHookSettings {
		t.Error("adopt alterou o settings.json")
	}
	var lock struct {
		Files    map[string]string `json:"files"`
		Released []string          `json:"released"`
	}
	if err := json.Unmarshal([]byte(readFile(t, root, e2eLock)), &lock); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(lock.Released, agent) || lock.Files[".claude/agents/dev-implementation.md"] == "" {
		t.Errorf("lock: %+v", lock)
	}

	// O projeto aplica a instrução do Makefile (versão do binário) e faz o
	// commit da adoção.
	writeFile(t, root, "Makefile", plan.snippet("Makefile"))
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "adota a pipeline")

	t.Run("doctor", func(t *testing.T) {
		_, stdout, _ := runBin(t, root, "", "doctor", "--json")
		var out struct {
			Checks []struct{ Name, Status, Message string } `json:"checks"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("doctor --json: %v\n%s", err, stdout)
		}
		got := map[string]string{}
		for _, c := range out.Checks {
			got[c.Name] = c.Status + ": " + c.Message
		}
		for name, want := range map[string]string{"configuração": "ok: ", "sync": "ok: ", "subagentes": "aviso: "} {
			if !strings.HasPrefix(got[name], want) {
				t.Errorf("doctor %s = %q, quero %q…", name, got[name], want)
			}
		}
		if !strings.Contains(got["subagentes"], "code-reviewer: Edit") {
			t.Errorf("subagentes não aponta o revisor do projeto: %q", got["subagentes"])
		}
	})

	t.Run("sync depois do adopt não muda nada", func(t *testing.T) {
		code, out := runSyncJSON(t, root)
		if code != exitOK || out.action(agent) != "liberado" {
			t.Fatalf("código %d, %+v", code, out)
		}
		if st := gitIn(t, root, "status", "--porcelain"); st != "" {
			t.Errorf("sync mudou o projeto adotado:\n%s", st)
		}
	})

	t.Run("segundo adopt é uso inválido", func(t *testing.T) {
		code, _, stderr := runBin(t, root, "", "adopt")
		if code != exitBlocked || !strings.Contains(stderr, ".pipeline-claude.lock") {
			t.Errorf("código %d, stderr %q", code, stderr)
		}
	})
}

func TestE2EAdoptPreconditions(t *testing.T) {
	t.Run("working tree sujo", func(t *testing.T) {
		root := adoptProject(t)
		writeFile(t, root, "rascunho.txt", "não commitado\n")
		code, _, stderr := runBin(t, root, "", "adopt")
		if code != exitBlocked || stderr == "" {
			t.Errorf("código %d, stderr %q", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(root, e2eLock)); err == nil {
			t.Error("adopt com working tree sujo gravou o lock")
		}
	})
	t.Run("sem go.mod", func(t *testing.T) {
		code, _, stderr := runBin(t, t.TempDir(), "", "adopt")
		if code != exitBlocked || !strings.Contains(stderr, "go.mod") {
			t.Errorf("código %d, stderr %q", code, stderr)
		}
	})
	t.Run("--dir", func(t *testing.T) {
		root := adoptProject(t)
		code, _, stderr := runBin(t, t.TempDir(), "", "adopt", "--dir", root, "--dry-run")
		if code != exitOK {
			t.Errorf("código %d, stderr %q", code, stderr)
		}
	})
}
