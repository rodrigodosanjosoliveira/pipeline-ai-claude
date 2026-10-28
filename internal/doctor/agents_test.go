package doctor

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
)

const agentsDir = ".claude/agents"

// agentDoc monta um subagente com o front matter dado.
func agentDoc(front string) string {
	return "---\n" + front + "\n---\n\n# Subagente\n\nCorpo do subagente.\n"
}

// writeAgent grava .claude/agents/<name>.md com o conteúdo dado.
func writeAgent(t *testing.T, root, name, body string) {
	t.Helper()
	writeRel(t, root, agentsDir+"/"+name+".md", body)
}

// setEditors troca editor_agents na configuração gerada.
func setEditors(t *testing.T, root string, editors ...string) {
	t.Helper()
	p := filepath.Join(root, config.FileName)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["editor_agents"] = editors
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// setReportWriters troca report_writers na configuração gerada (TD-35).
func setReportWriters(t *testing.T, root string, writers ...string) {
	t.Helper()
	p := filepath.Join(root, config.FileName)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["report_writers"] = writers
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// releaseRel tira rel de files e o põe em released no lock.
func releaseRel(t *testing.T, root, rel string) {
	t.Helper()
	l, err := manifest.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Files[rel]; !ok {
		t.Fatalf("%s fora de files no lock", rel)
	}
	delete(l.Files, rel)
	l.Released = append(l.Released, rel)
	if err := manifest.Write(root, l); err != nil {
		t.Fatal(err)
	}
}

// countAgents conta os .md em .claude/agents.
func countAgents(t *testing.T, root string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, ".claude", "agents", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

const reviewerDesc = "description: Revisa o diff da delivery do catálogo de livros."

func TestCheckAgents(t *testing.T) {
	reviewer := func(front string) func(t *testing.T, root string) {
		return func(t *testing.T, root string) { writeAgent(t, root, "code-reviewer", agentDoc(front)) }
	}
	symlinkOK := func(t *testing.T) {
		t.Helper()
		dir := t.TempDir()
		if err := os.Symlink(dir, filepath.Join(dir, "link")); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
	}
	tests := []struct {
		name    string
		modules []string
		symlink bool
		mutate  func(t *testing.T, root string)
		status  Status
		msg     []string // trechos esperados na mensagem, nesta ordem
		noMsg   []string // trechos que não podem aparecer na mensagem
		action  string   // trecho esperado na ação ("" = ação vazia)
		count   bool     // mensagem traz a contagem de subagentes no disco
	}{
		{name: "gerado pelo scaffold", status: StatusOK, count: true},
		{
			name:    "gerado com todos os módulos",
			modules: slices.Sorted(maps.Keys(config.Modules)),
			status:  StatusOK,
			count:   true,
		},
		{
			name:   "revisor com Edit",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc + "\ntools: Read, Grep, Edit"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: Edit"},
			noMsg:  []string{"Read", "Grep"},
			action: "editor_agents",
		},
		{
			// report_writers escrevem só em deliveries_dir, pelo hook (TD-35).
			name: "revisor com Edit em report_writers",
			mutate: func(t *testing.T, root string) {
				reviewer("name: code-reviewer\n"+reviewerDesc+"\ntools: Read, Grep, Edit, Write, MultiEdit")(t, root)
				setReportWriters(t, root, "code-reviewer")
			},
			status: StatusOK,
			count:  true,
		},
		{
			name: "revisor em report_writers continua sem Bash",
			mutate: func(t *testing.T, root string) {
				reviewer("name: code-reviewer\n"+reviewerDesc+"\ntools: Read, Edit, Bash")(t, root)
				setReportWriters(t, root, "code-reviewer")
			},
			status: StatusWarn,
			msg:    []string{"code-reviewer: Bash"},
			noMsg:  []string{"code-reviewer: Edit"},
			action: "report_writers",
		},
		{
			name: "report_writers sem diferenciar caixa no nome do agente",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "Code-Reviewer", agentDoc("name: Code-Reviewer\n"+reviewerDesc+"\ntools: Read, Edit"))
				setReportWriters(t, root, "code-reviewer")
			},
			status: StatusOK,
			count:  true,
		},
		{
			name:   "revisor com Bash",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc + "\ntools: Read, Bash"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: Bash"},
			action: "editor_agents",
		},
		{
			name:   "revisor com Bash restrito por padrão",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc + "\ntools: Read, Bash(git diff:*)"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: Bash"},
			action: "editor_agents",
		},
		{
			name:   "revisor com todas as ferramentas de escrita, em ordem",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc + "\ntools: Write, NotebookEdit, Read, MultiEdit, Edit, Bash"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: Bash", "code-reviewer: Edit", "code-reviewer: MultiEdit", "code-reviewer: NotebookEdit", "code-reviewer: Write"},
			action: "editor_agents",
		},
		{
			name:   "nome de ferramenta sem diferenciar caixa",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc + "\ntools: Read, edit"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: edit"},
			action: "editor_agents",
		},
		{
			name: "dois subagentes, em ordem de nome",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "security-review", agentDoc("name: security-review\ntools: Read, Bash"))
				writeAgent(t, root, "code-reviewer", agentDoc("name: code-reviewer\ntools: Read, Write"))
			},
			status: StatusWarn,
			msg:    []string{"code-reviewer: Write", "security-review: Bash"},
			action: "editor_agents",
		},
		{
			name: "git-delivery com Bash ok",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "git-delivery", agentDoc("name: git-delivery\ntools: Read, Bash"))
			},
			status: StatusOK,
		},
		{
			name: "git-delivery com Edit avisa",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "git-delivery", agentDoc("name: git-delivery\ntools: Read, Bash, Edit"))
			},
			status: StatusWarn,
			msg:    []string{"git-delivery: Edit"},
			noMsg:  []string{"git-delivery: Bash"},
			action: "editor_agents",
		},
		{
			name: "editor com Edit ok",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "dev-implementation", agentDoc("name: dev-implementation\ntools: Read, Edit, Write, MultiEdit, NotebookEdit, Bash"))
			},
			status: StatusOK,
		},
		{
			name: "editor tirado de editor_agents avisa",
			mutate: func(t *testing.T, root string) {
				setEditors(t, root, "test-writer", "docs-update", "query-performance")
			},
			status: StatusWarn,
			msg:    []string{"dev-implementation: Bash", "dev-implementation: Edit"},
			noMsg:  []string{"test-writer:", "docs-update:"},
			action: "editor_agents",
		},
		{
			name: "subagente do projeto em editor_agents ok",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "catalogo-livros", agentDoc("name: catalogo-livros\ntools: Read, Edit, Bash"))
				setEditors(t, root, "dev-implementation", "test-writer", "docs-update", "query-performance", "catalogo-livros")
			},
			status: StatusOK,
		},
		{
			name: "subagente do projeto fora do lock é checado",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "catalogo-livros", agentDoc("name: catalogo-livros\ntools: Read, Edit"))
			},
			status: StatusWarn,
			msg:    []string{"catalogo-livros: Edit"},
			action: "editor_agents",
		},
		{
			name: "subagente released também é checado",
			mutate: func(t *testing.T, root string) {
				releaseRel(t, root, agentReview)
				writeAgent(t, root, "code-reviewer", agentDoc("name: code-reviewer\ntools: Read, Edit"))
			},
			status: StatusWarn,
			msg:    []string{"code-reviewer: Edit"},
			action: "editor_agents",
		},
		{
			name:   "lista YAML em bloco com Edit",
			mutate: reviewer("name: code-reviewer\ntools:\n  - Read\n  - Grep\n  - Edit\n" + reviewerDesc),
			status: StatusWarn,
			msg:    []string{"code-reviewer: Edit"},
			noMsg:  []string{"Grep", "Read", "description"},
			action: "editor_agents",
		},
		{
			name:   "lista YAML em bloco limpa",
			mutate: reviewer("name: code-reviewer\ntools:\n  - Read\n  - \"Grep\"\n  - 'Glob'\n" + reviewerDesc),
			status: StatusOK,
		},
		{
			name: "lista YAML em bloco do git-delivery com Bash",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "git-delivery", agentDoc("name: git-delivery\ntools:\n- Read\n- Bash"))
			},
			status: StatusOK,
		},
		{
			name:   "lista YAML em linha",
			mutate: reviewer("name: code-reviewer\ntools: [Read, Grep, Write]"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: Write"},
			action: "editor_agents",
		},
		{
			name:   "outras chaves ignoradas",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc + "\nmodel: sonnet\nmaxTurns: 12\ncolor: blue\ntools: Read, Grep\n# comentário: Edit"),
			status: StatusOK,
		},
		{
			name:   "valor em várias linhas de outra chave ignorado",
			mutate: reviewer("name: code-reviewer\ndescription: >\n  Revisa o diff.\n  Não usa Edit nem Bash.\ntools: Read"),
			status: StatusOK,
		},
		{
			name:   "aspas no name",
			mutate: reviewer("name: \"code-reviewer\"\ntools: Read"),
			status: StatusOK,
		},
		{
			name: "fim de linha CRLF",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "code-reviewer", strings.ReplaceAll(agentDoc("name: code-reviewer\ntools: Read, Edit"), "\n", "\r\n"))
			},
			status: StatusWarn,
			msg:    []string{"code-reviewer: Edit"},
			action: "editor_agents",
		},
		{
			name:   "tools ausente",
			mutate: reviewer("name: code-reviewer\n" + reviewerDesc),
			status: StatusWarn,
			msg:    []string{"code-reviewer: tools ausente"},
			action: "tools",
		},
		{
			name:   "tools vazio",
			mutate: reviewer("name: code-reviewer\ntools:\n" + reviewerDesc),
			status: StatusWarn,
			msg:    []string{"code-reviewer: tools ausente"},
			action: "tools",
		},
		{
			name:   "tools só com vírgulas",
			mutate: reviewer("name: code-reviewer\ntools: , ,"),
			status: StatusWarn,
			msg:    []string{"code-reviewer: tools ausente"},
			action: "tools",
		},
		{
			name:   "name diferente do arquivo",
			mutate: reviewer("name: revisor-geral\ntools: Read"),
			status: StatusError,
			msg:    []string{agentReview, "revisor-geral"},
			action: "name",
		},
		{
			name:   "name com caixa diferente",
			mutate: reviewer("name: Code-Reviewer\ntools: Read"),
			status: StatusError,
			msg:    []string{agentReview, "Code-Reviewer"},
			action: "name",
		},
		{
			name: "front matter sem fechamento",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "code-reviewer", "---\nname: code-reviewer\ntools: Read\n\n# Code Reviewer\n")
			},
			status: StatusError,
			msg:    []string{agentReview, "fechamento"},
			action: "front matter",
		},
		{
			name: "sem front matter",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "code-reviewer", "# Code Reviewer\n\nname: code-reviewer\n")
			},
			status: StatusError,
			msg:    []string{agentReview, "sem front matter"},
			action: "front matter",
		},
		{
			name:   "sem name",
			mutate: reviewer(reviewerDesc + "\ntools: Read"),
			status: StatusError,
			msg:    []string{agentReview, "sem name"},
			action: "name",
		},
		{
			name:   "linha fora do formato",
			mutate: reviewer("name: code-reviewer\ntools Read, Edit"),
			status: StatusError,
			msg:    []string{agentReview, "tools Read, Edit"},
			action: "front matter",
		},
		{
			name:   "tools repetido",
			mutate: reviewer("name: code-reviewer\ntools: Read\ntools: Edit"),
			status: StatusError,
			msg:    []string{agentReview, "tools"},
			action: "front matter",
		},
		{
			name:   "item de tools fora de lista",
			mutate: reviewer("name: code-reviewer\ntools:\n  Read, Edit"),
			status: StatusError,
			msg:    []string{agentReview, "tools"},
			action: "front matter",
		},
		{
			name: "erro e aviso juntos: erro, com o aviso na mensagem",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "code-reviewer", agentDoc("name: revisor-geral\ntools: Read"))
				writeAgent(t, root, "security-review", agentDoc("name: security-review\ntools: Read, Bash"))
			},
			status: StatusError,
			msg:    []string{agentReview, "security-review: Bash"},
			action: "name",
		},
		{
			name: "arquivo que não é .md ignorado",
			mutate: func(t *testing.T, root string) {
				writeRel(t, root, agentsDir+"/LEIAME.txt", "tools: Edit\n")
				if err := os.Mkdir(filepath.Join(root, ".claude", "agents", "rascunhos"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusOK,
		},
		{
			name: "diretório com nome .md",
			mutate: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, ".claude", "agents", "pasta.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusError,
			msg:    []string{agentsDir + "/pasta.md", "não é arquivo regular"},
			action: "front matter",
		},
		{
			name:    "subagente symlink não é seguido",
			symlink: true,
			mutate: func(t *testing.T, root string) {
				out := filepath.Join(t.TempDir(), "fora.md")
				if err := os.WriteFile(out, []byte(agentDoc("name: catalogo-livros\ntools: Read")), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(out, filepath.Join(root, ".claude", "agents", "catalogo-livros.md")); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusError,
			msg:    []string{agentsDir + "/catalogo-livros.md", "symlink"},
			action: "symlink",
		},
		{
			name:    ".claude/agents symlink não é seguido",
			symlink: true,
			mutate: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".claude", "agents")
				out := filepath.Join(t.TempDir(), "agents")
				if err := os.Rename(dir, out); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(out, dir); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusError,
			msg:    []string{agentsDir, "symlink"},
			action: "symlink",
		},
		{
			name:    ".claude symlink não é seguido",
			symlink: true,
			mutate: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".claude")
				out := filepath.Join(t.TempDir(), "claude")
				if err := os.Rename(dir, out); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(out, dir); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusError,
			msg:    []string{".claude", "symlink"},
			action: "symlink",
		},
		{
			name: ".claude/agents é arquivo",
			mutate: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, ".claude", "agents")); err != nil {
					t.Fatal(err)
				}
				writeRel(t, root, agentsDir, "não é diretório")
			},
			status: StatusError,
			msg:    []string{agentsDir, "não é diretório"},
			action: agentsDir,
		},
		{
			name: "sem .claude/agents",
			mutate: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, ".claude", "agents")); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusWarn,
			msg:    []string{agentsDir + " não encontrado"},
			action: "pipeline-claude sync",
		},
		{
			name: ".claude/agents vazio",
			mutate: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".claude", "agents")
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusWarn,
			msg:    []string{"nenhum subagente"},
			action: "pipeline-claude sync",
		},
		{
			name: "config ausente",
			mutate: func(t *testing.T, root string) {
				removeRel(t, root, config.FileName)
				writeAgent(t, root, "code-reviewer", agentDoc("name: revisor-geral\ntools: Edit"))
			},
			status: StatusWarn,
			msg:    []string{"não verificado"},
			action: "configuração",
		},
		{
			name: "config inválida",
			mutate: func(t *testing.T, root string) {
				writeRel(t, root, config.FileName, "{")
				writeAgent(t, root, "code-reviewer", agentDoc("name: revisor-geral\ntools: Edit"))
			},
			status: StatusWarn,
			msg:    []string{"não verificado"},
			action: "configuração",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.symlink {
				symlinkOK(t)
			}
			root := scaffolded(t, tt.modules...)
			if tt.mutate != nil {
				tt.mutate(t, root)
			}
			c := find(t, collect(root, healthyEnv().env()), checkAgents)
			if c.Status != tt.status {
				t.Fatalf("status = %s, quero %s (%s → %s)", c.Status, tt.status, c.Message, c.Action)
			}
			rest := c.Message
			for _, s := range tt.msg {
				i := strings.Index(rest, s)
				if i < 0 {
					t.Errorf("mensagem %q sem %q (ou fora de ordem)", c.Message, s)
					continue
				}
				rest = rest[i+len(s):]
			}
			for _, s := range tt.noMsg {
				if strings.Contains(c.Message, s) {
					t.Errorf("mensagem %q com %q", c.Message, s)
				}
			}
			if tt.count {
				n := countAgents(t, root)
				if n == 0 || !strings.Contains(c.Message, fmt.Sprintf("%d subagente(s)", n)) {
					t.Errorf("mensagem %q sem a contagem %d", c.Message, n)
				}
			}
			if !strings.Contains(c.Action, tt.action) {
				t.Errorf("ação %q sem %q", c.Action, tt.action)
			}
			if tt.action == "" && c.Action != "" {
				t.Errorf("ação = %q, quero vazia", c.Action)
			}
		})
	}
}

