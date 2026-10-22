package fixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rawPreToolUse = `{
  "session_id": "8f1c2d3e-aaaa-bbbb-cccc-123456789abc",
  "transcript_path": "/home/fulano/.claude/projects/x/abc.jsonl",
  "cwd": "/home/fulano/workspace/produto",
  "scratchpad_dir": "/tmp/claude-1000/-home-fulano-workspace-produto/8f1c2d3e/scratchpad",
  "hook_event_name": "PreToolUse",
  "tool_name": "MultiEdit",
  "agent_type": "dev-implementation",
  "agent_id": "a1b2c3",
  "tool_use_id": "toolu_ABCDEF",
  "tool_input": {
    "file_path": "/home/fulano/workspace/produto/internal/exemplo/alvo.go",
    "edits": [{"old_string": "segredo do produto", "new_string": "outro segredo"}]
  }
}`

func TestSanitize(t *testing.T) {
	out, name, err := Sanitize([]byte(rawPreToolUse), Options{
		Root: "/home/fulano/workspace/produto",
		Home: "/home/fulano",
	})
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	s := string(out)
	for _, proibido := range []string{"fulano", "/tmp/", "produto\"", "segredo", "8f1c2d3e", "a1b2c3", "toolu_ABCDEF"} {
		if strings.Contains(s, proibido) {
			t.Errorf("saída ainda contém %q:\n%s", proibido, s)
		}
	}
	for _, esperado := range []string{
		`"file_path": "/workspace/exemplo/internal/exemplo/alvo.go"`,
		`"cwd": "/workspace/exemplo"`,
		`"session_id": "` + PlaceholderSessionID + `"`,
		`"scratchpad_dir": "` + PlaceholderScratchpad + `"`,
		`"old_string": "` + PlaceholderContent + `"`,
		`"agent_type": "dev-implementation"`,
	} {
		if !strings.Contains(s, esperado) {
			t.Errorf("saída não contém %s:\n%s", esperado, s)
		}
	}
	if name != "pretooluse-multiedit-dev-implementation.json" {
		t.Errorf("nome = %q", name)
	}
	if probs := Check(out, nil); len(probs) != 0 {
		t.Errorf("saída sanitizada reprovada por Check: %v", probs)
	}
}

func TestSanitizeErros(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		opts Options
	}{
		{"root relativo", rawPreToolUse, Options{Root: "workspace/produto"}},
		{"root vazio", rawPreToolUse, Options{}},
		{"json inválido", `{"tool_name":`, Options{Root: "/x"}},
		{"não é objeto", `[1,2]`, Options{Root: "/x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Sanitize([]byte(tt.raw), tt.opts); err == nil {
				t.Fatal("esperava erro")
			}
		})
	}
}

func TestReplacePrefixAllNaoTrocaNomeParcial(t *testing.T) {
	got := replacePrefixAll("/a/bc/x e /a/b/y e /a/b", "/a/b", "/R")
	want := "/a/bc/x e /R/y e /R"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		blocklist []string
		wantProbs int
	}{
		{"caminho real", `{"cwd": "/home/fulano/x"}`, nil, 1},
		{"session real", `{"session_id": "abc"}`, nil, 1},
		{"scratchpad real", `{"scratchpad_dir": "/tmp/claude-1000/x"}`, nil, 1},
		{"conteúdo presente", `{"tool_input": {"content": "package main"}}`, nil, 1},
		{"termo bloqueado", `{"cwd": "/workspace/exemplo/NomeInterno"}`, []string{"# comentário", "nomeinterno"}, 1},
		{"válido", `{"cwd": "/workspace/exemplo", "tool_input": {"file_path": "/workspace/exemplo/a.go", "content": "<removido>"}}`, nil, 0},
		{"traversal fora do placeholder", `{"cwd": "/workspace/exemplo/../outro"}`, nil, 1},
		{"json inválido", `{`, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check([]byte(tt.raw), tt.blocklist)
			if len(got) != tt.wantProbs {
				t.Errorf("problemas = %v, quer %d", got, tt.wantProbs)
			}
		})
	}
}

// TestFixturesVersionadas varre testdata/hooks e reprova qualquer fixture que
// não esteja sanitizada. A lista privada de termos bloqueados é lida de
// PIPELINE_CLAUDE_FIXTURE_BLOCKLIST (arquivo, um termo por linha) quando
// definida; no CI público só as regras estruturais rodam.
func TestFixturesVersionadas(t *testing.T) {
	var blocklist []string
	if p := os.Getenv("PIPELINE_CLAUDE_FIXTURE_BLOCKLIST"); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("PIPELINE_CLAUDE_FIXTURE_BLOCKLIST definido, mas ilegível: %v", err)
		}
		blocklist = strings.Split(string(data), "\n")
	} else {
		t.Log("PIPELINE_CLAUDE_FIXTURE_BLOCKLIST não definido: só regras estruturais")
	}

	root := filepath.Join("..", "..", "testdata", "hooks")
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".json" {
			return err
		}
		n++
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, prob := range Check(raw, blocklist) {
			t.Errorf("%s %s", p, prob)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d fixture(s) verificada(s)", n)
}
