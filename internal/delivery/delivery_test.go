package delivery

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/validate"
)

// Projeto fictício: sistema de empréstimos de uma biblioteca de bairro.

const (
	deliveriesDir = "docs-ai/deliveries"
	indexRel      = deliveriesDir + "/INDEX.md"
	today         = "2026-10-26"
	defaultIDs    = `"delivery_id_pattern": "^(FEAT|FIX)-[0-9]{3,}$", "delivery_prefixes": ["FEAT", "FIX"]`
	indexHead     = "# Deliveries\n\n| ID | Título | Status | Data |\n|---|---|---|---|\n"
)

// now tem fuso diferente de UTC de propósito: a data vem de now como recebido.
var now = time.Date(2026, 10, 26, 23, 30, 0, 0, time.FixedZone("BRT", -3*60*60))

// configJSON monta a configuração do projeto fictício; fields entra no objeto
// (sem vírgula inicial) e substitui prefixos e padrão de ID.
func configJSON(fields string) string {
	if fields != "" {
		fields = ",\n " + fields
	}
	return `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "protected_paths": ["internal/"]` + fields + "}"
}

// project cria a raiz do projeto com a configuração, os arquivos e os
// diretórios informados e devolve a configuração carregada.
func project(t *testing.T, fields string, files map[string]string, dirs ...string) *config.Config {
	t.Helper()
	root := filepath.Join(t.TempDir(), "biblioteca")
	files = mergeFiles(files, map[string]string{config.FileName: configJSON(fields)})
	for _, d := range append([]string{"."}, dirs...) {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func mergeFiles(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// snapshot devolve o conteúdo de toda a árvore sob dir (diretórios como "<dir>").
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, lerr := os.Readlink(p)
			if lerr != nil {
				return lerr
			}
			out[rel] = "<link " + target + ">"
		case d.IsDir():
			out[rel] = "<dir>"
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if w, ok := after[k]; !ok {
			t.Errorf("%s sumiu", k)
		} else if v != w {
			t.Errorf("%s mudou:\nantes:  %q\ndepois: %q", k, v, w)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s foi criado", k)
		}
	}
}

func readFile(t *testing.T, cfg *config.Config, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.Root(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNew(t *testing.T) {
	row := func(id, title string) string { return "| " + id + " | " + title + " | done | 2026-01-01 |\n" }
	newRow := func(id string) string { return "| " + id + " | Multa por atraso | draft | " + today + " |\n" }
	feat := NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}
	tests := []struct {
		name      string
		fields    string // padrão: defaultIDs
		index     *string
		files     map[string]string
		dirs      []string
		opts      NewOptions
		wantID    string
		wantIndex string
		wantErr   string
	}{
		{
			name:      "primeiro ID",
			index:     ptr(indexHead),
			opts:      feat,
			wantID:    "FEAT-001",
			wantIndex: indexHead + newRow("FEAT-001"),
		},
		{
			name:      "sequência a partir do índice, com link e código na célula",
			index:     ptr(indexHead + row("FEAT-001", "Cadastro de leitores") + row("[FEAT-002](FEAT-002/report.md)", "Reserva") + row("`FIX-009`", "Login")),
			opts:      feat,
			wantID:    "FEAT-003",
			wantIndex: indexHead + row("FEAT-001", "Cadastro de leitores") + row("[FEAT-002](FEAT-002/report.md)", "Reserva") + row("`FIX-009`", "Login") + newRow("FEAT-003"),
		},
		{
			name:      "outro prefixo tem sequência própria",
			index:     ptr(indexHead + row("FEAT-004", "Cadastro") + row("**FIX-009**", "Login")),
			opts:      NewOptions{Title: "Multa por atraso", Prefix: "FIX"},
			wantID:    "FIX-010",
			wantIndex: indexHead + row("FEAT-004", "Cadastro") + row("**FIX-009**", "Login") + newRow("FIX-010"),
		},
		{
			name:      "diretório sem linha no índice",
			index:     ptr(indexHead + row("FEAT-001", "Cadastro")),
			dirs:      []string{deliveriesDir + "/FEAT-004"},
			opts:      feat,
			wantID:    "FEAT-005",
			wantIndex: indexHead + row("FEAT-001", "Cadastro") + newRow("FEAT-005"),
		},
		{
			name:      "buracos na sequência: maior mais um",
			index:     ptr(indexHead + row("FEAT-007", "Reserva") + row("FEAT-001", "Cadastro")),
			opts:      feat,
			wantID:    "FEAT-008",
			wantIndex: indexHead + row("FEAT-007", "Reserva") + row("FEAT-001", "Cadastro") + newRow("FEAT-008"),
		},
		{
			name:      "número passa para 4 dígitos",
			index:     ptr(indexHead + row("FEAT-999", "Reserva")),
			opts:      feat,
			wantID:    "FEAT-1000",
			wantIndex: indexHead + row("FEAT-999", "Reserva") + newRow("FEAT-1000"),
		},
		{
			name:      "número com 4 dígitos e zeros à esquerda",
			index:     ptr(indexHead + row("FEAT-1234", "Reserva") + row("FEAT-0042", "Cadastro")),
			opts:      feat,
			wantID:    "FEAT-1235",
			wantIndex: indexHead + row("FEAT-1234", "Reserva") + row("FEAT-0042", "Cadastro") + newRow("FEAT-1235"),
		},
		{
			name: "prefixo sensível a caixa e ID só como célula inteira",
			index: ptr("A FEAT-050 é citada em texto.\n\n" + indexHead + row("feat-010", "Minúsculo") +
				row("FEATX-020", "Outro prefixo") + row("FEAT-3a", "Sem número") + row("FEAT-060 nota", "Célula com texto")),
			opts:   feat,
			wantID: "FEAT-001",
			wantIndex: "A FEAT-050 é citada em texto.\n\n" + indexHead + row("feat-010", "Minúsculo") +
				row("FEATX-020", "Outro prefixo") + row("FEAT-3a", "Sem número") + row("FEAT-060 nota", "Célula com texto") + newRow("FEAT-001"),
		},
		{
			// Divergência do original, que descartava comentários HTML: toda
			// célula de linha de tabela conta, como no validador, para nunca
			// gerar um ID que ele já considere registrado (TD-20). A linha
			// nova vai para a primeira tabela com coluna de ID fora de comentário e
			// de código.
			name: "comentário e bloco de código contam para o número mas não recebem a linha",
			index: ptr("```\n| ID | Título |\n|---|---|\n| FEAT-080 | exemplo |\n```\n\n<!--\n| ID | Título |\n|---|---|\n| FEAT-070 | exemplo |\n-->\n\n" +
				indexHead + row("FEAT-001", "Cadastro")),
			opts:   feat,
			wantID: "FEAT-081",
			wantIndex: "```\n| ID | Título |\n|---|---|\n| FEAT-080 | exemplo |\n```\n\n<!--\n| ID | Título |\n|---|---|\n| FEAT-070 | exemplo |\n-->\n\n" +
				indexHead + row("FEAT-001", "Cadastro") + newRow("FEAT-081"),
		},
		{
			name:      "linha nova no fim da primeira tabela, antes do texto e de outra tabela",
			index:     ptr(indexHead + row("FEAT-001", "Cadastro") + "\nNotas da equipe.\n\n| ID | Arquivo |\n|---|---|\n| FEAT-020 | antigo |\n"),
			opts:      feat,
			wantID:    "FEAT-021",
			wantIndex: indexHead + row("FEAT-001", "Cadastro") + newRow("FEAT-021") + "\nNotas da equipe.\n\n| ID | Arquivo |\n|---|---|\n| FEAT-020 | antigo |\n",
		},
		{
			// Índice com tabela de prefixos antes da de registro: a linha vai
			// para a primeira tabela com coluna de ID, nunca para a de prefixos.
			name: "primeira tabela sem coluna de ID é pulada",
			index: ptr("## Prefixos\n\n| Frente | Prefixo |\n|---|---|\n| Acervo | `FEAT` |\n\n## Registro\n\n" +
				"| ID | Título | Status | PR/MR |\n|---|---|---|---|\n| FEAT-001 | Cadastro | done | !1 |\n"),
			opts:   feat,
			wantID: "FEAT-002",
			wantIndex: "## Prefixos\n\n| Frente | Prefixo |\n|---|---|\n| Acervo | `FEAT` |\n\n## Registro\n\n" +
				"| ID | Título | Status | PR/MR |\n|---|---|---|---|\n| FEAT-001 | Cadastro | done | !1 |\n| FEAT-002 | Multa por atraso | draft |  |\n",
		},
		{
			name:      "colunas por título normalizado; desconhecidas ficam vazias",
			index:     ptr("| **Delivery** | Title | Date | STATUS | Responsável |\n|:---|---|---:|:---:|---|\n"),
			opts:      feat,
			wantID:    "FEAT-001",
			wantIndex: "| **Delivery** | Title | Date | STATUS | Responsável |\n|:---|---|---:|:---:|---|\n| FEAT-001 | Multa por atraso | " + today + " | draft |  |\n",
		},
		{
			name:      "coluna delivery_id",
			index:     ptr("| delivery_id | Título |\n|---|---|\n"),
			opts:      feat,
			wantID:    "FEAT-001",
			wantIndex: "| delivery_id | Título |\n|---|---|\n| FEAT-001 | Multa por atraso |\n",
		},
		{
			name:      "índice CRLF",
			index:     ptr(strings.ReplaceAll(indexHead+row("FEAT-001", "Cadastro")+"\nFim.\n", "\n", "\r\n")),
			opts:      feat,
			wantID:    "FEAT-002",
			wantIndex: strings.ReplaceAll(indexHead+row("FEAT-001", "Cadastro")+newRow("FEAT-002")+"\nFim.\n", "\n", "\r\n"),
		},
		{
			name:      "índice sem quebra de linha final",
			index:     ptr(indexHead + strings.TrimSuffix(row("FEAT-001", "Cadastro"), "\n")),
			opts:      feat,
			wantID:    "FEAT-002",
			wantIndex: indexHead + row("FEAT-001", "Cadastro") + strings.TrimSuffix(newRow("FEAT-002"), "\n"),
		},
		{
			name:      "prefixo único dispensa --prefix",
			fields:    `"delivery_prefixes": ["FEAT"]`,
			index:     ptr(indexHead),
			opts:      NewOptions{Title: "Multa por atraso"},
			wantID:    "FEAT-001",
			wantIndex: indexHead + newRow("FEAT-001"),
		},
		{
			name:      "sem delivery_id_pattern o ID não é conferido",
			fields:    `"delivery_prefixes": ["FEAT"]`,
			index:     ptr(indexHead + row("FEAT-999", "Reserva")),
			opts:      feat,
			wantID:    "FEAT-1000",
			wantIndex: indexHead + row("FEAT-999", "Reserva") + newRow("FEAT-1000"),
		},
		{
			name:      "título com espaços nas pontas e 120 caracteres acentuados",
			index:     ptr(indexHead),
			opts:      NewOptions{Title: "  " + strings.Repeat("é", 120) + "  ", Prefix: "FEAT"},
			wantID:    "FEAT-001",
			wantIndex: indexHead + "| FEAT-001 | " + strings.Repeat("é", 120) + " | draft | " + today + " |\n",
		},

		// Prefixo.
		{name: "prefixo vazio com vários configurados", index: ptr(indexHead), opts: NewOptions{Title: "Multa"}, wantErr: "informe o prefixo"},
		{name: "prefixo vazio sem prefixo configurado", fields: `"delivery_prefixes": []`, index: ptr(indexHead), opts: NewOptions{Title: "Multa"}, wantErr: "delivery_prefixes está vazio"},
		{name: "prefixo informado sem prefixo configurado", fields: `"delivery_prefixes": []`, index: ptr(indexHead), opts: feat, wantErr: "não está em delivery_prefixes"},
		{name: "prefixo fora da lista", index: ptr(indexHead), opts: NewOptions{Title: "Multa", Prefix: "BUG"}, wantErr: "não está em delivery_prefixes"},
		{name: "prefixo com caixa diferente", index: ptr(indexHead), opts: NewOptions{Title: "Multa", Prefix: "feat"}, wantErr: "não está em delivery_prefixes"},
		{name: "prefixo com barra vertical", fields: `"delivery_prefixes": ["A|B"]`, index: ptr(indexHead), opts: NewOptions{Title: "Multa"}, wantErr: "ID gerado"},

		// Título.
		{name: "título vazio", index: ptr(indexHead), opts: NewOptions{Prefix: "FEAT"}, wantErr: "título é obrigatório"},
		{name: "título só com espaços", index: ptr(indexHead), opts: NewOptions{Title: "   ", Prefix: "FEAT"}, wantErr: "título é obrigatório"},
		{name: "título com barra vertical", index: ptr(indexHead), opts: NewOptions{Title: "Multa | atraso", Prefix: "FEAT"}, wantErr: `"|"`},
		{name: "título com quebra de linha", index: ptr(indexHead), opts: NewOptions{Title: "Multa\natraso", Prefix: "FEAT"}, wantErr: "uma linha"},
		{name: "título com quebra de linha final", index: ptr(indexHead), opts: NewOptions{Title: "Multa\r\n", Prefix: "FEAT"}, wantErr: "uma linha"},
		{name: "título com tabulação", index: ptr(indexHead), opts: NewOptions{Title: "Multa\tatraso", Prefix: "FEAT"}, wantErr: "caractere de controle"},
		{name: "título longo", index: ptr(indexHead), opts: NewOptions{Title: strings.Repeat("a", 121), Prefix: "FEAT"}, wantErr: "120"},
		{name: "título com comentário HTML", index: ptr(indexHead), opts: NewOptions{Title: "Multa <!-- oculta", Prefix: "FEAT"}, wantErr: "comentário HTML"},
		{name: "título com UTF-8 inválido", index: ptr(indexHead), opts: NewOptions{Title: "Multa \xff", Prefix: "FEAT"}, wantErr: "UTF-8"},

		// ID gerado × delivery_id_pattern.
		{
			name:    "ID fora do delivery_id_pattern",
			fields:  `"delivery_id_pattern": "^FEAT-[0-9]{3}$", "delivery_prefixes": ["FEAT"]`,
			index:   ptr(indexHead + row("FEAT-999", "Reserva")),
			opts:    feat,
			wantErr: "configuração incoerente",
		},
		{
			name:    "número grande demais para continuar a sequência",
			index:   ptr(indexHead + row("FEAT-99999999999999999999", "Reserva")),
			opts:    feat,
			wantErr: "FEAT-99999999999999999999",
		},

		// Índice.
		{name: "índice ausente", dirs: []string{deliveriesDir}, opts: feat, wantErr: "não existe"},
		{name: "índice é diretório", dirs: []string{indexRel}, opts: feat, wantErr: "não é um arquivo regular"},
		{name: "índice sem tabela", index: ptr("# Deliveries\n\nNenhuma delivery.\n"), opts: feat, wantErr: "tabela com cabeçalho"},
		{name: "tabela sem linha separadora", index: ptr("| ID | Título |\n| FEAT-001 | Cadastro |\n"), opts: feat, wantErr: "tabela com cabeçalho"},
		{name: "tabela só dentro de comentário", index: ptr("<!--\n" + indexHead + "-->\n"), opts: feat, wantErr: "tabela com cabeçalho"},
		{name: "tabela só dentro de bloco de código", index: ptr("~~~\n" + indexHead + "~~~\n"), opts: feat, wantErr: "tabela com cabeçalho"},
		{name: "sem coluna de ID", index: ptr("| Código | Título |\n|---|---|\n"), opts: feat, wantErr: "nenhuma tabela com coluna de ID"},
		{name: "nenhuma das tabelas com coluna de ID", index: ptr("| Frente | Prefixo |\n|---|---|\n\n| Código | Título |\n|---|---|\n"), opts: feat, wantErr: "nenhuma tabela com coluna de ID"},
		{name: "tabela de ID só em comentário", index: ptr("| Frente | Prefixo |\n|---|---|\n<!--\n" + indexHead + "-->\n"), opts: feat, wantErr: "nenhuma tabela com coluna de ID"},
		{
			name:    "deliveries_dir ausente",
			fields:  defaultIDs + `, "index_file": "docs/INDEX.md"`,
			files:   map[string]string{"docs/INDEX.md": indexHead},
			opts:    feat,
			wantErr: "deliveries_dir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := tt.fields
			if fields == "" {
				fields = defaultIDs
			}
			files := tt.files
			if tt.index != nil {
				files = mergeFiles(files, map[string]string{indexRel: *tt.index})
			}
			cfg := project(t, fields, files, tt.dirs...)
			before := snapshot(t, filepath.Dir(cfg.Root()))

			got, err := New(cfg, tt.opts, now)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("New = %+v, esperava erro com %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("erro = %q, esperava conter %q", err, tt.wantErr)
				}
				sameTree(t, before, snapshot(t, filepath.Dir(cfg.Root())))
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			want := Created{
				ID:         tt.wantID,
				ReportPath: deliveriesDir + "/" + tt.wantID + "/report.md",
				IndexPath:  cfg.IndexFile,
			}
			if got != want {
				t.Fatalf("New = %+v, esperava %+v", got, want)
			}
			if idx := readFile(t, cfg, cfg.IndexFile); idx != tt.wantIndex {
				t.Errorf("índice:\n%q\nesperado:\n%q", idx, tt.wantIndex)
			}
			if rep := readFile(t, cfg, got.ReportPath); !strings.HasPrefix(rep, "---\ndelivery_id: "+tt.wantID+"\n") {
				t.Errorf("report não começa com o delivery_id:\n%s", rep)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestNewReportFromSchema(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		want   string
	}{
		{
			name:   "esquema padrão",
			fields: defaultIDs,
			want: `---
delivery_id: FEAT-001
status: draft
require_release_ops: false
require_security: false
---

# FEAT-001 — Multa por atraso

## PM/Triage
- Escopo fechado: <preencher>
- Criterios de aceite: <preencher>
- Riscos: <preencher>

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

## Final Checklist
- [ ] <preencher>
`,
		},
		{
			// Seção de nível 1: não há nível acima para o título da delivery.
			// Front com chaves extras (vazias), flag repetida em
			// front_required, seção só em fields_required, seção sem campos
			// e checklist fora de sections_required.
			name: "esquema próprio",
			fields: defaultIDs + `, "report_schema": {"section_level": 1,
				"front_required": ["delivery_id", "Responsavel", "require_dados", "status"],
				"sections_required": ["Contexto", "Entrega"],
				"fields_required": {"Entrega": ["Arquivos"], "Riscos": ["Impacto", "Mitigação"]},
				"flag_sections": {"require_dados": {"section": "Dados", "fields": ["Migração"]}, "require_auditoria": {"section": "Auditoria"}},
				"checklist_section": "Conferência"}`,
			want: `---
delivery_id: FEAT-001
status: draft
Responsavel:
require_dados: false
require_auditoria: false
---

# Contexto

# Entrega
- Arquivos: <preencher>

# Riscos
- Impacto: <preencher>
- Mitigação: <preencher>

# Conferência
- [ ] <preencher>
`,
		},
		{
			name: "front_required sem delivery_id nem status: os dois entram mesmo assim",
			fields: defaultIDs + `, "report_schema": {"section_level": 3, "front_required": ["titulo"],
				"sections_required": ["Resumo"], "fields_required": {"Resumo": ["Texto"]}}`,
			want: `---
delivery_id: FEAT-001
status: draft
titulo:
---

## FEAT-001 — Multa por atraso

### Resumo
- Texto: <preencher>
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := project(t, tt.fields, map[string]string{indexRel: indexHead})
			got, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if rep := readFile(t, cfg, got.ReportPath); rep != tt.want {
				t.Errorf("report:\n%s\nesperado:\n%s", rep, tt.want)
			}
		})
	}
}

func TestNewReportFromTemplate(t *testing.T) {
	const tmpl = "---\ndelivery_id: <delivery_id>\ntitulo: <titulo>\ndata: <data>\n---\n\n# <delivery_id>: <titulo>\n\nCriado em <data>. <outro> fica como está.\n"
	cfg := project(t, defaultIDs+`, "report_template": "docs-ai/templates/report.md"`,
		map[string]string{indexRel: indexHead, "docs-ai/templates/report.md": tmpl})
	got, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := "---\ndelivery_id: FEAT-001\ntitulo: Multa por atraso\ndata: " + today + "\n---\n\n# FEAT-001: Multa por atraso\n\nCriado em " + today + ". <outro> fica como está.\n"
	if rep := readFile(t, cfg, got.ReportPath); rep != want {
		t.Errorf("report:\n%q\nesperado:\n%q", rep, want)
	}
	if tpl := readFile(t, cfg, "docs-ai/templates/report.md"); tpl != tmpl {
		t.Errorf("template alterado: %q", tpl)
	}
}

func TestNewTemplateMissing(t *testing.T) {
	cfg := project(t, defaultIDs+`, "report_template": "docs-ai/templates/report.md"`, map[string]string{indexRel: indexHead})
	before := snapshot(t, cfg.Root())
	_, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
	if err == nil || !strings.Contains(err.Error(), "report_template") {
		t.Fatalf("erro = %v, esperava falha de report_template", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}

func TestNewInvalidConfig(t *testing.T) {
	opts := NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}
	if _, err := New(nil, opts, now); err == nil {
		t.Error("New(nil) não falhou")
	}
	if _, err := New(&config.Config{DeliveriesDir: deliveriesDir, IndexFile: indexRel}, opts, now); err == nil {
		t.Error("New com config sem raiz não falhou")
	}
}

// TestNewNeverOverwrites simula outra criação entre o cálculo do ID e a
// escrita: o diretório da delivery já existe com um report.
func TestNewNeverOverwrites(t *testing.T) {
	cfg := project(t, defaultIDs, map[string]string{indexRel: indexHead})
	p, err := prepare(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	other := filepath.Join(cfg.Root(), "docs-ai", "deliveries", "FEAT-001")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "report.md"), []byte("report de outra pessoa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, cfg.Root())

	err = p.apply()
	if err == nil || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("apply = %v, esperava erro de diretório existente", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}

// TestNewRollbackWhenIndexFails: o diretório do índice é somente leitura, o
// rename falha e o diretório criado para a delivery é removido.
func TestNewRollbackWhenIndexFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignora permissão de diretório")
	}
	cfg := project(t, defaultIDs+`, "index_file": "registro/INDEX.md"`,
		map[string]string{"registro/INDEX.md": indexHead}, deliveriesDir)
	regDir := filepath.Join(cfg.Root(), "registro")
	if err := os.Chmod(regDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(regDir, 0o755) })
	before := snapshot(t, cfg.Root())

	_, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
	if err == nil || !strings.Contains(err.Error(), "índice") {
		t.Fatalf("erro = %v, esperava falha no índice", err)
	}
	sameTree(t, before, snapshot(t, cfg.Root()))
}

// TestNewIndexChangedDuringCreation: outra escrita no índice entre a leitura
// e o rename não é perdida; a criação é desfeita.
func TestNewIndexChangedDuringCreation(t *testing.T) {
	cfg := project(t, defaultIDs, map[string]string{indexRel: indexHead})
	indexPath := filepath.Join(cfg.Root(), filepath.FromSlash(indexRel))
	concurrent := indexHead + "| FEAT-001 | Cadastro de leitores | draft | " + today + " |\n"
	testHookBeforeRename = func() {
		if err := os.WriteFile(indexPath, []byte(concurrent), 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { testHookBeforeRename = nil })

	_, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
	if err == nil || !strings.Contains(err.Error(), "mudou") {
		t.Fatalf("erro = %v, esperava índice alterado durante a criação", err)
	}
	if got := readFile(t, cfg, indexRel); got != concurrent {
		t.Errorf("índice = %q, esperava a escrita concorrente preservada", got)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.Root(), filepath.FromSlash(deliveriesDir)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "INDEX.md" {
			t.Errorf("sobrou %s em deliveries_dir", e.Name())
		}
	}
}

func TestNewPreservesIndexMode(t *testing.T) {
	cfg := project(t, defaultIDs, map[string]string{indexRel: indexHead})
	indexPath := filepath.Join(cfg.Root(), filepath.FromSlash(indexRel))
	if err := os.Chmod(indexPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now); err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("permissão do índice = %v, esperava 0640", info.Mode().Perm())
	}
}

func TestConfine(t *testing.T) {
	root := filepath.Join(t.TempDir(), "biblioteca")
	tests := []struct {
		rel  string
		want string // "" = erro
	}{
		{"docs-ai/deliveries", filepath.Join(root, "docs-ai", "deliveries")},
		{"docs-ai/./deliveries/../deliveries/FEAT-001", filepath.Join(root, "docs-ai", "deliveries", "FEAT-001")},
		{"docs-ai/deliveries/../../../fora", ""},
		{"../fora", ""},
		{"..", ""},
		{".", ""},
		{"", ""},
		{"docs-ai/..", ""},
		{"/etc/passwd", ""},
	}
	for _, tt := range tests {
		got, err := confine(root, tt.rel)
		switch {
		case tt.want == "" && err == nil:
			t.Errorf("confine(%q) = %q, esperava erro", tt.rel, got)
		case tt.want != "" && err != nil:
			t.Errorf("confine(%q): %v", tt.rel, err)
		case got != tt.want:
			t.Errorf("confine(%q) = %q, esperava %q", tt.rel, got, tt.want)
		}
	}
}

// TestNewStaysInsideRoot: nada é gravado fora da raiz, nem por prefixo com
// "..", nem por deliveries_dir ou índice que sejam symlink para fora (TD-17).
func TestNewStaysInsideRoot(t *testing.T) {
	t.Run("prefixo com ..", func(t *testing.T) {
		cfg := project(t, `"delivery_prefixes": ["../../fora"]`, map[string]string{indexRel: indexHead})
		parent := filepath.Dir(cfg.Root())
		before := snapshot(t, parent)
		_, err := New(cfg, NewOptions{Title: "Multa por atraso"}, now)
		if err == nil {
			t.Fatal("New aceitou prefixo que sai da raiz")
		}
		sameTree(t, before, snapshot(t, parent))
	})

	symlinkCase := func(t *testing.T, link, target string, files map[string]string) {
		t.Helper()
		cfg := project(t, defaultIDs, files)
		parent := filepath.Dir(cfg.Root())
		outside := filepath.Join(parent, "fora")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "INDEX.md"), []byte(indexHead), 0o644); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(cfg.Root(), filepath.FromSlash(link))
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, target), linkPath); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
		before := snapshot(t, parent)
		_, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
		if err == nil || !strings.Contains(err.Error(), "fora da raiz") {
			t.Fatalf("erro = %v, esperava recusa por sair da raiz", err)
		}
		sameTree(t, before, snapshot(t, parent))
	}
	t.Run("deliveries_dir é symlink para fora", func(t *testing.T) {
		symlinkCase(t, deliveriesDir, "", nil)
	})
	t.Run("índice é symlink para fora", func(t *testing.T) {
		symlinkCase(t, indexRel, "INDEX.md", nil)
	})
}

// TestValidatorSync: o report criado por New reprova no validador só por
// placeholder e checklist; preenchido, passa (sem git).
func TestValidatorSync(t *testing.T) {
	cfg := project(t, defaultIDs, map[string]string{indexRel: indexHead + "| FEAT-001 | Cadastro de leitores | done | 2026-01-01 |\n"})
	got, err := New(cfg, NewOptions{Title: "Multa por atraso", Prefix: "FEAT"}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run := func() (int, string) {
		var stdout, stderr bytes.Buffer
		code := validate.Run(validate.Options{
			Target: got.ID,
			NoGit:  true,
			Getwd:  func() (string, error) { return cfg.Root(), nil },
		}, &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}

	code, out := run()
	if code != validate.ExitFail {
		t.Fatalf("report recém-criado: código %d, esperava %d\n%s", code, validate.ExitFail, out)
	}
	var errs int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ERRO:") {
			continue
		}
		errs++
		if !strings.Contains(line, "placeholder") && !strings.HasPrefix(line, "ERRO: checklist") {
			t.Errorf("erro que não é de placeholder nem de checklist: %s", line)
		}
	}
	if errs == 0 {
		t.Fatalf("nenhum erro listado:\n%s", out)
	}

	reportPath := filepath.Join(cfg.Root(), filepath.FromSlash(got.ReportPath))
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.ReplaceAll(string(data), "<preencher>", "conferido na revisão")
	filled = strings.ReplaceAll(filled, "- [ ]", "- [x]")
	if err := os.WriteFile(reportPath, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run(); code != validate.ExitOK {
		t.Fatalf("report preenchido: código %d, esperava 0\n%s", code, out)
	}
}

// inSomeCell é o oráculo do fuzz, independente do parser: id é parte de algum
// trecho entre "|" de uma linha que começa com "|".
func inSomeCell(content, id string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		for _, seg := range strings.Split(line, "|") {
			if strings.Contains(seg, id) {
				return true
			}
		}
	}
	return false
}

func FuzzIndexTable(f *testing.F) {
	for _, seed := range []string{
		"",
		"|",
		"||\n|-|",
		indexHead,
		indexHead + "| FEAT-001 | a | b | c |\n| [FEAT-002](x) | `FIX-003` |",
		strings.ReplaceAll(indexHead+"| **FEAT-010** |\n", "\n", "\r\n"),
		"<!--\n" + indexHead + "-->\n" + indexHead,
		"```\n" + indexHead + "```\n| ID |\n|:-:|\n| FEAT-7 |",
		"| a <!-- | b |\n|---|\n--> | FEAT-001 |\n",
		"| ID |\n|---|\n| FEAT-001 |\n```\n| FEAT-002 |\n",
		"\ufeff| ID |\n| --- |\n|\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		idx, err := parseIndex(content)
		if err != nil {
			return
		}
		for _, c := range idx.cells {
			if c == "" {
				t.Fatal("célula vazia devolvida")
			}
			if !inSomeCell(content, c) {
				t.Fatalf("célula %q não está em linha de tabela de %q", c, content)
			}
		}
		if len(idx.header) == 0 || idx.last < 0 || idx.last >= len(idx.lines) {
			t.Fatalf("tabela inconsistente: cabeçalho %q, última linha %d de %d", idx.header, idx.last, len(idx.lines))
		}
		n, err := nextNumber("FEAT", idx.cells, nil)
		if err != nil {
			return
		}
		id := fmt.Sprintf("FEAT-%03d", n)
		if inSomeCell(content, id) && containsCell(idx.cells, id) {
			t.Fatalf("ID %q gerado já está no índice", id)
		}
		row, err := idx.row(id, "Título de fuzz", today)
		if err != nil {
			return // sem coluna de ID
		}
		out := idx.insert(row)
		again, err := parseIndex(out)
		if err != nil {
			t.Fatalf("índice com a linha nova não é mais legível: %v\n%q", err, out)
		}
		if !containsCell(again.cells, id) {
			t.Fatalf("ID %q não aparece numa célula depois da inserção:\n%q", id, out)
		}
		if again.last != idx.last+1 {
			t.Fatalf("linha nova fora da primeira tabela: última linha %d, esperava %d\n%q", again.last, idx.last+1, out)
		}
	})
}

func containsCell(cells []string, id string) bool {
	for _, c := range cells {
		if c == id {
			return true
		}
	}
	return false
}
