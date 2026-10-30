package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

const fence = "```"

// testConfig é a configuração do projeto fictício (biblioteca de bairro).
const testConfig = `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "base_ref": "base",
 "delivery_id_pattern": "^DEL-[0-9]{3}$",
 "protected_paths": ["internal/"],
 "security_surface_paths": ["internal/auth/"],
 "auto_flags": {"require_release_ops": ["migrations/"]}}`

const testIndex = `# Deliveries

| ID | Título | Status |
|---|---|---|
| DEL-001 | Multa por atraso | done |
| [DEL-002](DEL-002/report.md) | Reserva de livros | done |
| ` + "`DEL-003`" + ` | Login de leitores | done |

A DEL-099 só é citada em texto, fora da tabela.
`

// reportFor monta um report válido no esquema padrão para id. extra entra no
// front matter; sections é acrescentado ao fim.
func reportFor(id, extra, sections string) string {
	return `---
delivery_id: ` + id + `
status: done
` + extra + `---

# Relatório da ` + id + `

## PM/Triage
- Escopo fechado: cálculo de multa por atraso na devolução de livros
- Critérios de aceite: multa zero dentro do prazo
- Riscos: arredondamento de centavos

## Architecture/Guardrails
- Invariantes aplicáveis: INV-01 valores monetários em centavos
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
- [x] testes passam
` + sections
}

const securitySection = `
## Security
- Resultado: sem achados; token validado no middleware
`

const releaseSection = `
## Release/Ops
- Plano de rollback: reverter a migration 0007
`

// requireGit pula sem git no PATH, exceto com PIPELINE_CLAUDE_REQUIRE_GIT=1 (CI).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("PIPELINE_CLAUDE_REQUIRE_GIT") == "1" {
			t.Fatalf("git não encontrado no PATH e PIPELINE_CLAUDE_REQUIRE_GIT=1: %v", err)
		}
		t.Skipf("git não encontrado no PATH: %v", err)
	}
}

// isolateGit desliga a configuração do usuário e fixa autor e datas. Usa
// t.Setenv: os testes deste pacote não podem usar t.Parallel.
func isolateGit(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Exemplo",
		"GIT_AUTHOR_EMAIL":    "exemplo@exemplo.invalid",
		"GIT_AUTHOR_DATE":     "2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME":  "Exemplo",
		"GIT_COMMITTER_EMAIL": "exemplo@exemplo.invalid",
		"GIT_COMMITTER_DATE":  "2026-01-01T00:00:00Z",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		t.Setenv(k, v)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project cria um projeto sem git com configuração, índice e os reports dados
// (ID → conteúdo). Devolve a raiz.
func project(t *testing.T, reports map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, config.FileName, testConfig)
	write(t, root, "docs-ai/deliveries/INDEX.md", testIndex)
	for id, body := range reports {
		write(t, root, "docs-ai/deliveries/"+id+"/report.md", body)
	}
	return root
}

// gitProject cria o projeto num repositório git com a branch "base" no
// primeiro commit e HEAD em "main"; changed são arquivos alterados depois da
// base (commitados). Devolve a raiz.
func gitProject(t *testing.T, reports map[string]string, changed ...string) string {
	t.Helper()
	requireGit(t)
	isolateGit(t)
	root := project(t, reports)
	write(t, root, "README.md", "biblioteca\n")
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	git(t, root, "branch", "base")
	for _, f := range changed {
		write(t, root, f, "alterado\n")
	}
	if len(changed) > 0 {
		git(t, root, "add", "-A")
		git(t, root, "commit", "-q", "-m", "delivery")
	}
	return root
}

type result struct {
	code   int
	stdout string
	stderr string
}

