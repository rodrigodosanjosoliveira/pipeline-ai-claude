package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunConverteENaoSobrescreve(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	root := "/home/fulano/workspace/produto"
	payload := `{"session_id":"x","cwd":"` + root + `","hook_event_name":"PreToolUse","tool_name":"Edit",
"tool_input":{"file_path":"` + root + `/internal/exemplo/alvo.go","old_string":"a","new_string":"b"}}`
	for _, n := range []string{"1.json", "2.json"} {
		if err := os.WriteFile(filepath.Join(in, n), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root, "-home", "/home/fulano", "-in", in, "-out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("código %d, stderr: %s", code, stderr.String())
	}
	for _, n := range []string{"pretooluse-edit-main.json", "pretooluse-edit-main-2.json"} {
		data, err := os.ReadFile(filepath.Join(out, n))
		if err != nil {
			t.Fatalf("esperava %s: %v", n, err)
		}
		if strings.Contains(string(data), "fulano") {
			t.Errorf("%s não sanitizado: %s", n, data)
		}
	}
}

func TestRunFlagsObrigatorias(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-in", "x"}, &stdout, &stderr); code != 2 {
		t.Fatalf("código %d, quer 2", code)
	}
}

func TestRunPayloadInvalidoFalha(t *testing.T) {
	in := t.TempDir()
	if err := os.WriteFile(filepath.Join(in, "ruim.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", "/x", "-in", in, "-out", t.TempDir()}, &stdout, &stderr); code != 1 {
		t.Fatalf("código %d, quer 1", code)
	}
}
