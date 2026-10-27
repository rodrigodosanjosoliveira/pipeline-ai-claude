package manifest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("0123456789abcdef", 4)
)

// lockJSON monta um lock válido com files e released no formato do arquivo.
func lockJSON(files, released string) string {
	return `{"version":1,"toolkit_version":"v0.3.0","files":{` + files + `},"released":[` + released + `]}`
}

func writeLock(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func sampleLock() *Lock {
	return &Lock{
		Version:        1,
		ToolkitVersion: "v0.3.0",
		Files: map[string]string{
			".githooks/pre-commit":                  hashB,
			".claude/agents/revisor.md":             hashA,
			".claude/skills/emprestimos/SKILL.md":   hashA,
			".claude/skills/emprestimos/exemplo.md": hashB,
		},
		Released: []string{".claude/skills/acervo/SKILL.md", ".claude/agents/catalogo.md"},
	}
}

const sampleBytes = `{
  "version": 1,
  "toolkit_version": "v0.3.0",
  "files": {
    ".claude/agents/revisor.md": "` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `",
    ".claude/skills/emprestimos/SKILL.md": "` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `",
    ".claude/skills/emprestimos/exemplo.md": "` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `",
    ".githooks/pre-commit": "` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `"
  },
  "released": [
    ".claude/agents/catalogo.md",
    ".claude/skills/acervo/SKILL.md"
  ]
}
`

func TestWriteReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	in := sampleLock()
	if err := Write(root, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != sampleBytes {
		t.Fatalf("bytes gravados:\n%s\nesperado:\n%s", first, sampleBytes)
	}
	got, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := sampleLock()
	want.Released = []string{".claude/agents/catalogo.md", ".claude/skills/acervo/SKILL.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read = %#v, esperado %#v", got, want)
	}

	if err := Write(root, got); err != nil {
		t.Fatalf("segundo Write: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Write não determinístico:\n%s\n×\n%s", first, second)
	}
}