func runAt(t *testing.T, dir string, opts Options) result {
	t.Helper()
	if opts.Getwd == nil {
		opts.Getwd = func() (string, error) { return dir, nil }
	}
	var stdout, stderr bytes.Buffer
	code := Run(opts, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

func (r result) String() string {
	return "código " + string(rune('0'+r.code)) + "\nstdout:\n" + r.stdout + "stderr:\n" + r.stderr
}

func TestUsoInvalido(t *testing.T) {
	tests := []struct {
		name   string
		target string
		setup  func(t *testing.T, root string)
		getwd  func() (string, error)
		want   string
	}{
		{name: "alvo vazio", target: "", want: "alvo"},
		{name: "ID ..", target: "..", want: ".."},
		{name: "ID .", target: ".", want: "."},
		{
			name:   "diretório em vez de arquivo",
			target: "docs-ai/deliveries/DEL-001",
			want:   "DEL-001",
		},
		{
			name:   "config inválida",
			target: "DEL-001",
			setup: func(t *testing.T, root string) {
				write(t, root, config.FileName, `{"version": 1}`)
			},
			want: config.FileName,
		},
		{
			name:   "getwd falha",
			target: "DEL-001",
			getwd:  func() (string, error) { return "", errors.New("sem diretório") },
			want:   "diretório",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
			if tt.setup != nil {
				tt.setup(t, root)
			}
			r := runAt(t, root, Options{Target: tt.target, NoGit: true, Getwd: tt.getwd})
			if r.code != ExitBadUsage {
				t.Fatalf("quer %d; %s", ExitBadUsage, r)
			}
			if !strings.Contains(r.stderr, tt.want) {
				t.Errorf("stderr sem %q:\n%s", tt.want, r.stderr)
			}
		})
	}
}

// Report inexistente é resultado, não uso inválido: reprova (1) no mesmo
// formato dos demais erros, em texto e em JSON (TASK-055).
func TestAlvoInexistente(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		display string
	}{
		{name: "ID inexistente", target: "DEL-404", display: "docs-ai/deliveries/DEL-404/report.md"},
		{name: "caminho inexistente", target: "docs-ai/nada/report.md", display: "docs-ai/nada/report.md"},
		{name: "diretório da delivery sem report", target: "DEL-002", display: "docs-ai/deliveries/DEL-002/report.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
			if err := os.MkdirAll(filepath.Join(root, "docs-ai", "deliveries", "DEL-002"), 0o755); err != nil {
				t.Fatal(err)
			}
			r := runAt(t, root, Options{Target: tt.target, NoGit: true})
			if r.code != ExitFail {
				t.Fatalf("quer %d; %s", ExitFail, r)
			}
			for _, want := range []string{"pipeline-claude validate: " + tt.display + "\n", "  ERRO: report", "não encontrado: o arquivo não existe", "1 erro(s), 0 aviso(s)", "REPROVADO"} {
				if !strings.Contains(r.stdout, want) {
					t.Errorf("stdout sem %q:\n%s", want, r.stdout)
				}
			}
			if r.stderr != "" {
				t.Errorf("stderr = %q, quer vazio", r.stderr)
			}
			if strings.Contains(r.stdout, root) {
				t.Errorf("a saída vaza o caminho absoluto do projeto:\n%s", r.stdout)
			}

			j := runAt(t, root, Options{Target: tt.target, NoGit: true, JSON: true})
			if j.code != ExitFail {
				t.Fatalf("json: quer %d; %s", ExitFail, j)
			}
			var out struct {
				Report   string `json:"report"`
				OK       bool   `json:"ok"`
				Findings []struct {
					Level   string `json:"level"`
					Message string `json:"message"`
				} `json:"findings"`
			}
			if err := json.Unmarshal([]byte(j.stdout), &out); err != nil {
				t.Fatalf("json inválido: %v\n%s", err, j.stdout)
			}
			if out.Report != tt.display || out.OK || len(out.Findings) != 1 || out.Findings[0].Level != "ERRO" || !strings.Contains(out.Findings[0].Message, "não encontrado") {
				t.Errorf("json = %+v", out)
			}
		})
	}
}

