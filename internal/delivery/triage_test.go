package delivery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/validate"
)

// Escopo e flags na criação (TASK-053): o pm-triage não escreve no report, então
// New grava o que a triagem decidiu.

const triageTemplate = `---
delivery_id: <delivery_id>
status: draft
require_security: false
require_release_ops: false
---

# <delivery_id> — <titulo>

## PM/Triage
- Escopo fechado: <escopo>
- Criterios de aceite: <preencher>

## Final Checklist
- [ ] <preencher>
`

func triageProject(t *testing.T, tmpl string) *config.Config {
	t.Helper()
	return project(t, defaultIDs+`, "report_template": "docs-ai/templates/report.md"`,
		map[string]string{indexRel: indexHead, "docs-ai/templates/report.md": tmpl})
}

func TestNewScope(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		scope   string
		want    string // trecho esperado no report
		notWant string
		errPart string
	}{
		{name: "escopo entra no lugar de <escopo>", tmpl: triageTemplate, scope: "Multa por atraso; fora: cobrança",
			want: "- Escopo fechado: Multa por atraso; fora: cobrança\n", notWant: "<escopo>"},
		{name: "espaços nas pontas saem", tmpl: triageTemplate, scope: "  Multa  ",
			want: "- Escopo fechado: Multa\n"},
		{name: "sem escopo, <escopo> vira placeholder", tmpl: triageTemplate, scope: "",
			want: "- Escopo fechado: <preencher>\n", notWant: "<escopo>"},
		{name: "escopo com <titulo> não é expandido de novo", tmpl: triageTemplate, scope: "usa <titulo> literal",
			want: "- Escopo fechado: usa <titulo> literal\n"},
		{name: "template sem <escopo> recusa o escopo", tmpl: strings.ReplaceAll(triageTemplate, "<escopo>", "<preencher>"), scope: "Multa",
			errPart: "<escopo>"},
		{name: "duas linhas", tmpl: triageTemplate, scope: "a\nb", errPart: "uma linha"},
		{name: "retorno de carro", tmpl: triageTemplate, scope: "a\rb", errPart: "uma linha"},
		{name: "controle", tmpl: triageTemplate, scope: "a\x00b", errPart: "controle"},
		{name: "UTF-8 inválido", tmpl: triageTemplate, scope: "a\xffb", errPart: "UTF-8"},
		{name: "abre comentário", tmpl: triageTemplate, scope: "a <!-- b", errPart: "comentário"},
		{name: "fecha comentário", tmpl: triageTemplate, scope: "a --> b", errPart: "comentário"},
		{name: "longo demais", tmpl: triageTemplate, scope: strings.Repeat("é", MaxScopeLen+1), errPart: "máximo"},
		{name: "no limite", tmpl: triageTemplate, scope: strings.Repeat("é", MaxScopeLen),
			want: "- Escopo fechado: " + strings.Repeat("é", MaxScopeLen) + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := triageProject(t, tt.tmpl)
			before := snapshot(t, cfg.Root())
			got, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Scope: tt.scope}, now)
			if tt.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errPart) {
					t.Fatalf("erro = %v, esperava conter %q", err, tt.errPart)
				}
				sameTree(t, before, snapshot(t, cfg.Root()))
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rep := readFile(t, cfg, got.ReportPath)
			if !strings.Contains(rep, tt.want) {
				t.Errorf("report sem %q:\n%s", tt.want, rep)
			}
			if tt.notWant != "" && strings.Contains(rep, tt.notWant) {
				t.Errorf("report com %q:\n%s", tt.notWant, rep)
			}
		})
	}
}

