package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
		// Antes: "init não implementado". Agora o init existe (TD-24).
		{name: "init sem nome", args: []string{"init"}, wantCode: exitBlocked, wantStderr: "nome"},
		{name: "init com dois nomes", args: []string{"init", "a", "b"}, wantCode: exitBlocked, wantStderr: "um nome"},
		{name: "init com opção desconhecida", args: []string{"init", "a", "--xpto"}, wantCode: exitBlocked, wantStderr: "xpto"},
		{name: "init -h", args: []string{"init", "-h"}, wantCode: exitOK, wantStderr: "--toolkit-version"},
		{name: "init com módulo vazio na lista", args: []string{"init", "a", "--toolkit-version", "v0.3.0", "--modules", "data-model,,release-ops"}, wantCode: exitBlocked, wantStderr: "--modules"},
		// O binário de teste roda sem versão de módulo (dev): sem
		// --toolkit-version o init não tem o que fixar no Makefile (TD-24).
		{name: "init sem versão em build dev", args: []string{"init", "a"}, wantCode: exitBlocked, wantStderr: "--toolkit-version"},
		// Antes: "adopt previsto". Agora o adopt existe (TD-31 a TD-33).
		{name: "adopt com argumento", args: []string{"adopt", "x"}, wantCode: exitBlocked, wantStderr: "argumento"},
		{name: "adopt com opção desconhecida", args: []string{"adopt", "--xpto"}, wantCode: exitBlocked, wantStderr: "xpto"},
		{name: "adopt -h", args: []string{"adopt", "-h"}, wantCode: exitOK, wantStderr: "--dry-run"},
		{name: "adopt com módulo vazio na lista", args: []string{"adopt", "--toolkit-version", "v0.3.0", "--modules", "data-model,"}, wantCode: exitBlocked, wantStderr: "--modules"},
		{name: "adopt sem versão em build dev", args: []string{"adopt"}, wantCode: exitBlocked, wantStderr: "--toolkit-version"},
		{name: "sync com argumento", args: []string{"sync", "x"}, wantCode: exitBlocked, wantStderr: "argumento"},
		{name: "sync com opção desconhecida", args: []string{"sync", "--xpto"}, wantCode: exitBlocked, wantStderr: "xpto"},
		{name: "sync --release com --dry-run", args: []string{"sync", "--dry-run", "--release", ".claude/agents/x.md"}, wantCode: exitBlocked, wantStderr: "--dry-run"},
		{name: "sync -h", args: []string{"sync", "-h"}, wantCode: exitOK, wantStderr: "--release"},
		// Antes: "fmt-hook não implementado". Agora bloqueia por falha fechada.
		{name: "fmt-hook com stdin vazio bloqueia", args: []string{"fmt-hook"}, wantCode: exitBlocked, wantStderr: "payload"},
		{name: "fmt-hook com argumento extra", args: []string{"fmt-hook", "x"}, stdin: "{}", wantCode: exitBlocked, wantStderr: "fmt-hook"},
		{name: "sensors com opção desconhecida", args: []string{"sensors", "--xpto"}, wantCode: exitBlocked, wantStderr: "xpto"},
		{name: "sensors --timeout inválido", args: []string{"sensors", "--timeout", "dez"}, wantCode: exitBlocked, wantStderr: "timeout"},
		{name: "sensors --timeout zero", args: []string{"sensors", "--timeout", "0s"}, wantCode: exitBlocked, wantStderr: "timeout"},
		{name: "sensors --timeout negativo", args: []string{"sensors", "--timeout=-1m"}, wantCode: exitBlocked, wantStderr: "timeout"},
		{name: "sensors -h", args: []string{"sensors", "-h"}, wantCode: exitOK, wantStderr: "timeout"},
		{name: "delivery sem subcomando", args: []string{"delivery"}, wantCode: exitBlocked, wantStderr: "delivery new"},
		{name: "delivery com subcomando desconhecido", args: []string{"delivery", "rm"}, wantCode: exitBlocked, wantStderr: `"rm"`},
		{name: "delivery new sem título", args: []string{"delivery", "new"}, wantCode: exitBlocked, wantStderr: "--title"},
		{name: "delivery new com título vazio", args: []string{"delivery", "new", "--title", "  "}, wantCode: exitBlocked, wantStderr: "--title"},
		{name: "delivery new com argumento extra", args: []string{"delivery", "new", "--title", "x", "y"}, wantCode: exitBlocked, wantStderr: "argumento"},
		{name: "delivery new -h", args: []string{"delivery", "new", "-h"}, wantCode: exitOK, wantStderr: "--prefix"},
		{name: "mcp com argumento", args: []string{"mcp", "x"}, wantCode: exitBlocked, wantStderr: "mcp"},
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
	for _, cmd := range []string{"hook", "validate", "doctor", "fmt-hook", "sensors", "delivery", "mcp", "init", "sync", "adopt"} {
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

func TestParseInterspersed(t *testing.T) {
	tests := []struct {
		args   []string
		json   bool
		names  []string
		wantOK bool
	}{
		{[]string{"a", "--json", "b"}, true, []string{"a", "b"}, true},
		{[]string{"--json", "a"}, true, []string{"a"}, true},
		{[]string{"a", "--", "--json"}, false, []string{"a", "--json"}, true},
		{nil, false, nil, true},
		{[]string{"a", "--xpto"}, false, nil, false},
	}
	for _, tt := range tests {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		asJSON := fs.Bool("json", false, "")
		names, _, ok := parseInterspersed(fs, tt.args)
		if ok != tt.wantOK || (ok && (*asJSON != tt.json || strings.Join(names, ",") != strings.Join(tt.names, ","))) {
			t.Errorf("parseInterspersed(%q) = %q, json=%v, ok=%v", tt.args, names, *asJSON, ok)
		}
	}
}

// Ponta a ponta com o binário compilado.

var binPath string

func TestMain(m *testing.M) {
	// Como sensor (TestHelperProcess), o binário de teste não compila nada.
	if os.Getenv(helperEnv) == "1" {
		os.Exit(m.Run())
	}
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
	// Versão fixada por ldflags, como no make build: init e sync precisam de
	// uma versão de módulo (TD-24, TD-25).
	build := exec.Command(goTool(), "build", "-ldflags", "-X "+buildinfoVar+"="+e2eVersion, "-o", binPath, ".")
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
 "delivery_prefixes": ["DEL"],
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
		{"alvo inexistente reprova", []string{"validate", "--no-git", "DEL-404"}, exitFail, "REPROVADO", ""},
		{"caminho inexistente reprova", []string{"validate", "--no-git", "docs-ai/nada/report.md"}, exitFail, "não encontrado", ""},
		{"ID inválido segue sendo uso inválido", []string{"validate", "--no-git", ".."}, exitBlocked, "", "inválido"},
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

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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

func TestE2EFmtHook(t *testing.T) {
	const (
		unformatted = "package exemplo\nfunc  Soma(a,b int)int{return a+b}\n"
		formatted   = "package exemplo\n\nfunc Soma(a, b int) int { return a + b }\n"
	)
	t.Run("formata o arquivo da fixture", func(t *testing.T) {
		root := e2eProject(t)
		writeFile(t, root, "internal/exemplo/alvo.go", unformatted)
		code, stdout, stderr := runBin(t, root, fixture(t, "posttooluse-edit-main.json", root), "fmt-hook")
		if code != exitOK || stdout != "" {
			t.Fatalf("código = %d, stdout = %q\nstderr:\n%s", code, stdout, stderr)
		}
		if got := readFile(t, root, "internal/exemplo/alvo.go"); got != formatted {
			t.Errorf("arquivo = %q", got)
		}
		if !strings.Contains(stderr, "formatado internal/exemplo/alvo.go") {
			t.Errorf("stderr = %q", stderr)
		}
	})
	t.Run("evento errado bloqueia", func(t *testing.T) {
		root := e2eProject(t)
		writeFile(t, root, "internal/exemplo/alvo.go", unformatted)
		code, _, _ := runBin(t, root, fixture(t, "pretooluse-edit-main.json", root), "fmt-hook")
		if code != exitBlocked || readFile(t, root, "internal/exemplo/alvo.go") != unformatted {
			t.Errorf("código = %d", code)
		}
	})
}

// helperEnv liga TestHelperProcess: os sensores do teste rodam o próprio
// binário de teste, sem depender de sh.
const helperEnv = "PIPELINE_CLAUDE_CLI_HELPER"

func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	if os.Args[len(os.Args)-1] == "ok" {
		fmt.Println("sensor ok")
		os.Exit(0)
	}
	fmt.Println("sensor falhou")
	os.Exit(3)
}

func TestE2ESensors(t *testing.T) {
	t.Setenv(helperEnv, "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sensor := func(name, mode string) string {
		argv, _ := json.Marshal([]string{exe, "-test.run=^TestHelperProcess$", "--", mode})
		return fmt.Sprintf(`{"name": %q, "cmd": %s}`, name, argv)
	}
	root := e2eProject(t)
	cfg := strings.Replace(e2eConfig, `"version": 1,`, `"version": 1, "sensors": [`+sensor("passa", "ok")+`, `+sensor("falha", "fail")+`],`, 1)
	writeFile(t, root, ".pipeline-claude.json", cfg)

	tests := []struct {
		name   string
		args   []string
		want   int
		stdout string
	}{
		{"todos, um falha", []string{"sensors"}, exitFail, "falha"},
		{"só o que passa", []string{"sensors", "--timeout", "30s", "passa"}, exitOK, "passa"},
		{"só o que falha", []string{"sensors", "falha"}, exitFail, "falha"},
		{"nome desconhecido", []string{"sensors", "nenhum"}, exitBlocked, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runBin(t, root, "", tt.args...)
			if code != tt.want || !strings.Contains(stdout, tt.stdout) {
				t.Errorf("código = %d, quer %d\nstdout:\n%s\nstderr:\n%s", code, tt.want, stdout, stderr)
			}
		})
	}
	t.Run("json", func(t *testing.T) {
		code, stdout, _ := runBin(t, root, "", "sensors", "--json")
		var out struct {
			OK      bool `json:"ok"`
			Sensors []struct {
				Name string `json:"name"`
				OK   bool   `json:"ok"`
			} `json:"sensors"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("saída não é JSON: %v\n%s", err, stdout)
		}
		if code != exitFail || out.OK || len(out.Sensors) != 2 || !out.Sensors[0].OK || out.Sensors[1].OK {
			t.Errorf("código %d, saída %+v\n%s", code, out, stdout)
		}
	})
	t.Run("config ausente", func(t *testing.T) {
		code, _, stderr := runBin(t, t.TempDir(), "", "sensors")
		if code != exitBlocked || !strings.Contains(stderr, "doctor") {
			t.Errorf("código = %d, stderr = %q", code, stderr)
		}
	})
}

func TestE2EDeliveryNew(t *testing.T) {
	root := e2eProject(t)
	// DEL-001 e DEL-002 no índice, DEL-003 só em diretório: a próxima é DEL-004.
	code, stdout, stderr := runBin(t, root, "", "delivery", "new", "--title", "Reserva de livros")
	if code != exitOK || !strings.Contains(stdout, "DEL-004") || !strings.Contains(stdout, "docs-ai/deliveries/DEL-004/report.md") {
		t.Fatalf("primeira: código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	code, stdout, stderr = runBin(t, root, "", "delivery", "new", "--json", "--prefix", "DEL", "--title", "Renovação online")
	if code != exitOK {
		t.Fatalf("segunda: código %d\nstderr:\n%s", code, stderr)
	}
	var out struct {
		ID     string `json:"id"`
		Report string `json:"report"`
		Index  string `json:"index"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("saída não é JSON: %v\n%s", err, stdout)
	}
	if out.ID != "DEL-005" || out.Report != "docs-ai/deliveries/DEL-005/report.md" || out.Index != "docs-ai/deliveries/INDEX.md" {
		t.Errorf("json = %+v", out)
	}

	// O validate reconhece o índice: recém-criada reprova só por conteúdo.
	code, stdout, _ = runBin(t, root, "", "validate", "--no-git", "DEL-004")
	if code != exitFail || strings.Contains(stdout, "índice") {
		t.Errorf("validate recém-criada: código %d\n%s", code, stdout)
	}
	report := readFile(t, root, "docs-ai/deliveries/DEL-004/report.md")
	report = strings.ReplaceAll(report, "<preencher>", "conferido na revisão")
	report = strings.ReplaceAll(report, "- [ ]", "- [x]")
	writeFile(t, root, "docs-ai/deliveries/DEL-004/report.md", report)
	if code, stdout, _ := runBin(t, root, "", "validate", "--no-git", "DEL-004"); code != exitOK {
		t.Errorf("validate preenchida: código %d\n%s", code, stdout)
	}

	t.Run("título inválido não cria nada", func(t *testing.T) {
		before := readFile(t, root, "docs-ai/deliveries/INDEX.md")
		code, _, stderr := runBin(t, root, "", "delivery", "new", "--title", "a|b")
		if code != exitFail || !strings.Contains(stderr, "|") {
			t.Errorf("código = %d, stderr = %q", code, stderr)
		}
		if readFile(t, root, "docs-ai/deliveries/INDEX.md") != before {
			t.Error("índice alterado")
		}
	})
	t.Run("config ausente", func(t *testing.T) {
		code, _, stderr := runBin(t, t.TempDir(), "", "delivery", "new", "--title", "x")
		if code != exitBlocked || !strings.Contains(stderr, "doctor") {
			t.Errorf("código = %d, stderr = %q", code, stderr)
		}
	})
}

func TestE2EMCP(t *testing.T) {
	root := e2eProject(t)
	session := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"teste","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"pipeline_validate","arguments":{"target":"DEL-001","no_git":true}}}`,
	}, "\n") + "\n"
	code, stdout, stderr := runBin(t, root, session, "mcp")
	if code != exitOK {
		t.Fatalf("código = %d\nstderr:\n%s", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("respostas = %d:\n%s", len(lines), stdout)
	}
	var resp [3]struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Tools           []struct {
				Name string `json:"name"`
			} `json:"tools"`
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	for i, l := range lines {
		if err := json.Unmarshal([]byte(l), &resp[i]); err != nil {
			t.Fatalf("linha %d não é JSON: %v\n%s", i, err, l)
		}
		if resp[i].ID != i+1 {
			t.Errorf("linha %d com id %d", i, resp[i].ID)
		}
	}
	if resp[0].Result.ProtocolVersion != "2025-06-18" {
		t.Errorf("initialize: %s", lines[0])
	}
	if len(resp[1].Result.Tools) != 5 { // 5 com pipeline_diff (TD-29)
		t.Errorf("tools/list: %s", lines[1])
	}
	if resp[2].Result.IsError || len(resp[2].Result.Content) != 1 || !strings.Contains(resp[2].Result.Content[0].Text, `"ok": true`) {
		t.Errorf("pipeline_validate: %s", lines[2])
	}
}

// TASK-053: --scope e --flags gravam a decisão da triagem no report criado.
func TestE2EDeliveryNewScopeFlags(t *testing.T) {
	t.Run("flags ligam a flag e acrescentam a seção", func(t *testing.T) {
		root := e2eProject(t)
		code, stdout, stderr := runBin(t, root, "", "delivery", "new", "--title", "Login", "--flags", "require_security, require_release_ops")
		if code != exitOK {
			t.Fatalf("código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		report := readFile(t, root, "docs-ai/deliveries/DEL-004/report.md")
		for _, want := range []string{"require_security: true\n", "require_release_ops: true\n", "\n## Security\n- Resultado: <preencher>\n", "\n## Release/Ops\n- Plano de rollback: <preencher>\n"} {
			if !strings.Contains(report, want) {
				t.Errorf("report sem %q:\n%s", want, report)
			}
		}
	})
	t.Run("flag desconhecida não cria nada", func(t *testing.T) {
		root := e2eProject(t)
		before := readFile(t, root, "docs-ai/deliveries/INDEX.md")
		code, _, stderr := runBin(t, root, "", "delivery", "new", "--title", "Login", "--flags", "require_magia")
		if code != exitFail || !strings.Contains(stderr, "require_magia") || !strings.Contains(stderr, "require_security") {
			t.Errorf("código = %d, stderr = %q", code, stderr)
		}
		if readFile(t, root, "docs-ai/deliveries/INDEX.md") != before {
			t.Error("índice alterado")
		}
	})
	t.Run("--flags vazio é uso inválido", func(t *testing.T) {
		root := e2eProject(t)
		code, _, stderr := runBin(t, root, "", "delivery", "new", "--title", "Login", "--flags", "require_security,,require_release_ops")
		if code != exitBlocked || !strings.Contains(stderr, "--flags") {
			t.Errorf("código = %d, stderr = %q", code, stderr)
		}
	})
	t.Run("escopo sem report_template reprova", func(t *testing.T) {
		root := e2eProject(t)
		code, _, stderr := runBin(t, root, "", "delivery", "new", "--title", "Login", "--scope", "Login por senha")
		if code != exitFail || !strings.Contains(stderr, "report_template") {
			t.Errorf("código = %d, stderr = %q", code, stderr)
		}
	})
	t.Run("escopo com <escopo> no template", func(t *testing.T) {
		root := e2eProject(t)
		writeFile(t, root, ".pipeline-claude.json", strings.Replace(e2eConfig, `"editor_agents"`, `"report_template": "docs-ai/modelo.md", "editor_agents"`, 1))
		writeFile(t, root, "docs-ai/modelo.md", "---\ndelivery_id: <delivery_id>\nstatus: draft\nrequire_security: false\n---\n\n## PM/Triage\n- Escopo fechado: <escopo>\n")
		code, stdout, stderr := runBin(t, root, "", "delivery", "new", "--json", "--title", "Login", "--scope", "Login por senha", "--flags", "require_security")
		if code != exitOK {
			t.Fatalf("código %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		report := readFile(t, root, "docs-ai/deliveries/DEL-004/report.md")
		for _, want := range []string{"- Escopo fechado: Login por senha\n", "require_security: true\n", "## Security\n"} {
			if !strings.Contains(report, want) {
				t.Errorf("report sem %q:\n%s", want, report)
			}
		}
	})
}