func TestParseAgentFrontMatter(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    agentMeta
		errPart string // trecho do erro ("" = sem erro)
	}{
		{name: "vírgulas", in: agentDoc("name: a\ntools: Read, Grep"), want: agentMeta{Name: "a", Tools: []string{"Read", "Grep"}, HasTools: true}},
		{name: "lista em bloco", in: agentDoc("name: a\ntools:\n  - Read\n  - Edit"), want: agentMeta{Name: "a", Tools: []string{"Read", "Edit"}, HasTools: true}},
		{name: "lista em bloco sem recuo", in: agentDoc("tools:\n- Read\nname: a"), want: agentMeta{Name: "a", Tools: []string{"Read"}, HasTools: true}},
		{name: "lista em linha", in: agentDoc("name: a\ntools: [Read, 'Edit']"), want: agentMeta{Name: "a", Tools: []string{"Read", "Edit"}, HasTools: true}},
		{name: "sem tools", in: agentDoc("name: a\nmodel: opus"), want: agentMeta{Name: "a"}},
		{name: "tools vazio", in: agentDoc("name: a\ntools:"), want: agentMeta{Name: "a", HasTools: true}},
		{name: "aspas simples no name", in: agentDoc("name: 'a'\ntools: Read"), want: agentMeta{Name: "a", Tools: []string{"Read"}, HasTools: true}},
		{name: "corpo sem fim de linha depois do fechamento", in: "---\nname: a\ntools: Read\n---", want: agentMeta{Name: "a", Tools: []string{"Read"}, HasTools: true}},
		{name: "linhas em branco e comentário", in: agentDoc("\nname: a\n# tools: Edit\n\ntools: Read"), want: agentMeta{Name: "a", Tools: []string{"Read"}, HasTools: true}},
		{name: "comentário no fim do valor", in: agentDoc("name: a # revisor\ntools: Read, Grep # sem Edit"), want: agentMeta{Name: "a", Tools: []string{"Read", "Grep"}, HasTools: true}},
		{name: "# entre aspas não é comentário", in: agentDoc("name: \"a #1\"\ntools: Read"), want: agentMeta{Name: "a #1", Tools: []string{"Read"}, HasTools: true}},
		{name: "vazio", in: "", errPart: "sem front matter"},
		{name: "sem abertura", in: "name: a\n---\n", errPart: "sem front matter"},
		{name: "abertura com espaço antes", in: " ---\nname: a\n---\n", errPart: "sem front matter"},
		{name: "sem fechamento", in: "---\nname: a\n", errPart: "fechamento"},
		{name: "fechamento com texto", in: "---\nname: a\n--- x\n", errPart: "fechamento"},
		{name: "sem name", in: agentDoc("tools: Read"), errPart: "sem name"},
		{name: "name vazio", in: agentDoc("name:\ntools: Read"), errPart: "sem name"},
		{name: "name repetido", in: agentDoc("name: a\nname: b"), errPart: "repetid"},
		{name: "linha sem dois-pontos", in: agentDoc("name: a\nlixo"), errPart: "lixo"},
		{name: "item fora de lista", in: agentDoc("name: a\ntools:\n  Read"), errPart: "tools"},
		{name: "CR solto no name", in: "---\nname:0\r0\n---", errPart: "controle"},
		{name: "item de lista sem tools", in: agentDoc("name: a\nmodel:\n  - opus"), want: agentMeta{Name: "a"}},
		{name: "lista em bloco depois de valor", in: agentDoc("name: a\ntools: Read\n  - Edit"), errPart: "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAgentFrontMatter([]byte(tt.in))
			if tt.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errPart) {
					t.Fatalf("erro = %v, quero com %q", err, tt.errPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if got.Name != tt.want.Name || got.HasTools != tt.want.HasTools || !slices.Equal(got.Tools, tt.want.Tools) {
				t.Errorf("= %+v, quero %+v", got, tt.want)
			}
		})
	}
}