func TestNewScopeComEsquema(t *testing.T) {
	cfg := project(t, defaultIDs, map[string]string{indexRel: indexHead})
	before := snapshot(t, cfg.Root())
	_, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Scope: "Multa"}, now)
	if err == nil || !strings.Contains(err.Error(), "report_template") {
		t.Fatalf("erro = %v, esperava citar report_template", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}

func TestNewFlags(t *testing.T) {
	const securityBlock = "\n## Security\n- Resultado: <preencher>\n"
	tests := []struct {
		name    string
		tmpl    string // vazio: report do esquema
		flags   []string
		want    []string
		notWant []string
		errPart string
	}{
		{name: "template: linha false vira true", tmpl: triageTemplate, flags: []string{"require_security"},
			want:    []string{"require_security: true\n", "require_release_ops: false\n", securityBlock},
			notWant: []string{"require_security: false"}},
		{name: "template: duas flags", tmpl: triageTemplate, flags: []string{"require_security", "require_release_ops"},
			want: []string{"require_security: true\n", "require_release_ops: true\n", securityBlock, "\n## Release/Ops\n- Plano de rollback: <preencher>\n"}},
		{name: "flag repetida vale uma vez", tmpl: triageTemplate, flags: []string{"require_security", "require_security"},
			want: []string{"require_security: true\n"}, notWant: []string{"## Security\n- Resultado: <preencher>\n\n## Security"}},
		{name: "template sem a linha da flag: entra no front matter", tmpl: strings.ReplaceAll(triageTemplate, "require_security: false\n", ""),
			flags: []string{"require_security"}, want: []string{"require_release_ops: false\nrequire_security: true\n---\n", securityBlock}},
		{name: "esquema: flag liga o front e acrescenta a seção", flags: []string{"require_security"},
			want:    []string{"require_security: true\n", "require_release_ops: false\n", securityBlock},
			notWant: []string{"require_security: false"}},
		{name: "flag desconhecida", tmpl: triageTemplate, flags: []string{"require_magia"}, errPart: "require_magia"},
		{name: "flag vazia", tmpl: triageTemplate, flags: []string{""}, errPart: "vazia"},
		{name: "flag que não é de seção (status)", tmpl: triageTemplate, flags: []string{"status"}, errPart: "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *config.Config
			if tt.tmpl == "" {
				cfg = project(t, defaultIDs, map[string]string{indexRel: indexHead})
			} else {
				cfg = triageProject(t, tt.tmpl)
			}
			before := snapshot(t, cfg.Root())
			got, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Flags: tt.flags}, now)
			if tt.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errPart) {
					t.Fatalf("erro = %v, esperava conter %q", err, tt.errPart)
				}
				if strings.Contains(tt.errPart, "require_magia") && !strings.Contains(err.Error(), "require_security") {
					t.Errorf("erro não lista as flags válidas: %v", err)
				}
				sameTree(t, before, snapshot(t, cfg.Root()))
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			rep := readFile(t, cfg, got.ReportPath)
			for _, w := range tt.want {
				if !strings.Contains(rep, w) {
					t.Errorf("report sem %q:\n%s", w, rep)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(rep, w) {
					t.Errorf("report com %q:\n%s", w, rep)
				}
			}
			if n := strings.Count(rep, "\n## Security\n"); n > 1 {
				t.Errorf("seção Security %d vezes:\n%s", n, rep)
			}
		})
	}
}

// Flag que o template já traz como true não duplica a seção que ele já tem.
func TestNewFlagJaLigadaNoTemplate(t *testing.T) {
	tmpl := strings.Replace(triageTemplate, "require_security: false", "require_security: true", 1) +
		"\n## Security\n- Resultado: <preencher>\n"
	cfg := triageProject(t, tmpl)
	got, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Flags: []string{"require_security"}}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep := readFile(t, cfg, got.ReportPath)
	if n := strings.Count(rep, "## Security"); n != 1 {
		t.Errorf("seção Security %d vezes:\n%s", n, rep)
	}
	if n := strings.Count(rep, "require_security: true"); n != 1 {
		t.Errorf("flag %d vezes:\n%s", n, rep)
	}
}

// Seção citada só em comentário HTML não conta como seção presente.
func TestNewFlagSecaoEmComentario(t *testing.T) {
	tmpl := triageTemplate + "\n<!--\n## Security\n- Resultado: x\n-->\n"
	cfg := triageProject(t, tmpl)
	got, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Flags: []string{"require_security"}}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep := readFile(t, cfg, got.ReportPath)
	if !strings.HasSuffix(rep, "\n## Security\n- Resultado: <preencher>\n") {
		t.Errorf("seção não acrescentada depois do comentário:\n%s", rep)
	}
}