func TestWriteEmptyLock(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, &Lock{Version: 1, ToolkitVersion: "dev"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"version\": 1,\n  \"toolkit_version\": \"dev\",\n  \"files\": {},\n  \"released\": []\n}\n"
	if string(data) != want {
		t.Fatalf("bytes gravados:\n%q\nesperado:\n%q", data, want)
	}
	got, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Files == nil || got.Released == nil {
		t.Fatalf("Read devolveu coleção nil: %#v", got)
	}
	if len(got.Files) != 0 || len(got.Released) != 0 {
		t.Fatalf("Read = %#v, esperado vazio", got)
	}
}

func TestWriteDedupesReleasedWithoutMutatingInput(t *testing.T) {
	root := t.TempDir()
	in := &Lock{
		Version:        1,
		ToolkitVersion: "v0.3.0",
		Released:       []string{".githooks/pre-push", ".claude/agents/b.md", ".githooks/pre-push", ".claude/agents/b.md"},
	}
	orig := append([]string(nil), in.Released...)
	if err := Write(root, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !reflect.DeepEqual(in.Released, orig) {
		t.Fatalf("Write alterou a entrada: %v", in.Released)
	}
	got, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{".claude/agents/b.md", ".githooks/pre-push"}
	if !reflect.DeepEqual(got.Released, want) {
		t.Fatalf("released = %v, esperado %v", got.Released, want)
	}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("\r")) || !bytes.HasSuffix(data, []byte("}\n")) {
		t.Fatalf("fim de linha inesperado: %q", data)
	}
}

func TestReadNormalizesReleased(t *testing.T) {
	root := writeLock(t, lockJSON(``, `".githooks/pre-push",".claude/agents/b.md",".githooks/pre-push"`))
	got, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{".claude/agents/b.md", ".githooks/pre-push"}
	if !reflect.DeepEqual(got.Released, want) {
		t.Fatalf("released = %v, esperado %v", got.Released, want)
	}
}

func TestReadAccepts(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"mínimo", `{"version":1,"toolkit_version":"dev"}`},
		{"coleções null", `{"version":1,"toolkit_version":"dev","files":null,"released":null}`},
		{"CRLF e espaços", "{\r\n  \"version\": 1,\r\n  \"toolkit_version\": \"dev\"\r\n}\r\n"},
		{"arquivo fundo na área", lockJSON(`".claude/skills/acervo/refs/a/b.md":"`+hashA+`"`, ``)},
		{"nome com ponto e espaço", lockJSON(`".claude/agents/..revisor v2.md":"`+hashA+`"`, ``)},
		{"githooks", lockJSON(`".githooks/commit-msg":"`+hashB+`"`, `".githooks/pre-push"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Read(writeLock(t, tt.content)); err != nil {
				t.Fatalf("Read: %v", err)
			}
		})
	}
}

func TestReadErrors(t *testing.T) {
	file := func(p string) string { return lockJSON(`"`+p+`":"`+hashA+`"`, ``) }
	released := func(p string) string { return lockJSON(``, `"`+p+`"`) }
	tests := []struct {
		name    string
		content string
		want    string // trecho da mensagem
	}{
		{"vazio", ``, "JSON inválido"},
		{"não é objeto", `[]`, "JSON inválido"},
		{"null", `null`, "version"},
		{"JSON truncado", `{"version":1,`, "JSON inválido"},
		{"campo desconhecido", `{"version":1,"toolkit_version":"dev","extra":true}`, "extra"},
		{"conteúdo depois do objeto", `{"version":1,"toolkit_version":"dev"} {}`, "depois do objeto"},
		{"lixo depois do objeto", `{"version":1,"toolkit_version":"dev"}x`, "JSON inválido"},
		{"chave repetida no topo", `{"version":2,"version":1,"toolkit_version":"dev"}`, "repetida"},
		{"chave repetida em files", lockJSON(`".githooks/a":"`+hashA+`",".githooks/a":"`+hashB+`"`, ``), "repetida"},
		{"version ausente", `{"toolkit_version":"dev"}`, "version"},
		{"version 0", `{"version":0,"toolkit_version":"dev"}`, "version"},
		{"version 2", `{"version":2,"toolkit_version":"dev"}`, "version"},
		{"version texto", `{"version":"1","toolkit_version":"dev"}`, "JSON inválido"},
		{"toolkit_version ausente", `{"version":1}`, "toolkit_version"},
		{"toolkit_version vazio", `{"version":1,"toolkit_version":""}`, "toolkit_version"},
		{"toolkit_version em branco", `{"version":1,"toolkit_version":"  "}`, "toolkit_version"},
		{"hash maiúsculo", lockJSON(`".githooks/a":"`+strings.ToUpper(hashB)+`"`, ``), "hash"},
		{"hash curto", lockJSON(`".githooks/a":"`+hashA[:63]+`"`, ``), "hash"},
		{"hash longo", lockJSON(`".githooks/a":"`+hashA+`a"`, ``), "hash"},
		{"hash não hex", lockJSON(`".githooks/a":"`+strings.Repeat("g", 64)+`"`, ``), "hash"},
		{"hash vazio", lockJSON(`".githooks/a":""`, ``), "hash"},
		{"caminho vazio", file(``), "caminho"},
		{"caminho absoluto", file(`/.githooks/a`), "caminho"},
		{"letra de drive", file(`C:/.githooks/a`), "caminho"},
		{"letra de drive sem barra", file(`c:.githooks/a`), "caminho"},
		{"barra invertida", file(`.githooks\\a`), "caminho"},
		{"sobe com ..", file(`.githooks/../.githooks/a`), "caminho"},
		{"sai da raiz", file(`../.githooks/a`), "caminho"},
		{"ponto no meio", file(`.githooks/./a`), "caminho"},
		{"prefixo ./", file(`./.githooks/a`), "caminho"},
		{"barra dupla", file(`.githooks//a`), "caminho"},
		{"barra final", file(`.githooks/a/`), "caminho"},
		{"só a área", file(`.githooks/`), "caminho"},
		{"área sem barra", file(`.githooks`), "caminho"},
		{"fora da área", file(`CLAUDE.md`), "caminho"},
		{"docs-ai é do projeto", file(`docs-ai/README.md`), "caminho"},
		{"settings é do projeto", file(`.claude/settings.json`), "caminho"},
		{"caixa diferente", file(`.Claude/agents/a.md`), "caminho"},
		{"caixa diferente githooks", file(`.GITHOOKS/a`), "caminho"},
		{"caractere de controle", file(`.githooks/a\u0000`), "caminho"},
		{"released fora da área", released(`go.mod`), "caminho"},
		{"released com ..", released(`.githooks/../x`), "caminho"},
		{"released vazio", released(``), "caminho"},
		{"mesmo caminho em files e released", lockJSON(`".githooks/a":"`+hashA+`"`, `".githooks/a"`), "files e released"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Read(writeLock(t, tt.content))
			if err == nil {
				t.Fatalf("Read aceitou: %#v", got)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("erro não pode ser ErrNotFound: %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("erro %q não contém %q", err, tt.want)
			}
		})
	}
}

func TestReadNotFound(t *testing.T) {
	_, err := Read(t.TempDir())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("erro = %v, esperado ErrNotFound", err)
	}
}

func TestReadNotRegular(t *testing.T) {
	t.Run("diretório", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, FileName), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Read(root)
		if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "arquivo regular") {
			t.Fatalf("erro = %v, esperado arquivo não regular", err)
		}
	})
	t.Run("symlink para lock válido", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "outro.lock")
		if err := os.WriteFile(target, []byte(`{"version":1,"toolkit_version":"dev"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, FileName)); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
		_, err := Read(root)
		if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "arquivo regular") {
			t.Fatalf("erro = %v, esperado arquivo não regular", err)
		}
	})
	t.Run("symlink quebrado", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "nada"), filepath.Join(root, FileName)); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
		_, err := Read(root)
		if err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("erro = %v, esperado arquivo não regular", err)
		}
	})
}

func TestWriteRejectsInvalid(t *testing.T) {
	valid := func(mod func(*Lock)) *Lock {
		l := sampleLock()
		mod(l)
		return l
	}
	tests := []struct {
		name string
		lock *Lock
		want string
	}{
		{"nil", nil, "lock"},
		{"version 0", valid(func(l *Lock) { l.Version = 0 }), "version"},
		{"version 2", valid(func(l *Lock) { l.Version = 2 }), "version"},
		{"toolkit_version vazio", valid(func(l *Lock) { l.ToolkitVersion = "" }), "toolkit_version"},
		{"hash inválido", valid(func(l *Lock) { l.Files[".githooks/pre-commit"] = "x" }), "hash"},
		{"caminho fora da área", valid(func(l *Lock) { l.Files["Makefile"] = hashA }), "caminho"},
		{"caminho sujo", valid(func(l *Lock) { l.Files[".claude/agents/../x.md"] = hashA }), "caminho"},
		{"released fora da área", valid(func(l *Lock) { l.Released = append(l.Released, "/etc/passwd") }), "caminho"},
		{"files e released", valid(func(l *Lock) { l.Released = append(l.Released, ".githooks/pre-commit") }), "files e released"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			err := Write(root, tt.lock)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("erro = %v, esperado trecho %q", err, tt.want)
			}
			entries, rerr := os.ReadDir(root)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(entries) != 0 {
				t.Fatalf("Write deixou arquivos com lock inválido: %v", entries)
			}
		})
	}
}

func TestWriteKeepsOldLockOnInvalid(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, sampleLock()); err != nil {
		t.Fatal(err)
	}
	bad := sampleLock()
	bad.Files["README.md"] = hashA
	if err := Write(root, bad); err == nil {
		t.Fatal("Write aceitou lock inválido")
	}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sampleBytes {
		t.Fatalf("lock anterior alterado:\n%s", data)
	}
}

func TestWriteNotRegular(t *testing.T) {
	t.Run("diretório", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, FileName), 0o755); err != nil {
			t.Fatal(err)
		}
		err := Write(root, sampleLock())
		if err == nil || !strings.Contains(err.Error(), "arquivo regular") {
			t.Fatalf("erro = %v, esperado arquivo não regular", err)
		}
		assertNoTemp(t, root)
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "alvo")
		if err := os.WriteFile(target, []byte("intacto"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, FileName)); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
		err := Write(root, sampleLock())
		if err == nil || !strings.Contains(err.Error(), "arquivo regular") {
			t.Fatalf("erro = %v, esperado arquivo não regular", err)
		}
		data, rerr := os.ReadFile(target)
		if rerr != nil || string(data) != "intacto" {
			t.Fatalf("alvo do symlink alterado: %q, %v", data, rerr)
		}
		assertNoTemp(t, root)
	})
}

func TestWriteMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "não-existe")
	if err := Write(root, sampleLock()); err == nil {
		t.Fatal("Write aceitou raiz inexistente")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Write criou a raiz: %v", err)
	}
}

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, &Lock{Version: 1, ToolkitVersion: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, sampleLock()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sampleBytes {
		t.Fatalf("lock não substituído:\n%s", data)
	}
	assertNoTemp(t, root)
}

func assertNoTemp(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName {
			t.Fatalf("arquivo inesperado na raiz: %s", e.Name())
		}
	}
}

func TestHash(t *testing.T) {
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := Hash(nil); got != empty {
		t.Fatalf("Hash(nil) = %s, esperado %s", got, empty)
	}
	lf := []byte("# Revisor\n\nRevisa empréstimos.\n")
	crlf := []byte("# Revisor\r\n\r\nRevisa empréstimos.\r\n")
	if Hash(lf) != Hash(crlf) {
		t.Fatal("Hash difere entre LF e CRLF")
	}
	if Hash(lf) == Hash([]byte("# Revisor\n\nRevisa devoluções.\n")) {
		t.Fatal("Hash igual para conteúdo diferente")
	}
	if Hash([]byte("a\rb")) == Hash([]byte("a\nb")) {
		t.Fatal("CR sozinho não pode virar LF")
	}
	if Hash([]byte("a\r\n")) == Hash([]byte("a")) {
		t.Fatal("CRLF não pode sumir")
	}
	h := Hash(crlf)
	if len(h) != 64 || strings.ToLower(h) != h {
		t.Fatalf("Hash fora do formato: %q", h)
	}
}

func TestManagedArea(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{".claude/agents/revisor.md", true},
		{".claude/skills/acervo/SKILL.md", true},
		{".githooks/pre-commit", true},
		{".githooks/x/y", true},
		{".claude/agents/", false},
		{".claude/skills/", false},
		{".githooks/", false},
		{".claude/agents", false},
		{".githooks", false},
		{".Claude/agents/revisor.md", false},
		{".claude/Agents/revisor.md", false},
		{".GITHOOKS/pre-commit", false},
		{".claude/settings.json", false},
		{".claude/agentsx/a.md", false},
		{"CLAUDE.md", false},
		{"docs-ai/README.md", false},
		{"x/.githooks/pre-commit", false},
		{"/.githooks/pre-commit", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ManagedArea(tt.rel); got != tt.want {
			t.Errorf("ManagedArea(%q) = %v, esperado %v", tt.rel, got, tt.want)
		}
	}
}

func FuzzRead(f *testing.F) {
	f.Add([]byte(sampleBytes))
	f.Add([]byte(`{"version":1,"toolkit_version":"dev"}`))
	f.Add([]byte(lockJSON(`".githooks/a":"`+hashA+`"`, `".githooks/b",".githooks/b",".claude/agents/c.md"`)))
	f.Add([]byte(lockJSON(`".githooks/a":"`+hashA+`"`, `".githooks/a"`)))
	f.Add([]byte(lockJSON(`".claude/agents/../x":"`+hashA+`"`, ``)))
	f.Add([]byte(`{"version":1,"toolkit_version":"<&>\u2028"}`))
	f.Add([]byte(`{"version":1,`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, FileName), data, 0o644); err != nil {
			t.Fatal(err)
		}
		l, err := Read(root)
		if err != nil {
			if l != nil {
				t.Fatal("Read devolveu lock junto com erro")
			}
			return
		}
		if err := Write(root, l); err != nil {
			t.Fatalf("lock aceito reprovou no Write: %v", err)
		}
		first, err := os.ReadFile(filepath.Join(root, FileName))
		if err != nil {
			t.Fatal(err)
		}
		back, err := Read(root)
		if err != nil {
			t.Fatalf("lock gravado reprovou no Read: %v\n%s", err, first)
		}
		if !reflect.DeepEqual(l, back) {
			t.Fatalf("ida e volta diferente:\n%#v\n×\n%#v", l, back)
		}
		if err := Write(root, back); err != nil {
			t.Fatal(err)
		}
		second, err := os.ReadFile(filepath.Join(root, FileName))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("Write não determinístico:\n%s\n×\n%s", first, second)
		}
	})
}