func TestCheckAgentsOrdem(t *testing.T) {
	// subagentes entra logo depois de sensores (TD-34).
	checks := collect(project(t, nil), healthyEnv().env())
	for i, c := range checks {
		if c.Name == checkSensors {
			if i+1 >= len(checks) || checks[i+1].Name != checkAgents {
				t.Errorf("checagem depois de sensores não é subagentes: %+v", checks)
			}
			return
		}
	}
	t.Errorf("checagem sensores ausente: %+v", checks)
}

// FuzzAgentFrontMatter: entrada arbitrária nunca entra em pânico; aceita, o
// name não é vazio nem tem quebra de linha, e nenhuma ferramenta é vazia.
func FuzzAgentFrontMatter(f *testing.F) {
	for _, s := range []string{
		"",
		"---",
		"---\n---\n",
		agentDoc("name: a\ntools: Read, Edit"),
		agentDoc("name: a\ntools:\n  - Read\n  - Bash"),
		agentDoc("name: a\ntools: [Read, Edit]"),
		agentDoc("name: \"a\"\ntools: '"),
		agentDoc("name: a\ntools: [\nmodel: opus"),
		"---\r\nname: a\r\ntools: Read\r\n---\r\n",
		"---\nname: a\ntools:\n  -\n  - \n---\n",
		"---\nname:0\r0\n---",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := parseAgentFrontMatter(data)
		if err != nil {
			return
		}
		if m.Name == "" || strings.ContainsAny(m.Name, "\r\n") {
			t.Errorf("name inválido aceito: %q", m.Name)
		}
		for _, tl := range m.Tools {
			if strings.TrimSpace(tl) == "" {
				t.Errorf("ferramenta vazia em %q", m.Tools)
			}
		}
		if len(m.Tools) > 0 && !m.HasTools {
			t.Errorf("ferramentas sem HasTools: %+v", m)
		}
	})
}