func TestNewFlagsSemFrontMatter(t *testing.T) {
	cfg := triageProject(t, "# <delivery_id>\n\n## PM/Triage\n")
	before := snapshot(t, cfg.Root())
	_, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Flags: []string{"require_security"}}, now)
	if err == nil || !strings.Contains(err.Error(), "front matter") {
		t.Fatalf("erro = %v, esperava citar front matter", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}

func TestNewFlagComValorEstranho(t *testing.T) {
	cfg := triageProject(t, strings.Replace(triageTemplate, "require_security: false", "require_security: talvez", 1))
	before := snapshot(t, cfg.Root())
	_, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Flags: []string{"require_security"}}, now)
	if err == nil || !strings.Contains(err.Error(), "require_security") {
		t.Fatalf("erro = %v, esperava citar require_security", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}

// O report criado com escopo e flags só reprova por placeholder e checklist;
// preenchido, o validador aprova, com a seção Security exigida pela flag.
func TestNewScopeFlagsValidador(t *testing.T) {
	full := strings.Replace(triageTemplate, "## Final Checklist", `- Riscos: <preencher>

## Architecture/Guardrails
- Invariantes aplicaveis: <preencher>
- Arquivos provaveis de impacto: <preencher>

## Dev
- Arquivos alterados: <preencher>
- Resumo tecnico: <preencher>
- Testes criados/ajustados: <preencher>
- Evidencias (codigo/teste): <preencher>

## QA
- Resultado: <preencher>
- Cenarios executados: <preencher>

## Docs
- Docs atualizados: <preencher>

## Final Checklist`, 1)
	cfg := triageProject(t, full)
	got, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Scope: "Multa por atraso", Flags: []string{"require_security"}}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run := func() (int, string) {
		var stdout, stderr strings.Builder
		code := validate.Run(validate.Options{
			Target: got.ID,
			NoGit:  true,
			Getwd:  func() (string, error) { return cfg.Root(), nil },
		}, &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}
	if code, out := run(); code != validate.ExitFail {
		t.Fatalf("recém-criado: código %d, esperava %d\n%s", code, validate.ExitFail, out)
	}
	p := filepath.Join(cfg.Root(), filepath.FromSlash(got.ReportPath))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.ReplaceAll(string(data), "<preencher>", "conferido na revisão")
	filled = strings.ReplaceAll(filled, "- [ ]", "- [x]")
	if err := os.WriteFile(p, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != validate.ExitOK {
		t.Fatalf("preenchido: código %d, esperava 0\n%s", code, out)
	}
}

// FuzzApplyFlags: qualquer template sem panic; sem erro, a flag fica true no
// front matter, a seção que ela exige existe e aplicar de novo não muda nada.
func FuzzApplyFlags(f *testing.F) {
	f.Add(triageTemplate)
	f.Add("---\nrequire_security: true\n---\n")
	f.Add("---\r\nrequire_security: false\r\n---\r\n## Security\r\n")
	f.Add("# sem front matter\n")
	f.Add("---\nrequire_security: false\n")
	f.Add("---\n---\n<!--\n## Security\n-->")
	f.Add("")
	schema := config.DefaultReportSchema()
	flags := []string{"require_security"}
	f.Fuzz(func(t *testing.T, content string) {
		out, err := applyFlags(content, &schema, flags)
		if err != nil {
			return
		}
		if !strings.Contains(out, "require_security: true") && !strings.Contains(strings.ToLower(out), "require_security:") {
			t.Fatalf("flag ausente do resultado:\n%q", out)
		}
		again, err := applyFlags(out, &schema, flags)
		if err != nil {
			t.Fatalf("segunda aplicação falhou: %v\n%q", err, out)
		}
		if again != out {
			t.Fatalf("não é idempotente:\n%q\n%q", out, again)
		}
	})
}

// Achado do fuzz: comentário sem fechar engolia a seção acrescentada; agora é
// erro, sem criar nada.
func TestNewFlagsComentarioSemFechar(t *testing.T) {
	cfg := triageProject(t, triageTemplate+"\n<!-- nota sem fim\n")
	before := snapshot(t, cfg.Root())
	_, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Flags: []string{"require_security"}}, now)
	if err == nil || !strings.Contains(err.Error(), "Security") {
		t.Fatalf("erro = %v, esperava citar a seção Security", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}