// stat que falha por outro motivo que não existir (aqui, "DEL-005" é um arquivo
// e não um diretório) também reprova, sem vazar o caminho absoluto.
func TestAlvoInacessivel(t *testing.T) {
	root := project(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
	write(t, root, "docs-ai/deliveries/DEL-005", "arquivo onde devia haver um diretório\n")
	r := runAt(t, root, Options{Target: "DEL-005", NoGit: true})
	if r.code != ExitFail {
		t.Fatalf("quer %d; %s", ExitFail, r)
	}
	if !strings.Contains(r.stdout, "ERRO: report da delivery \"DEL-005\" não encontrado") || !strings.Contains(r.stdout, "REPROVADO") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, root) || r.stderr != "" {
		t.Errorf("a saída vaza o caminho absoluto ou usa stderr:\n%s\nstderr: %q", r.stdout, r.stderr)
	}
}

func TestConfigAusente(t *testing.T) {
	dir := t.TempDir()
	if _, err := config.Find(dir); err == nil {
		t.Skip("existe um " + config.FileName + " acima de " + dir)
	}
	r := runAt(t, dir, Options{Target: "DEL-001", NoGit: true})
	if r.code != ExitBadUsage {
		t.Fatalf("quer 2; %s", r)
	}
}

func TestAlvo(t *testing.T) {
	root := project(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
	abs := filepath.Join(root, "docs-ai", "deliveries", "DEL-001", "report.md")
	tests := []struct {
		name   string
		dir    string
		target string
	}{
		{"ID na raiz", root, "DEL-001"},
		{"ID de subdiretório", filepath.Join(root, "docs-ai"), "DEL-001"},
		{"caminho relativo", root, "docs-ai/deliveries/DEL-001/report.md"},
		{"caminho relativo de subdiretório", filepath.Join(root, "docs-ai", "deliveries"), "DEL-001/report.md"},
		{"caminho absoluto", t.TempDir(), abs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runAt(t, tt.dir, Options{Target: tt.target, NoGit: true})
			if r.code != ExitOK {
				t.Fatalf("quer 0; %s", r)
			}
			if !strings.Contains(r.stdout, "docs-ai/deliveries/DEL-001/report.md") {
				t.Errorf("saída sem o report relativo à raiz:\n%s", r.stdout)
			}
		})
	}
}

