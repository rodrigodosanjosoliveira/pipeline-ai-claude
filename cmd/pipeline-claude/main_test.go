package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "sem argumentos é uso inválido", args: nil, wantCode: exitBlocked, wantStderr: "Uso: pipeline-claude"},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantStdout: "pipeline-claude "},
		{name: "--version", args: []string{"--version"}, wantCode: exitOK, wantStdout: "pipeline-claude "},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: "Códigos de saída"},
		{name: "comando desconhecido", args: []string{"xpto"}, wantCode: exitBlocked, wantStderr: `comando desconhecido "xpto"`},
		// Antes: "hook não implementado bloqueia". Agora o hook existe e
		// bloqueia por falha fechada: stdin vazio não é payload (TD-08).
		{name: "hook com stdin vazio bloqueia", args: []string{"hook"}, wantCode: exitBlocked, wantStderr: "payload"},
		{name: "hook com argumento extra", args: []string{"hook", "x"}, stdin: "{}", wantCode: exitBlocked, wantStderr: "hook"},
		{name: "init não implementado", args: []string{"init"}, wantCode: exitBlocked, wantStderr: "Fase 3"},
		{name: "fmt-hook não implementado", args: []string{"fmt-hook"}, wantCode: exitBlocked, wantStderr: "Fase 2"},
		{name: "validate sem alvo", args: []string{"validate"}, wantCode: exitBlocked, wantStderr: "validate"},
		{name: "validate com dois alvos", args: []string{"validate", "A", "B"}, wantCode: exitBlocked, wantStderr: "um alvo"},
		{name: "validate com opção desconhecida", args: []string{"validate", "--xpto", "A"}, wantCode: exitBlocked, wantStderr: "xpto"},
		{name: "validate --base sem valor", args: []string{"validate", "A", "--base"}, wantCode: exitBlocked, wantStderr: "base"},
		{name: "validate -h", args: []string{"validate", "-h"}, wantCode: exitOK, wantStderr: "no-git"},
		{name: "doctor com argumento", args: []string{"doctor", "x"}, wantCode: exitBlocked, wantStderr: "doctor"},
		{name: "doctor com opção desconhecida", args: []string{"doctor", "--xpto"}, wantCode: exitBlocked, wantStderr: "xpto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr)
			if got != tt.wantCode {
				t.Fatalf("código = %d, quer %d (stderr: %q)", got, tt.wantCode, stderr.String())
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, quer conter %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, quer conter %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestPlannedCoversUsage(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for cmd := range planned {
		if !strings.Contains(buf.String(), cmd) {
			t.Errorf("comando previsto %q não aparece na ajuda", cmd)
		}
	}
	for _, cmd := range []string{"hook", "validate", "doctor"} {
		if _, ok := planned[cmd]; ok {
			t.Errorf("%q implementado ainda está em planned", cmd)
		}
		if !strings.Contains(buf.String(), cmd) {
			t.Errorf("comando %q não aparece na ajuda", cmd)
		}
	}
}

func TestParseValidate(t *testing.T) {
	// Opções antes ou depois do alvo.
	tests := []struct {
		args   []string
		base   string
		noGit  bool
		asJSON bool
		target string
	}{
		{[]string{"--no-git", "DEL-001"}, "", true, false, "DEL-001"},
		{[]string{"DEL-001", "--no-git", "--json"}, "", true, true, "DEL-001"},
		{[]string{"--base", "origin/dev", "DEL-001"}, "origin/dev", false, false, "DEL-001"},
		{[]string{"DEL-001", "--base=origin/dev"}, "origin/dev", false, false, "DEL-001"},
		{[]string{"--", "-DEL"}, "", false, false, "-DEL"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		opts, code, ok := parseValidate(tt.args, &stderr)
		if !ok {
			t.Errorf("parseValidate(%q) falhou (%d): %s", tt.args, code, stderr.String())
			continue
		}
		if opts.BaseRef != tt.base || opts.NoGit != tt.noGit || opts.JSON != tt.asJSON || opts.Target != tt.target {
			t.Errorf("parseValidate(%q) = %+v", tt.args, opts)
		}
	}
}

// Ponta a ponta com o binário compilado.

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pipeline-claude-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	name := "pipeline-claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binPath = filepath.Join(dir, name)
	build := exec.Command(goTool(), "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// goTool prefere o go do toolchain que roda os testes.
func goTool() string {
	if root := runtime.GOROOT(); root != "" {
		p := filepath.Join(root, "bin", "go")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "go"
}

// runBin roda o binário em dir com stdin e devolve código, stdout e stderr.
func runBin(t *testing.T, dir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("rodar binário: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

const e2eConfig = `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "delivery_id_pattern": "^DEL-[0-9]{3}$",
 "protected_paths": ["internal/", "docs-ai/"],
 "no_agent_paths": ["docs-ai/restrito/"],
 "editor_agents": ["dev-implementation"]}`

const e2eReport = `---
delivery_id: DEL-001
status: done
---

## PM/Triage
- Escopo fechado: multa por atraso na devolução de livros
- Critérios de aceite: multa zero dentro do prazo
- Riscos: arredondamento de centavos

## Architecture/Guardrails
- Invariantes aplicáveis: valores monetários em centavos
- Arquivos prováveis de impacto: internal/emprestimo/multa.go

## Dev
- Arquivos alterados: internal/emprestimo/multa.go
- Resumo técnico: tabela cobrindo 6 casos
- Testes criados/ajustados: TestMulta
- Evidências (código/teste): go test ./internal/emprestimo/ ok

## QA
- Resultado: aprovado
- Cenários executados: devolução no prazo e com atraso

## Docs
- Docs atualizados: docs/emprestimo.md

## Final Checklist
- [x] build passa
`

// e2eProject cria um projeto fictício (biblioteca de bairro) sem git.
func e2eProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	del2 := strings.Replace(strings.Replace(e2eReport, "DEL-001", "DEL-002", 1), "- [x] build passa", "- [ ] build passa", 1)
	files := map[string]string{
		".pipeline-claude.json":                e2eConfig,
		"docs-ai/deliveries/INDEX.md":          "| ID |\n|---|\n| DEL-001 |\n| DEL-002 |\n",
		"docs-ai/deliveries/DEL-001/report.md": e2eReport,
		"docs-ai/deliveries/DEL-002/report.md": del2,
		"docs-ai/deliveries/DEL-003/report.md": "sem front matter\n",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fixture lê uma fixture real e troca a raiz sanitizada pela raiz do projeto.
func fixture(t *testing.T, name, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "claude", "2.1.309", name))
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(filepath.ToSlash(root))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "/workspace/exemplo", strings.Trim(string(quoted), `"`))
}

func TestE2EHook(t *testing.T) {
	tests := []struct {
		fixture string
		want    int
	}{
		{"pretooluse-edit-main.json", exitBlocked},
		{"pretooluse-write-pm-triage.json", exitBlocked},
		{"pretooluse-notebookedit-main.json", exitBlocked},
		{"pretooluse-write-dev-implementation.json", exitOK},
		{"posttooluse-write-dev-implementation.json", exitBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			root := e2eProject(t)
			code, stdout, stderr := runBin(t, root, fixture(t, tt.fixture, root), "hook")
			if code != tt.want {
				t.Errorf("código = %d, quer %d\nstderr:\n%s", code, tt.want, stderr)
			}
			if stdout != "" {
				t.Errorf("hook escreveu em stdout: %q", stdout)
			}
			if code == exitBlocked && stderr == "" {
				t.Error("bloqueio sem mensagem em stderr")
			}
		})
	}
	t.Run("payload inválido", func(t *testing.T) {
		code, _, _ := runBin(t, e2eProject(t), "isto não é JSON", "hook")
		if code != exitBlocked {
			t.Errorf("código = %d, quer 2", code)
		}
	})
}

func TestE2EValidate(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		want   int
		stdout string
		stderr string
	}{
		{"report válido", []string{"validate", "--no-git", "DEL-001"}, exitOK, "APROVADO", ""},
		{"report válido por caminho", []string{"validate", "docs-ai/deliveries/DEL-001/report.md", "--no-git"}, exitOK, "APROVADO", ""},
		{"checklist aberto", []string{"validate", "--no-git", "DEL-002"}, exitFail, "REPROVADO", ""},
		{"report ilegível", []string{"validate", "--no-git", "DEL-003"}, exitFail, "front matter", ""},
		{"alvo inexistente", []string{"validate", "--no-git", "DEL-404"}, exitBlocked, "", "DEL-404"},
		{"sem git reprova com dica", []string{"validate", "DEL-001"}, exitFail, "--no-git", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := e2eProject(t)
			code, stdout, stderr := runBin(t, root, "", tt.args...)
			if code != tt.want {
				t.Fatalf("código = %d, quer %d\nstdout:\n%s\nstderr:\n%s", code, tt.want, stdout, stderr)
			}
			if !strings.Contains(stdout, tt.stdout) || !strings.Contains(stderr, tt.stderr) {
				t.Errorf("saída sem %q/%q\nstdout:\n%s\nstderr:\n%s", tt.stdout, tt.stderr, stdout, stderr)
			}
		})
	}
	t.Run("json", func(t *testing.T) {
		code, stdout, _ := runBin(t, e2eProject(t), "", "validate", "--no-git", "--json", "DEL-002")
		var out struct {
			Report   string            `json:"report"`
			OK       bool              `json:"ok"`
			Findings []json.RawMessage `json:"findings"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("saída não é JSON: %v\n%s", err, stdout)
		}
		if code != exitFail || out.OK || out.Report != "docs-ai/deliveries/DEL-002/report.md" || len(out.Findings) == 0 {
			t.Errorf("código %d, saída %+v", code, out)
		}
	})
}

func TestE2EDoctor(t *testing.T) {
	root := e2eProject(t)
	code, stdout, stderr := runBin(t, root, "", "doctor", "--json")
	var out struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("saída não é JSON: %v\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	// O projeto de teste não tem .claude/settings.json nem bin/: doctor reprova.
	if code != exitFail || out.OK || len(out.Checks) == 0 {
		t.Errorf("código %d, saída %+v", code, out)
	}
	code, stdout, _ = runBin(t, root, "", "doctor")
	if code != exitFail || !strings.Contains(stdout, "[erro]") {
		t.Errorf("doctor humano: código %d\n%s", code, stdout)
	}
}

func TestE2EVersion(t *testing.T) {
	code, stdout, _ := runBin(t, t.TempDir(), "", "version")
	if code != exitOK || !strings.HasPrefix(stdout, "pipeline-claude ") {
		t.Errorf("version: código %d, %q", code, stdout)
	}
}
