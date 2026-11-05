package hook

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

const hookLogRel = ".pipeline-claude/hook-log.jsonl"

// newLoggedProject cria um projeto com hook_log ligado e devolve a raiz.
func newLoggedProject(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	cfg := strings.Replace(testConfig, `"version": 1,`, `"version": 1, "hook_log": "`+hookLogRel+`",`, 1)
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

type logLine struct {
	Time     string `json:"time"`
	Tool     string `json:"tool"`
	Path     string `json:"path"`
	Agent    string `json:"agent"`
	Decision string `json:"decision"`
	Rule     string `json:"rule"`
}

// readLog devolve as linhas do log, conferindo que cada uma é um objeto JSON
// só com as chaves do TD-45.
func readLog(t *testing.T, root string) []logLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(hookLogRel)))
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	var out []logLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var keys map[string]any
		if err := json.Unmarshal(sc.Bytes(), &keys); err != nil {
			t.Fatalf("linha não é JSON: %q", sc.Text())
		}
		if len(keys) != 6 {
			t.Errorf("linha com chaves %v, quer time, tool, path, agent, decision, rule", keys)
		}
		var l logLine
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&l); err != nil {
			t.Fatalf("linha %q: %v", sc.Text(), err)
		}
		ts, err := time.Parse(time.RFC3339, l.Time)
		if err != nil || ts.Location() != time.UTC || !strings.HasSuffix(l.Time, "Z") {
			t.Errorf("time %q não é RFC 3339 em UTC", l.Time)
		}
		out = append(out, l)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Error("log sem quebra de linha no fim")
	}
	return out
}

func TestHookLogDecisoes(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		apply   func(m map[string]any, root string)
		code    int
		want    logLine
	}{
		{
			name: "deny da sessão principal em internal/", fixture: "2.1.309/pretooluse-edit-main.json",
			code: ExitBlock,
			want: logLine{Tool: "Edit", Path: "internal/exemplo/alvo.go", Agent: "", Decision: "deny", Rule: "protected_paths"},
		},
		{
			name: "allow do editor", fixture: "2.1.309/pretooluse-write-dev-implementation.json",
			code: ExitAllow,
			want: logLine{Tool: "Write", Path: "internal/exemplo/novo.go", Agent: "dev-implementation", Decision: "allow", Rule: "editor_agents"},
		},
		{
			name: "allow fora de caminho protegido", fixture: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, root string) { toolInput(m)["file_path"] = root + "/README.md" },
			code:  ExitAllow,
			want:  logLine{Tool: "Write", Path: "README.md", Decision: "allow", Rule: "unprotected"},
		},
		{
			name: "deny em no_agent_paths", fixture: "2.1.309/pretooluse-write-dev-implementation.json",
			apply: func(m map[string]any, root string) { toolInput(m)["file_path"] = root + "/docs-ai/restrito/x.md" },
			code:  ExitBlock,
			want:  logLine{Tool: "Write", Path: "docs-ai/restrito/x.md", Agent: "dev-implementation", Decision: "deny", Rule: "no_agent_paths"},
		},
		{
			name: "NotebookEdit usa notebook_path", fixture: "2.1.309/pretooluse-notebookedit-main.json",
			code: ExitBlock,
			want: logLine{Tool: "NotebookEdit", Path: "docs-ai/exemplo.ipynb", Decision: "deny", Rule: "protected_paths"},
		},
		{
			name: "fora da raiz registra sem caminho", fixture: "2.1.309/pretooluse-write-main.json",
			apply: func(m map[string]any, _ string) {
				toolInput(m)["file_path"] = filepath.ToSlash(filepath.Join(os.TempDir(), "outro-repo", "x.go"))
			},
			code: ExitAllow,
			want: logLine{Tool: "Write", Path: "", Decision: "allow", Rule: "outside_root"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newLoggedProject(t)
			m := loadFixture(t, tc.fixture, root)
			if tc.apply != nil {
				tc.apply(m, root)
			}
			code, msg := run(t, marshal(t, m), getwdErr)
			if code != tc.code {
				t.Fatalf("Run = %d, quer %d\n%s", code, tc.code, msg)
			}
			lines := readLog(t, root)
			if len(lines) != 1 {
				t.Fatalf("log com %d linhas, quer 1: %+v", len(lines), lines)
			}
			got := lines[0]
			got.Time = ""
			if got != tc.want {
				t.Errorf("linha = %+v, quer %+v", got, tc.want)
			}
		})
	}
}

// TestHookLogAcumula: cada decisão acrescenta uma linha; nada é sobrescrito.
func TestHookLogAcumula(t *testing.T) {
	root := newLoggedProject(t)
	for _, f := range []string{"2.1.309/pretooluse-edit-main.json", "2.1.309/pretooluse-write-dev-implementation.json", "2.1.309/pretooluse-edit-main.json"} {
		run(t, marshal(t, loadFixture(t, f, root)), getwdErr)
	}
	lines := readLog(t, root)
	var got []string
	for _, l := range lines {
		got = append(got, l.Decision)
	}
	if strings.Join(got, ",") != "deny,allow,deny" {
		t.Errorf("decisões = %v", got)
	}
}