func TestChecagensSemGit(t *testing.T) {
	tests := []struct {
		name    string
		id      string // diretório
		report  string
		index   string // vazio: índice padrão; "-": sem índice
		config  string // vazio: testConfig
		code    int
		wantOut []string
	}{
		{
			name: "report válido",
			id:   "DEL-001", report: reportFor("DEL-001", "", ""),
			code: ExitOK, wantOut: []string{"AVISO", "--no-git", "APROVADO"},
		},
		{
			name: "ID em link na tabela",
			id:   "DEL-002", report: reportFor("DEL-002", "", ""),
			code: ExitOK,
		},
		{
			name: "ID em código na tabela",
			id:   "DEL-003", report: reportFor("DEL-003", "", ""),
			code: ExitOK,
		},
		{
			name: "checagem do report reprova",
			id:   "DEL-001", report: strings.Replace(reportFor("DEL-001", "", ""), "## QA", "## Qualidade", 1),
			code: ExitFail, wantOut: []string{"ERRO", "QA", "REPROVADO"},
		},
		{
			name: "report ilegível",
			id:   "DEL-001", report: "sem front matter\n",
			code: ExitFail, wantOut: []string{"ERRO", "front matter"},
		},
		{
			name: "delivery_id fora do padrão",
			id:   "DEL-001", report: reportFor("DEL-1", "", ""),
			code: ExitFail, wantOut: []string{"delivery_id_pattern"},
		},
		{
			name: "delivery_id diferente do diretório",
			id:   "DEL-001", report: reportFor("DEL-002", "", ""),
			code: ExitFail, wantOut: []string{"DEL-002", "DEL-001", "diretório"},
		},
		{
			name: "ID fora do índice",
			id:   "DEL-004", report: reportFor("DEL-004", "", ""),
			code: ExitFail, wantOut: []string{"DEL-004", "INDEX.md"},
		},
		{
			name: "ID só em texto fora da tabela",
			id:   "DEL-099", report: reportFor("DEL-099", "", ""),
			code: ExitFail, wantOut: []string{"DEL-099", "INDEX.md"},
		},
		{
			name: "ID como prefixo de outro na tabela",
			id:   "DEL-001", report: reportFor("DEL-001", "", ""),
			index: "| ID |\n|---|\n| DEL-0012 |\n| xDEL-001 |\n",
			code:  ExitFail, wantOut: []string{"INDEX.md"},
		},
		{
			name: "índice ausente",
			id:   "DEL-001", report: reportFor("DEL-001", "", ""),
			index: "-",
			code:  ExitFail, wantOut: []string{"INDEX.md"},
		},
		{
			name: "sem delivery_id_pattern só avisa",
			id:   "DEL-001", report: reportFor("DEL-001", "", ""),
			config: strings.Replace(testConfig, `"delivery_id_pattern": "^DEL-[0-9]{3}$",`, "", 1),
			code:   ExitOK, wantOut: []string{"AVISO", "delivery_id_pattern"},
		},
		{
			name: "flag inválida",
			id:   "DEL-001", report: reportFor("DEL-001", "require_security: sim\n", ""),
			code: ExitFail, wantOut: []string{"require_security"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, map[string]string{tt.id: tt.report})
			switch tt.index {
			case "":
			case "-":
				if err := os.Remove(filepath.Join(root, "docs-ai", "deliveries", "INDEX.md")); err != nil {
					t.Fatal(err)
				}
			default:
				write(t, root, "docs-ai/deliveries/INDEX.md", tt.index)
			}
			if tt.config != "" {
				write(t, root, config.FileName, tt.config)
			}
			r := runAt(t, root, Options{Target: tt.id, NoGit: true})
			if r.code != tt.code {
				t.Fatalf("quer %d; %s", tt.code, r)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(r.stdout, w) {
					t.Errorf("saída sem %q:\n%s", w, r.stdout)
				}
			}
		})
	}
}

