package delivery

import (
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/report"
)

// Referência externa da delivery (TD-49): o ID da tarefa num gestor de fora
// (Wrike, Jira, issue), gravado em "ref:" no front matter para o git-delivery
// levá-lo ao commit.

func TestNewRef(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string // vazio: report do esquema
		ref     string
		flags   []string
		want    string // valor de Front["ref"]; vazio: sem a chave
		errPart string
	}{
		{name: "template: ref no front matter", tmpl: triageTemplate, ref: "WRK-4431", want: "WRK-4431"},
		{name: "esquema: ref no front matter", ref: "4431329797", want: "4431329797"},
		{name: "espaços nas pontas saem", tmpl: triageTemplate, ref: "  PROJ-12  ", want: "PROJ-12"},
		{name: "issue com #", tmpl: triageTemplate, ref: "#128", want: "#128"},
		{name: "ponto e sublinhado", tmpl: triageTemplate, ref: "a.b_c-1", want: "a.b_c-1"},
		{name: "junto com flags", tmpl: triageTemplate, ref: "WRK-1", flags: []string{"require_security"}, want: "WRK-1"},
		{name: "sem ref, sem chave", tmpl: triageTemplate, ref: ""},
		{name: "só espaços vale sem ref", tmpl: triageTemplate, ref: "   "},
		{name: "no limite", tmpl: triageTemplate, ref: strings.Repeat("a", MaxRefLen), want: strings.Repeat("a", MaxRefLen)},
		{name: "longa demais", tmpl: triageTemplate, ref: strings.Repeat("a", MaxRefLen+1), errPart: "ref"},
		{name: "espaço no meio", tmpl: triageTemplate, ref: "WRK 1", errPart: "ref"},
		{name: "colchete", tmpl: triageTemplate, ref: "WRK-1]", errPart: "ref"},
		{name: "dois-pontos", tmpl: triageTemplate, ref: "https://x", errPart: "ref"},
		{name: "começa com hífen", tmpl: triageTemplate, ref: "-1", errPart: "ref"},
		{name: "não ASCII", tmpl: triageTemplate, ref: "tarefa-é", errPart: "ref"},
		{name: "quebra de linha", tmpl: triageTemplate, ref: "a\nb", errPart: "ref"},
		{name: "template já tem ref", tmpl: strings.Replace(triageTemplate, "status: draft\n", "status: draft\nref: <ref>\n", 1),
			ref: "WRK-1", errPart: "ref"},
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
			got, err := New(cfg, NewOptions{Title: "Multa", Prefix: "FEAT", Ref: tt.ref, Flags: tt.flags}, now)
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
			if got.Ref != tt.want {
				t.Errorf("Created.Ref = %q, quero %q", got.Ref, tt.want)
			}
			rep := readFile(t, cfg, got.ReportPath)
			r, err := report.Parse([]byte(rep), 2)
			if err != nil {
				t.Fatalf("report gerado ilegível: %v\n%s", err, rep)
			}
			v, ok := r.Front["ref"]
			if tt.want == "" {
				if ok {
					t.Errorf("front matter com ref %q, quero sem a chave:\n%s", v, rep)
				}
				return
			}
			if !ok || v != tt.want {
				t.Errorf("front matter ref = %q (presente %v), quero %q:\n%s", v, ok, tt.want, rep)
			}
			if len(tt.flags) > 0 && r.Front["require_security"] != "true" {
				t.Errorf("flag perdida com a ref:\n%s", rep)
			}
		})
	}
}

func TestValidRef(t *testing.T) {
	for _, ok := range []string{"1", "WRK-4431", "4431329797", "#12", "a.b_c-1", strings.Repeat("Z", MaxRefLen)} {
		if !report.ValidRef(ok) {
			t.Errorf("ValidRef(%q) = false, quero true", ok)
		}
	}
	for _, bad := range []string{"", " ", "a b", "[1]", "a]", "a:b", "a/b", "-1", ".1", "#", "é", "a\tb", strings.Repeat("Z", MaxRefLen+1)} {
		if report.ValidRef(bad) {
			t.Errorf("ValidRef(%q) = true, quero false", bad)
		}
	}
	if MaxRefLen != report.MaxRefLen {
		t.Errorf("MaxRefLen = %d, report.MaxRefLen = %d", MaxRefLen, report.MaxRefLen)
	}
}

// FuzzApplyRef: qualquer template sem panic; sem erro, o front matter lido de
// volta tem a ref, e a segunda aplicação reprova (a chave já existe).
func FuzzApplyRef(f *testing.F) {
	f.Add(triageTemplate)
	f.Add("---\n---\n")
	f.Add("---\r\nstatus: draft\r\n---\r\n# x\r\n")
	f.Add("---\nRef: x\n---\n")
	f.Add("# sem front matter\n")
	f.Add("---\nstatus: draft\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, content string) {
		out, err := applyRef(content, "WRK-1")
		if err != nil {
			return
		}
		r, err := report.Parse([]byte(out), 2)
		if err != nil {
			return // o template já era ilegível; o validador reprova
		}
		if r.Front["ref"] != "WRK-1" {
			t.Fatalf("ref ausente do resultado:\n%q", out)
		}
		if _, err := applyRef(out, "WRK-1"); err == nil {
			t.Fatalf("segunda aplicação aceita:\n%q", out)
		}
	})
}