// TestHookLogSemConteudo: o conteúdo da edição nunca vai para o log.
func TestHookLogSemConteudo(t *testing.T) {
	const secret = "SEGREDO-DA-EDICAO-7f3a"
	root := newLoggedProject(t)
	for _, f := range []string{"2.1.309/pretooluse-edit-main.json", "2.1.309/pretooluse-write-dev-implementation.json", "2.1.309/pretooluse-notebookedit-main.json"} {
		m := loadFixture(t, f, root)
		in := toolInput(m)
		for _, k := range []string{"new_string", "old_string", "content", "new_source"} {
			if _, ok := in[k]; ok {
				in[k] = secret
			}
		}
		run(t, marshal(t, m), getwdErr)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(hookLogRel)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Count(data, []byte("\n")) != 3 {
		t.Errorf("log com conteúdo da edição ou sem 3 linhas:\n%s", data)
	}
}

// TestHookLogProtegido: Edit/Write no próprio log é bloqueado para qualquer
// agente, inclusive editor, e a tentativa é registrada.
func TestHookLogProtegido(t *testing.T) {
	for _, tc := range []struct {
		name, fixture, target string
	}{
		{"editor com Write", "2.1.309/pretooluse-write-dev-implementation.json", hookLogRel},
		{"sessão principal com Edit", "2.1.309/pretooluse-edit-main.json", hookLogRel},
		{"caixa diferente", "2.1.309/pretooluse-write-dev-implementation.json", ".pipeline-claude/HOOK-LOG.jsonl"},
		{"caminho não limpo", "2.1.309/pretooluse-write-dev-implementation.json", ".pipeline-claude/./x/../hook-log.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newLoggedProject(t)
			m := loadFixture(t, tc.fixture, root)
			toolInput(m)["file_path"] = root + "/" + tc.target
			code, msg := run(t, marshal(t, m), getwdErr)
			if code != ExitBlock || !strings.HasPrefix(msg, "BLOQUEADO pelo pipeline-claude") || !strings.Contains(msg, "hook_log") {
				t.Fatalf("Run = %d, quer bloqueio citando hook_log\n%s", code, msg)
			}
			lines := readLog(t, root)
			if len(lines) != 1 || lines[0].Decision != "deny" || lines[0].Rule != "hook_log" {
				t.Errorf("log = %+v", lines)
			}
		})
	}
}

// TestHookLogSemCampo: sem hook_log, nada é gravado.
func TestHookLogSemCampo(t *testing.T) {
	root := newProject(t, t.TempDir())
	for _, f := range []string{"2.1.309/pretooluse-edit-main.json", "2.1.309/pretooluse-write-dev-implementation.json"} {
		run(t, marshal(t, loadFixture(t, f, root)), getwdErr)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != config.FileName {
			t.Errorf("arquivo inesperado na raiz: %s", e.Name())
		}
	}
}

// TestHookLogFalhaBloqueia: sem conseguir gravar o log, até o que seria
// liberado bloqueia (regra 3).
func TestHookLogFalhaBloqueia(t *testing.T) {
	t.Run("pasta sem permissão de escrita", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows não aplica permissão de escrita por modo de arquivo (TD-39)")
		}
		if os.Geteuid() == 0 {
			t.Skip("root ignora a permissão de escrita")
		}
		root := newLoggedProject(t)
		dir := filepath.Join(root, ".pipeline-claude")
		if err := os.Mkdir(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		code, msg := run(t, marshal(t, loadFixture(t, "2.1.309/pretooluse-write-dev-implementation.json", root)), getwdErr)
		if code != ExitBlock || !strings.HasPrefix(msg, "BLOQUEADO pelo pipeline-claude") || !strings.Contains(msg, "hook_log") {
			t.Errorf("Run = %d, quer bloqueio citando hook_log\n%s", code, msg)
		}
	})
	t.Run("caminho do log ocupado por diretório", func(t *testing.T) {
		root := newLoggedProject(t)
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(hookLogRel)), 0o755); err != nil {
			t.Fatal(err)
		}
		code, msg := run(t, marshal(t, loadFixture(t, "2.1.309/pretooluse-write-dev-implementation.json", root)), getwdErr)
		if code != ExitBlock || !strings.HasPrefix(msg, "BLOQUEADO pelo pipeline-claude") {
			t.Errorf("Run = %d, quer bloqueio\n%s", code, msg)
		}
	})
	t.Run("deny continua deny e cita a falha", func(t *testing.T) {
		root := newLoggedProject(t)
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(hookLogRel)), 0o755); err != nil {
			t.Fatal(err)
		}
		code, msg := run(t, marshal(t, loadFixture(t, "2.1.309/pretooluse-edit-main.json", root)), getwdErr)
		if code != ExitBlock || !strings.HasPrefix(msg, "BLOQUEADO pelo pipeline-claude") || !strings.Contains(msg, "hook_log") {
			t.Errorf("Run = %d\n%s", code, msg)
		}
	})
	t.Run("pasta do log é symlink para fora da raiz", func(t *testing.T) {
		root := newLoggedProject(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, ".pipeline-claude")); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
		code, msg := run(t, marshal(t, loadFixture(t, "2.1.309/pretooluse-write-dev-implementation.json", root)), getwdErr)
		if code != ExitBlock || !strings.Contains(msg, "hook_log") {
			t.Errorf("Run = %d, quer bloqueio citando hook_log\n%s", code, msg)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("gravou fora da raiz: %v", entries)
		}
	})
}

// TestHookLogFixtures: com hook_log, as fixtures reais decidem igual e cada
// decisão vira uma linha.
func TestHookLogFixtures(t *testing.T) {
	for rel, want := range fixtureCases {
		if !strings.Contains(rel, "pretooluse") {
			continue
		}
		t.Run(rel, func(t *testing.T) {
			root := newLoggedProject(t)
			code, _ := run(t, marshal(t, loadFixture(t, rel, root)), getwdErr)
			if code != want {
				t.Errorf("Run = %d, quer %d", code, want)
			}
			lines := readLog(t, root)
			wantDecision := "allow"
			if want == ExitBlock {
				wantDecision = "deny"
			}
			if len(lines) != 1 || lines[0].Decision != wantDecision {
				t.Errorf("log = %+v, quer uma linha %s", lines, wantDecision)
			}
		})
	}
}