func TestChecagensComGit(t *testing.T) {
	tests := []struct {
		name    string
		report  string
		changed []string
		code    int
		wantOut []string
	}{
		{
			name:    "diff sem superfície nem auto_flags",
			report:  reportFor("DEL-001", "", ""),
			changed: []string{"internal/emprestimo/multa.go"},
			code:    ExitOK,
		},
		{
			name:    "superfície de segurança com flag false",
			report:  reportFor("DEL-001", "require_security: false\n", ""),
			changed: []string{"internal/auth/token.go", "internal/emprestimo/multa.go"},
			code:    ExitFail,
			wantOut: []string{"require_security", "internal/auth/token.go"},
		},
		{
			name:    "superfície de segurança com flag ausente",
			report:  reportFor("DEL-001", "", ""),
			changed: []string{"internal/auth/token.go"},
			code:    ExitFail,
			wantOut: []string{"require_security", "internal/auth/token.go"},
		},
		{
			name:    "superfície de segurança com caixa diferente",
			report:  reportFor("DEL-001", "", ""),
			changed: []string{"Internal/Auth/Token.go"},
			code:    ExitFail,
			wantOut: []string{"require_security"},
		},
		{
			name:    "superfície de segurança com flag true e seção",
			report:  reportFor("DEL-001", "require_security: true\n", securitySection),
			changed: []string{"internal/auth/token.go"},
			code:    ExitOK,
		},
		{
			name:    "auto_flags com flag false",
			report:  reportFor("DEL-001", "require_release_ops: false\n", ""),
			changed: []string{"migrations/0007_multa.sql"},
			code:    ExitFail,
			wantOut: []string{"require_release_ops", "migrations/0007_multa.sql"},
		},
		{
			name:    "auto_flags com flag true e seção",
			report:  reportFor("DEL-001", "require_release_ops: true\n", releaseSection),
			changed: []string{"migrations/0007_multa.sql"},
			code:    ExitOK,
		},
		{
			name:    "flag inválida não duplica erro do diff",
			report:  reportFor("DEL-001", "require_security: sim\n", ""),
			changed: []string{"internal/auth/token.go"},
			code:    ExitFail,
			wantOut: []string{`"sim"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gitProject(t, map[string]string{"DEL-001": tt.report}, tt.changed...)
			r := runAt(t, root, Options{Target: "DEL-001"})
			if r.code != tt.code {
				t.Fatalf("quer %d; %s", tt.code, r)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(r.stdout, w) {
					t.Errorf("saída sem %q:\n%s", w, r.stdout)
				}
			}
			if strings.Contains(r.stdout, "--no-git") {
				t.Errorf("aviso de --no-git com git ligado:\n%s", r.stdout)
			}
		})
	}
}

func TestGitNaoCommitado(t *testing.T) {
	// TD-11: working tree e não rastreados também disparam a flag.
	root := gitProject(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
	write(t, root, "internal/auth/novo.go", "package auth\n")
	r := runAt(t, root, Options{Target: "DEL-001"})
	if r.code != ExitFail || !strings.Contains(r.stdout, "internal/auth/novo.go") {
		t.Fatalf("quer 1 citando o arquivo não rastreado; %s", r)
	}
}

func TestGitFalhaReprova(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		git  bool
	}{
		{"base inexistente", Options{Target: "DEL-001", BaseRef: "nao-existe"}, true},
		{"base começando com hífen", Options{Target: "DEL-001", BaseRef: "--output=x"}, true},
		{"sem repositório", Options{Target: "DEL-001"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var root string
			if tt.git {
				root = gitProject(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
			} else {
				requireGit(t)
				isolateGit(t)
				root = project(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
				t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
			}
			r := runAt(t, root, tt.opts)
			if r.code != ExitFail {
				t.Fatalf("quer 1; %s", r)
			}
			if !strings.Contains(r.stdout, "--no-git") {
				t.Errorf("erro do git sem dica de --no-git:\n%s", r.stdout)
			}
		})
	}
}

func TestBaseRefDaOpcao(t *testing.T) {
	// base_ref da config é "base"; --base aponta para HEAD, então o diff some.
	root := gitProject(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")}, "internal/auth/token.go")
	if r := runAt(t, root, Options{Target: "DEL-001"}); r.code != ExitFail {
		t.Fatalf("com base_ref da config: quer 1; %s", r)
	}
	if r := runAt(t, root, Options{Target: "DEL-001", BaseRef: "HEAD"}); r.code != ExitOK {
		t.Fatalf("com --base HEAD: quer 0; %s", r)
	}
}

func TestConfigEmSubdiretorioDoRepositorio(t *testing.T) {
	// O gitdiff devolve caminhos relativos à raiz do repositório; os padrões da
	// config são relativos à raiz do projeto (onde está a config).
	requireGit(t)
	isolateGit(t)
	repo := t.TempDir()
	write(t, repo, "README.md", "monorepo\n")
	git(t, repo, "init", "-q")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "base")
	git(t, repo, "branch", "base")
	sub := filepath.Join(repo, "servicos", "acervo")
	write(t, sub, config.FileName, testConfig)
	write(t, sub, "docs-ai/deliveries/INDEX.md", testIndex)
	write(t, sub, "docs-ai/deliveries/DEL-001/report.md", reportFor("DEL-001", "", ""))
	write(t, sub, "internal/auth/token.go", "package auth\n")
	write(t, repo, "internal/auth/fora.go", "package auth\n") // fora do projeto
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "delivery")

	r := runAt(t, sub, Options{Target: "DEL-001"})
	if r.code != ExitFail {
		t.Fatalf("quer 1; %s", r)
	}
	if !strings.Contains(r.stdout, "internal/auth/token.go") {
		t.Errorf("saída sem o arquivo do projeto:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "fora.go") {
		t.Errorf("arquivo fora do projeto disparou flag:\n%s", r.stdout)
	}
}

func TestJSON(t *testing.T) {
	type out struct {
		Report   string `json:"report"`
		OK       bool   `json:"ok"`
		Findings []struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		} `json:"findings"`
	}
	t.Run("aprovado", func(t *testing.T) {
		root := project(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
		r := runAt(t, root, Options{Target: "DEL-001", NoGit: true, JSON: true})
		if r.code != ExitOK {
			t.Fatalf("quer 0; %s", r)
		}
		var o out
		if err := json.Unmarshal([]byte(r.stdout), &o); err != nil {
			t.Fatalf("saída não é JSON: %v\n%s", err, r.stdout)
		}
		if !o.OK || o.Report != "docs-ai/deliveries/DEL-001/report.md" {
			t.Errorf("out = %+v", o)
		}
		if len(o.Findings) != 1 || o.Findings[0].Level != "AVISO" {
			t.Errorf("findings = %+v, quer só o aviso de --no-git", o.Findings)
		}
	})
	t.Run("reprovado", func(t *testing.T) {
		root := project(t, map[string]string{"DEL-004": reportFor("DEL-004", "", "")})
		r := runAt(t, root, Options{Target: "DEL-004", NoGit: true, JSON: true})
		if r.code != ExitFail {
			t.Fatalf("quer 1; %s", r)
		}
		var o out
		if err := json.Unmarshal([]byte(r.stdout), &o); err != nil {
			t.Fatalf("saída não é JSON: %v\n%s", err, r.stdout)
		}
		if o.OK || len(o.Findings) == 0 {
			t.Errorf("out = %+v", o)
		}
	})
	t.Run("findings vazio é lista", func(t *testing.T) {
		root := gitProject(t, map[string]string{"DEL-001": reportFor("DEL-001", "", "")})
		r := runAt(t, root, Options{Target: "DEL-001", JSON: true})
		if r.code != ExitOK {
			t.Fatalf("quer 0; %s", r)
		}
		if !strings.Contains(r.stdout, `"findings": []`) {
			t.Errorf("findings não é lista vazia:\n%s", r.stdout)
		}
	})
}

// TestParityCorpus compara o validador com o resultado esperado num corpus
// privado de reports reais, fora do repositório (TD-05). Sem a variável, pula.
func TestParityCorpus(t *testing.T) {
	dir := os.Getenv("PIPELINE_CLAUDE_PARITY_CORPUS")
	if dir == "" {
		t.Skip("PIPELINE_CLAUDE_PARITY_CORPUS não definido")
	}
	data, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatalf("expected.json: %v", err)
	}
	var expected map[string]string
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatalf("expected.json: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("expected.json vazio")
	}
	ids := make([]string, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var nOK, nFail, mismatch int
	for _, id := range ids {
		want := expected[id]
		if want != "ok" && want != "fail" {
			t.Fatalf("expected.json: valor %q inválido (use ok ou fail)", want)
		}
		var stdout, stderr bytes.Buffer
		code := Run(Options{
			Target: id, NoGit: true,
			Getwd: func() (string, error) { return dir, nil },
		}, &stdout, &stderr)
		got := map[int]string{ExitOK: "ok", ExitFail: "fail"}[code]
		if got == "" {
			t.Errorf("%s: uso inválido (código %d): %s", id, code, strings.TrimSpace(stderr.String()))
			mismatch++
			continue
		}
		if got == "ok" {
			nOK++
		} else {
			nFail++
		}
		if got != want {
			mismatch++
			t.Errorf("%s: esperado %s, obtido %s\n%s", id, want, got, stdout.String())
		}
	}
	// Só contagens no log: o corpus é privado.
	t.Logf("corpus: %d reports, %d ok, %d fail, %d divergências", len(ids), nOK, nFail, mismatch)
}
