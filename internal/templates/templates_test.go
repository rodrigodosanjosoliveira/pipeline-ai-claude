package templates

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"go/format"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/delivery"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/report"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/validate"
)

// Projeto fictício usado nos testes (regra 4 do CLAUDE.md).
func sampleData() Data {
	return Data{
		Name:           "biblioteca-bairro",
		Module:         "example.com/biblioteca-bairro",
		Summary:        "API de empréstimos da biblioteca do bairro.",
		Provider:       "github",
		ToolkitVersion: "v0.3.0",
		Modules:        allModules(),
	}
}

func allModules() []string { return slices.Sorted(maps.Keys(config.Modules)) }

// testCoreAgents são os subagentes do núcleo (TD-26).
var testCoreAgents = []string{
	"pm-triage", "architecture-guardrails", "test-writer", "dev-implementation",
	"code-reviewer", "security-review", "supply-chain-review", "qa-validation",
	"docs-update", "git-delivery",
}

// reviewers não podem ter ferramenta de escrita (TD-26) nem Bash (TD-29).
var reviewers = []string{
	"code-reviewer", "security-review", "supply-chain-review", "architecture-guardrails",
	"migration-reviewer", "agent-auditor", "tenant-isolation", "qa-validation", "release-ops",
}

// shellAgents são os subagentes fora de editor_agents que mantêm Bash: o
// git-delivery faz commit e push (TD-29).
var shellAgents = []string{"git-delivery"}

var writeTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit"}

const (
	mcpDeliveryNew = "mcp__pipeline-claude__pipeline_delivery_new"
	mcpDiff        = "mcp__pipeline-claude__pipeline_diff"
	mcpSensors     = "mcp__pipeline-claude__pipeline_sensors"
	mcpValidate    = "mcp__pipeline-claude__pipeline_validate"
)

// agentTools é a tabela explícita das ferramentas de cada subagente e de quem
// escreve. Mudou um subagente, mude aqui: a tabela é a revisão da mudança.
var agentTools = map[string]struct {
	tools string
	write bool
}{
	// Cria a delivery pela ferramenta MCP (docs-ai/ não é protegido); sem Bash.
	"pm-triage":               {"Read, Grep, Glob, " + mcpDeliveryNew, false},
	"architecture-guardrails": {"Read, Grep, Glob", false},
	"test-writer":             {"Read, Grep, Glob, Edit, Write, Bash", true},
	"dev-implementation":      {"Read, Grep, Glob, Edit, Write, MultiEdit, Bash, " + mcpSensors, true},
	// Revisores: diff e sensores pelo MCP, sem Bash (TD-29).
	"code-reviewer":       {"Read, Grep, Glob, " + mcpDiff, false},
	"security-review":     {"Read, Grep, Glob, " + mcpDiff + ", " + mcpSensors, false},
	"supply-chain-review": {"Read, Grep, Glob, " + mcpDiff + ", " + mcpSensors, false},
	"qa-validation":       {"Read, Grep, Glob, " + mcpDiff + ", " + mcpSensors, false},
	// Edita docs-ai/ e o report; valida pela ferramenta MCP, sem Bash.
	"docs-update":  {"Read, Grep, Glob, Edit, Write, " + mcpValidate, true},
	"git-delivery": {"Read, Grep, Glob, Bash, " + mcpValidate, false},
	// Módulos.
	"data-model":         {"Read, Grep, Glob", false},
	"migration-reviewer": {"Read, Grep, Glob, " + mcpDiff, false},
	"query-performance":  {"Read, Grep, Glob, Edit, Write, Bash", true},
	"release-ops":        {"Read, Grep, Glob, " + mcpDiff, false},
	"agent-auditor":      {"Read, Grep, Glob, " + mcpDiff + ", " + mcpSensors, false},
	"context-loader":     {"Read, Grep, Glob, WebFetch, WebSearch", false},
	"tenant-isolation":   {"Read, Grep, Glob, " + mcpDiff, false},
}

// agentSensorsWant são os sensores que cada subagente pede pelo nome (TD-29).
var agentSensorsWant = map[string][]string{
	"agent-auditor":       {"test"},
	"security-review":     {"vuln"},
	"supply-chain-review": {"mod_verify", "vuln"},
}

// builtinTools são as ferramentas nativas do Claude Code aceitas em tools.
var builtinTools = []string{
	"Read", "Grep", "Glob", "Edit", "Write", "MultiEdit", "NotebookEdit",
	"Bash", "WebFetch", "WebSearch",
}

func findFile(t *testing.T, p string) File {
	t.Helper()
	for _, f := range Catalog() {
		if f.Path == p {
			return f
		}
	}
	t.Fatalf("%s não está no catálogo", p)
	return File{}
}

func render(t *testing.T, p string, d Data) string {
	t.Helper()
	out, err := Render(findFile(t, p), d)
	if err != nil {
		t.Fatalf("Render(%s): %v", p, err)
	}
	return string(out)
}

// renderProject grava num diretório o que o init gravaria: os arquivos do
// projeto e os gerenciados dos módulos de d.
func renderProject(t *testing.T, dir string, d Data) {
	t.Helper()
	files := append(ExpectedFor(Project, d.Modules, d.Provider), Expected(Managed, d.Modules)...)
	if len(files) == 0 {
		t.Fatal("Expected não devolveu nenhum arquivo")
	}
	for _, f := range files {
		out, err := Render(f, d)
		if err != nil {
			t.Fatalf("Render(%s): %v", f.Path, err)
		}
		rel, err := DestPath(f, d)
		if err != nil {
			t.Fatalf("DestPath(%s): %v", f.Path, err)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, out, f.Mode); err != nil {
			t.Fatal(err)
		}
	}
}

func agentName(p string) (string, bool) {
	if !strings.HasPrefix(p, ".claude/agents/") || !strings.HasSuffix(p, ".md") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(p, ".claude/agents/"), ".md"), true
}

// frontMatter lê o front matter YAML simples (chave: valor numa linha).
func frontMatter(t *testing.T, name, content string) map[string]string {
	t.Helper()
	if !strings.HasPrefix(content, "---\n") {
		t.Fatalf("%s: não começa com front matter", name)
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		t.Fatalf("%s: front matter sem fechamento", name)
	}
	m := map[string]string{}
	for _, l := range strings.Split(content[4:4+end], "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.HasPrefix(l, " ") {
			t.Fatalf("%s: linha de front matter fora do formato chave: valor: %q", name, l)
		}
		if _, dup := m[k]; dup {
			t.Fatalf("%s: chave %q repetida", name, k)
		}
		m[k] = strings.TrimSpace(v)
	}
	return m
}

func splitTools(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(x))
	}
	return out
}

// MCPToolNames devolve os nomes das ferramentas do servidor MCP do toolkit
// (tools/list). É preenchida por mcp_test.go, no pacote externo templates_test:
// importar internal/mcp aqui fecharia o ciclo templates → mcp → doctor →
// syncer → templates.
var MCPToolNames func() ([]string, error)

// mcpToolNames devolve as ferramentas que o servidor MCP do toolkit expõe, com
// o prefixo que o Claude Code usa em tools.
func mcpToolNames(t *testing.T) []string {
	t.Helper()
	if MCPToolNames == nil {
		t.Fatal("MCPToolNames não preenchida (mcp_test.go)")
	}
	tools, err := MCPToolNames()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, "mcp__pipeline-claude__"+tl)
	}
	if len(names) == 0 {
		t.Fatal("servidor MCP sem ferramentas")
	}
	return names
}

func knownTools(t *testing.T) []string {
	return append(slices.Clone(builtinTools), mcpToolNames(t)...)
}

func TestCatalogRendersAll(t *testing.T) {
	d := sampleData()
	if len(Catalog()) == 0 {
		t.Fatal("catálogo vazio")
	}
	for _, f := range Catalog() {
		out, err := Render(f, d)
		if err != nil {
			t.Errorf("Render(%s): %v", f.Path, err)
			continue
		}
		if len(bytes.TrimSpace(out)) == 0 {
			t.Errorf("%s: saída vazia", f.Path)
		}
		if bytes.Contains(out, []byte("[[")) || bytes.Contains(out, []byte("]]")) {
			t.Errorf("%s: saída com delimitador de template", f.Path)
		}
		if !bytes.HasSuffix(out, []byte("\n")) || bytes.Contains(out, []byte("\r")) {
			t.Errorf("%s: saída precisa terminar em LF e não ter CR", f.Path)
		}
		dest, err := DestPath(f, d)
		if err != nil {
			t.Errorf("DestPath(%s): %v", f.Path, err)
			continue
		}
		if strings.Contains(dest, "[[") || dest != path.Clean(dest) || path.IsAbs(dest) || strings.HasPrefix(dest, "..") {
			t.Errorf("DestPath(%s) = %q: caminho inválido", f.Path, dest)
		}
	}
}

func TestStarterCodePathUsesName(t *testing.T) {
	d := sampleData()
	var got []string
	for _, f := range Expected(Project, nil) {
		dest, err := DestPath(f, d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(dest, "cmd/") {
			got = append(got, dest)
		}
	}
	want := []string{"cmd/biblioteca-bairro/main.go", "cmd/biblioteca-bairro/main_test.go"}
	if !slices.Equal(got, want) {
		t.Errorf("código inicial em %v, esperado %v", got, want)
	}
}

func TestRenderIncompleteOrInvalidData(t *testing.T) {
	full := sampleData()
	with := func(mod func(*Data)) Data {
		d := sampleData()
		mod(&d)
		return d
	}
	tests := []struct {
		name string
		file string
		d    Data
		dest bool // falha esperada em DestPath, não em Render
	}{
		{name: "Data vazio no CLAUDE.md", file: "CLAUDE.md", d: Data{}},
		{name: "sem nome no CLAUDE.md", file: "CLAUDE.md", d: with(func(d *Data) { d.Name = "" })},
		{name: "sem resumo no CLAUDE.md", file: "CLAUDE.md", d: with(func(d *Data) { d.Summary = "" })},
		{name: "sem módulo no go.mod", file: "go.mod", d: with(func(d *Data) { d.Module = "" })},
		{name: "sem versão no Makefile", file: "Makefile", d: with(func(d *Data) { d.ToolkitVersion = "" })},
		{name: "sem provedor na config", file: ".pipeline-claude.json", d: with(func(d *Data) { d.Provider = "" })},
		{name: "sem nome no caminho do main.go", file: "cmd/[[.Name]]/main.go", d: with(func(d *Data) { d.Name = "" }), dest: true},
		{name: "nome com ..", file: "cmd/[[.Name]]/main.go", d: with(func(d *Data) { d.Name = "../x" }), dest: true},
		{name: "nome com barra", file: "cmd/[[.Name]]/main.go", d: with(func(d *Data) { d.Name = "a/b" }), dest: true},
		{name: "nome em maiúscula", file: "CLAUDE.md", d: with(func(d *Data) { d.Name = "A" })},
		{name: "provedor desconhecido", file: ".pipeline-claude.json", d: with(func(d *Data) { d.Provider = "bitbucket" })},
		{name: "resumo com quebra de linha", file: "CLAUDE.md", d: with(func(d *Data) { d.Summary = "a\n# injetado" })},
		{name: "versão com espaço", file: "Makefile", d: with(func(d *Data) { d.ToolkitVersion = "v1.0.0 x" })},
		{name: "versão sem v", file: "Makefile", d: with(func(d *Data) { d.ToolkitVersion = "1.0.0" })},
		{name: "módulo Go com espaço", file: "go.mod", d: with(func(d *Data) { d.Module = "example.com/a b" })},
		{name: "módulo Go com quebra de linha", file: "go.mod", d: with(func(d *Data) { d.Module = "a\nrequire x v1" })},
		{name: "módulo opcional desconhecido", file: ".pipeline-claude.json", d: with(func(d *Data) { d.Modules = []string{"inexistente"} })},
		{name: "módulo opcional repetido", file: ".pipeline-claude.json", d: with(func(d *Data) { d.Modules = []string{"data-model", "data-model"} })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := findFile(t, tt.file)
			if tt.dest {
				if _, err := DestPath(f, tt.d); err == nil {
					t.Errorf("DestPath(%s) aceitou Data inválido", tt.file)
				}
				return
			}
			if _, err := Render(f, tt.d); err == nil {
				t.Errorf("Render(%s) aceitou Data inválido", tt.file)
			}
		})
	}
	if _, err := Render(findFile(t, "CLAUDE.md"), full); err != nil {
		t.Fatalf("controle: Render com Data completo falhou: %v", err)
	}
}

// TestManagedIndependentOfProject: arquivo gerenciado só depende de
// enabled_modules (TD-22): o sync o re-renderiza só com a config.
func TestManagedIndependentOfProject(t *testing.T) {
	d1 := sampleData()
	d2 := Data{
		Name: "horta-comunitaria", Module: "example.org/horta", Summary: "Agenda de canteiros da horta.",
		Provider: "gitlab", ToolkitVersion: "v9.8.7-rc.1", Modules: d1.Modules,
	}
	onlyModules := Data{Modules: d1.Modules}
	n := 0
	for _, f := range Catalog() {
		if f.Class != Managed {
			continue
		}
		n++
		if strings.Contains(f.Path, "[[") {
			t.Errorf("%s: caminho de gerenciado não pode depender do projeto", f.Path)
		}
		a, err1 := Render(f, d1)
		b, err2 := Render(f, d2)
		c, err3 := Render(f, onlyModules)
		if err := errorsJoin(err1, err2, err3); err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		if !bytes.Equal(a, b) || !bytes.Equal(a, c) {
			t.Errorf("%s: gerenciado muda com nome, módulo, resumo, provedor ou versão", f.Path)
		}
	}
	if n == 0 {
		t.Fatal("nenhum arquivo gerenciado no catálogo")
	}
}

func errorsJoin(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func TestEmbeddedMatchesCatalog(t *testing.T) {
	cat := Catalog()
	srcs := map[string]string{}
	seenPath := map[string]bool{}
	for _, f := range cat {
		s := source(f)
		if s == "" {
			t.Errorf("%s: sem arquivo de origem", f.Path)
			continue
		}
		if other, dup := srcs[s]; dup {
			t.Errorf("%s e %s usam a mesma origem %s", f.Path, other, s)
		}
		srcs[s] = f.Path
		key := strings.ToLower(f.Path)
		if seenPath[key] {
			t.Errorf("%s: destino repetido (sem diferenciar caixa)", f.Path)
		}
		seenPath[key] = true
	}
	if embedded == nil {
		t.Fatal("nenhum arquivo embutido")
	}
	var onDisk []string
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		onDisk = append(onDisk, p)
		if _, ok := srcs[p]; !ok {
			t.Errorf("%s embutido e fora do catálogo (órfão)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for s, p := range srcs {
		if !slices.Contains(onDisk, s) {
			t.Errorf("%s: origem %s não está embutida", p, s)
		}
	}
}

func TestCatalogEntries(t *testing.T) {
	for _, f := range Catalog() {
		switch f.Class {
		case Managed:
			if !manifest.ManagedArea(f.Path) {
				t.Errorf("%s: gerenciado fora da área gerenciada (TD-23)", f.Path)
			}
		case Project:
			if manifest.ManagedArea(f.Path) {
				t.Errorf("%s: do projeto dentro da área gerenciada", f.Path)
			}
			if f.Module != "" {
				t.Errorf("%s: arquivo do projeto condicionado a módulo", f.Path)
			}
		default:
			t.Errorf("%s: classe inválida %d", f.Path, f.Class)
		}
		if f.Provider != "" && (f.Class != Project || (f.Provider != "github" && f.Provider != "gitlab")) {
			t.Errorf("%s: provedor %q em arquivo %v (só do projeto, github ou gitlab)", f.Path, f.Provider, f.Class)
		}
		if f.Module != "" && !config.Modules[f.Module] {
			t.Errorf("%s: módulo %q fora de config.Modules", f.Path, f.Module)
		}
		wantMode := fs.FileMode(0o644)
		if f.Path == ".githooks/pre-push" {
			wantMode = 0o755
		}
		if f.Mode != wantMode {
			t.Errorf("%s: modo %v, esperado %v", f.Path, f.Mode, wantMode)
		}
		if f.Path != path.Clean(f.Path) || path.IsAbs(f.Path) || strings.Contains(f.Path, `\`) {
			t.Errorf("%s: caminho não está limpo", f.Path)
		}
	}
	if !slices.IsSortedFunc(Catalog(), func(a, b File) int { return strings.Compare(a.Path, b.Path) }) {
		t.Error("catálogo fora de ordem")
	}
}

func TestCatalogIsCopy(t *testing.T) {
	c := Catalog()
	if len(c) == 0 {
		t.Fatal("catálogo vazio")
	}
	c[0].Path = "adulterado"
	if Catalog()[0].Path == "adulterado" {
		t.Error("Catalog devolve a fatia interna")
	}
}

func TestProjectFiles(t *testing.T) {
	want := []string{
		".claude/settings.json", ".gitignore", ".mcp.json", ".pipeline-claude.json",
		"CLAUDE.md", "Makefile",
		"cmd/[[.Name]]/main.go", "cmd/[[.Name]]/main_test.go",
		"docs-ai/01-INVARIANTES-GLOBAIS.md", "docs-ai/03-MAPA-RAPIDO-MODULOS.md",
		"docs-ai/DECISOES.md", "docs-ai/agents/08-HANDOFF-CONTRACT.md",
		"docs-ai/deliveries/INDEX.md", "docs-ai/deliveries/_template/report.md",
		"go.mod",
	}
	var got []string
	for _, f := range Expected(Project, allModules()) {
		got = append(got, f.Path)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("arquivos do projeto:\n got %v\nwant %v", got, want)
	}
}

func TestExpected(t *testing.T) {
	core := Expected(Managed, nil)
	var corePaths []string
	for _, f := range core {
		if f.Module != "" {
			t.Errorf("%s: módulo desligado no esperado", f.Path)
		}
		corePaths = append(corePaths, f.Path)
	}
	for _, a := range testCoreAgents {
		if !slices.Contains(corePaths, ".claude/agents/"+a+".md") {
			t.Errorf("subagente do núcleo %s fora do esperado", a)
		}
	}
	for _, s := range []string{"nova-delivery", "fluxo", "validar"} {
		if !slices.Contains(corePaths, ".claude/skills/"+s+"/SKILL.md") {
			t.Errorf("skill %s fora do esperado", s)
		}
	}
	if !slices.Contains(corePaths, ".githooks/pre-push") {
		t.Error(".githooks/pre-push fora do esperado")
	}
	if len(core) != len(testCoreAgents)+3+1 {
		t.Errorf("núcleo com %d arquivos, esperado %d", len(core), len(testCoreAgents)+4)
	}

	all := Expected(Managed, allModules())
	if len(all) != len(core)+len(config.Modules) {
		t.Errorf("com todos os módulos: %d arquivos, esperado %d", len(all), len(core)+len(config.Modules))
	}
	reversed := slices.Clone(allModules())
	slices.Reverse(reversed)
	if !slices.Equal(all, Expected(Managed, reversed)) {
		t.Error("Expected depende da ordem dos módulos")
	}
	one := Expected(Managed, []string{"tenant-isolation"})
	if len(one) != len(core)+1 || !slices.ContainsFunc(one, func(f File) bool { return f.Path == ".claude/agents/tenant-isolation.md" }) {
		t.Errorf("com tenant-isolation: %v", one)
	}
	for _, f := range Expected(Project, allModules()) {
		if f.Class != Project {
			t.Errorf("%s: classe errada em Expected(Project)", f.Path)
		}
	}
	if !slices.IsSortedFunc(all, func(a, b File) int { return strings.Compare(a.Path, b.Path) }) {
		t.Error("Expected fora de ordem")
	}
}

// TestModuleSubagents: config.Modules e os subagentes de módulo andam juntos.
func TestModuleSubagents(t *testing.T) {
	byModule := map[string]File{}
	for _, f := range Catalog() {
		if f.Module == "" {
			continue
		}
		if f.Class != Managed {
			t.Errorf("%s: condicionado a módulo e não gerenciado", f.Path)
		}
		if f.Path != ".claude/agents/"+f.Module+".md" {
			t.Errorf("%s: subagente de módulo %q fora de .claude/agents/<módulo>.md", f.Path, f.Module)
		}
		if !config.Modules[f.Module] {
			t.Errorf("%s: módulo %q não está em config.Modules", f.Path, f.Module)
		}
		byModule[f.Module] = f
	}
	for m := range config.Modules {
		if _, ok := byModule[m]; !ok {
			t.Errorf("config.Modules tem %q sem subagente no catálogo", m)
		}
	}
	for _, a := range testCoreAgents {
		f := findFile(t, ".claude/agents/"+a+".md")
		if f.Module != "" || f.Class != Managed {
			t.Errorf("%s: subagente do núcleo precisa ser gerenciado e sempre gerado", f.Path)
		}
	}
}

func TestSubagentFrontMatter(t *testing.T) {
	known := knownTools(t)
	d := sampleData()
	seen := map[string]bool{}
	for _, f := range Catalog() {
		name, ok := agentName(f.Path)
		if !ok {
			continue
		}
		seen[name] = true
		fm := frontMatter(t, f.Path, render(t, f.Path, d))
		if fm["name"] != name {
			t.Errorf("%s: name %q diferente do arquivo", f.Path, fm["name"])
		}
		if desc := fm["description"]; desc == "" || strings.HasPrefix(desc, ">") || strings.HasPrefix(desc, "|") {
			t.Errorf("%s: description vazia ou em bloco (%q)", f.Path, desc)
		}
		for k := range fm {
			if !slices.Contains([]string{"name", "description", "tools"}, k) {
				t.Errorf("%s: chave de front matter inesperada %q", f.Path, k)
			}
		}
		raw, ok := fm["tools"]
		if !ok || raw == "" {
			t.Errorf("%s: tools ausente (omitido herda todas as ferramentas)", f.Path)
			continue
		}
		tools := splitTools(raw)
		for _, tl := range tools {
			if !slices.Contains(known, tl) {
				t.Errorf("%s: ferramenta desconhecida %q", f.Path, tl)
			}
		}
		want, ok := agentTools[name]
		if !ok {
			t.Errorf("%s: subagente fora da tabela agentTools", f.Path)
			continue
		}
		if raw != want.tools {
			t.Errorf("%s: tools %q, tabela diz %q", f.Path, raw, want.tools)
		}
		hasWrite := slices.ContainsFunc(tools, func(tl string) bool { return slices.Contains(writeTools, tl) })
		if hasWrite != want.write {
			t.Errorf("%s: escrita = %v, tabela diz %v", f.Path, hasWrite, want.write)
		}
	}
	for name := range agentTools {
		if !seen[name] {
			t.Errorf("tabela agentTools tem %q sem subagente no catálogo", name)
		}
	}
	for _, r := range reviewers {
		if agentTools[r].write {
			t.Errorf("revisor %s com escrita na tabela (TD-26)", r)
		}
	}
}

func TestSkillsFrontMatter(t *testing.T) {
	known := knownTools(t)
	d := sampleData()
	re := regexp.MustCompile(`^\.claude/skills/([a-z][a-z0-9-]*)/SKILL\.md$`)
	n := 0
	for _, f := range Catalog() {
		if !strings.HasPrefix(f.Path, ".claude/skills/") {
			continue
		}
		n++
		m := re.FindStringSubmatch(f.Path)
		if m == nil {
			t.Errorf("%s: skill fora de .claude/skills/<nome>/SKILL.md", f.Path)
			continue
		}
		fm := frontMatter(t, f.Path, render(t, f.Path, d))
		if fm["name"] != m[1] {
			t.Errorf("%s: name %q diferente do diretório", f.Path, fm["name"])
		}
		if fm["description"] == "" {
			t.Errorf("%s: description vazia", f.Path)
		}
		for k := range fm {
			if !slices.Contains([]string{"name", "description", "argument-hint", "allowed-tools"}, k) {
				t.Errorf("%s: chave de front matter inesperada %q", f.Path, k)
			}
		}
		if raw, ok := fm["allowed-tools"]; ok {
			for _, tl := range splitTools(raw) {
				if !slices.Contains(known, tl) {
					t.Errorf("%s: ferramenta desconhecida %q", f.Path, tl)
				}
				if slices.Contains(writeTools, tl) || tl == "Bash" {
					t.Errorf("%s: skill pré-aprova %s; a sessão principal não escreve", f.Path, tl)
				}
			}
		}
	}
	if n != 3 {
		t.Errorf("%d skills, esperado 3", n)
	}
}

// TestMCPToolsReferenced: toda ferramenta MCP citada em qualquer template
// existe no servidor.
func TestMCPToolsReferenced(t *testing.T) {
	known := mcpToolNames(t)
	re := regexp.MustCompile(`mcp__pipeline-claude__[a-z_]+`)
	d := sampleData()
	for _, f := range Catalog() {
		for _, m := range re.FindAllString(render(t, f.Path, d), -1) {
			if !slices.Contains(known, m) {
				t.Errorf("%s cita %s, que o servidor MCP não expõe", f.Path, m)
			}
		}
	}
}

func loadGenerated(t *testing.T, d Data) (*config.Config, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	out := render(t, ".pipeline-claude.json", d)
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load: %v\n%s", err, out)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	return cfg, raw
}

var noAgentPaths = []string{".claude/", ".mcp.json", ".pipeline-claude.json", ".pipeline-claude.lock"}

func TestGeneratedConfigLoads(t *testing.T) {
	combos := [][]string{nil, {}, allModules(), {"query-performance"}, {"migration-reviewer", "tenant-isolation"}}
	for _, provider := range []string{"github", "gitlab"} {
		for _, mods := range combos {
			d := sampleData()
			d.Provider = provider
			d.Modules = mods
			t.Run(provider+"/"+strings.Join(mods, ","), func(t *testing.T) {
				cfg, raw := loadGenerated(t, d)
				if _, ok := raw["report_schema"]; ok {
					t.Error("config gerada declara report_schema (TD-26: vale o padrão)")
				}
				if cfg.GitProvider != provider || cfg.ProjectType != "api" || cfg.Database != "postgres" {
					t.Errorf("provedor/tipo/banco: %q %q %q", cfg.GitProvider, cfg.ProjectType, cfg.Database)
				}
				wantMods := mods
				if wantMods == nil {
					wantMods = []string{}
				}
				if got := cfg.EnabledModules; !slices.Equal(got, wantMods) || got == nil {
					t.Errorf("enabled_modules = %#v, esperado %#v (lista, nunca null)", got, wantMods)
				}
				if !slices.Equal(cfg.NoAgentPaths, noAgentPaths) {
					t.Errorf("no_agent_paths = %v", cfg.NoAgentPaths)
				}
				for _, p := range append([]string{"cmd/", "internal/", "migrations/", "go.mod", "go.sum", "Makefile"}, noAgentPaths...) {
					if !slices.Contains(cfg.ProtectedPaths, p) {
						t.Errorf("protected_paths sem %s", p)
					}
				}
				if cfg.IsProtected("docs-ai/deliveries/INDEX.md") || cfg.IsProtected("CLAUDE.md") {
					t.Error("docs-ai/ e CLAUDE.md não podem ser protegidos (o fluxo de delivery grava lá)")
				}
				if !slices.Equal(cfg.EditorAgents, []string{"dev-implementation", "test-writer", "query-performance", "docs-update"}) {
					t.Errorf("editor_agents = %v", cfg.EditorAgents)
				}
				if cfg.DeliveryIDPattern != "^DEL-[0-9]{3,}$" || !slices.Equal(cfg.DeliveryPrefixes, []string{"DEL"}) {
					t.Errorf("padrão/prefixos: %q %v", cfg.DeliveryIDPattern, cfg.DeliveryPrefixes)
				}
				if got := cfg.FlagsTriggeredBy("go.mod"); !slices.Equal(got, []string{"require_security"}) {
					t.Errorf("auto_flags para go.mod: %v", got)
				}
				if got := cfg.FlagsTriggeredBy("go.sum"); !slices.Equal(got, []string{"require_security"}) {
					t.Errorf("auto_flags para go.sum: %v", got)
				}
				var sensors [][]string
				for _, s := range cfg.Sensors {
					sensors = append(sensors, s.Cmd)
				}
				wantSensors := [][]string{
					{"go", "vet", "./..."}, {"go", "test", "./..."}, {"go", "build", "./..."},
					{"go", "run", "golang.org/x/vuln/cmd/govulncheck@" + toolkitGovulncheck(t), "./..."},
					{"go", "mod", "verify"},
				}
				if !slices.EqualFunc(sensors, wantSensors, slices.Equal[[]string]) {
					t.Errorf("sensors = %v", sensors)
				}
				if len(cfg.SecuritySurfacePaths) == 0 {
					t.Error("security_surface_paths vazio")
				}
				// Arquivos apontados pela config existem no catálogo.
				for _, p := range append([]string{cfg.IndexFile, cfg.ReportTemplate}, cfg.InvariantsFiles...) {
					findFile(t, p)
				}
				if len(cfg.InvariantsFiles) == 0 || cfg.ReportTemplate == "" {
					t.Error("invariants_files e report_template precisam estar configurados")
				}
			})
		}
	}
}

func TestEditorAgentsCanWrite(t *testing.T) {
	cfg, _ := loadGenerated(t, sampleData())
	d := sampleData()
	for _, a := range cfg.EditorAgents {
		p := ".claude/agents/" + a + ".md"
		fm := frontMatter(t, p, render(t, p, d))
		tools := splitTools(fm["tools"])
		if !slices.Contains(tools, "Edit") && !slices.Contains(tools, "Write") {
			t.Errorf("editor %s sem Edit nem Write", a)
		}
	}
	for _, r := range reviewers {
		if cfg.IsEditorAgent(r) {
			t.Errorf("revisor %s em editor_agents", r)
		}
	}
}

func TestSettingsJSON(t *testing.T) {
	out := render(t, ".claude/settings.json", sampleData())
	var s struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("settings.json: %v\n%s", err, out)
	}
	// deny ↔ no_agent_paths: as duas camadas cobrem os mesmos caminhos (TD-26).
	var want []string
	for _, p := range noAgentPaths {
		if strings.HasSuffix(p, "/") {
			want = append(want, "Edit(/"+p+"**)")
		} else {
			want = append(want, "Edit(/"+p+")")
		}
	}
	if !slices.Equal(s.Permissions.Deny, want) {
		t.Errorf("permissions.deny = %v, esperado %v", s.Permissions.Deny, want)
	}
	cmds := map[string]string{
		"PreToolUse":  `"${CLAUDE_PROJECT_DIR}"/bin/pipeline-claude hook`,
		"PostToolUse": `"${CLAUDE_PROJECT_DIR}"/bin/pipeline-claude fmt-hook`,
	}
	matchers := map[string]string{"PreToolUse": "Edit|Write|MultiEdit|NotebookEdit", "PostToolUse": "Edit|Write|MultiEdit"}
	if len(s.Hooks) != 2 {
		t.Errorf("hooks com %d eventos, esperado 2", len(s.Hooks))
	}
	for ev, cmd := range cmds {
		en := s.Hooks[ev]
		if len(en) != 1 || en[0].Matcher != matchers[ev] || len(en[0].Hooks) != 1 ||
			en[0].Hooks[0].Type != "command" || en[0].Hooks[0].Command != cmd {
			t.Errorf("%s: registro inesperado %+v", ev, en)
		}
	}
	cfg, _ := loadGenerated(t, sampleData())
	for _, p := range []string{".claude/agents/x.md", ".mcp.json", ".pipeline-claude.json", ".pipeline-claude.lock"} {
		if !cfg.IsNoAgent(p) {
			t.Errorf("%s negado no settings.json e fora de no_agent_paths", p)
		}
	}
}

func TestMCPJSON(t *testing.T) {
	out := render(t, ".mcp.json", sampleData())
	var m struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf(".mcp.json: %v", err)
	}
	s, ok := m.MCPServers["pipeline-claude"]
	if len(m.MCPServers) != 1 || !ok {
		t.Fatalf("mcpServers = %v", m.MCPServers)
	}
	if s.Type != "stdio" || s.Command != "${CLAUDE_PROJECT_DIR:-.}/bin/pipeline-claude" || !slices.Equal(s.Args, []string{"mcp"}) {
		t.Errorf("servidor pipeline-claude = %+v", s)
	}
}

func TestMakefile(t *testing.T) {
	out := render(t, "Makefile", sampleData())
	for _, want := range []string{
		"PIPELINE_CLAUDE_VERSION := v0.3.0\n",
		"\tGOBIN=$(CURDIR)/bin go install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@$(PIPELINE_CLAUDE_VERSION)\n",
		"\nbuild:", "\ntest:", "\nvet:", "\ncheck:", "\ntools:",
		"\tbin/pipeline-claude sensors\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Makefile sem %q", want)
		}
	}
	for i, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "    ") {
			t.Errorf("Makefile linha %d indentada com espaço (receita exige tab): %q", i+1, l)
		}
	}
}

func TestGoMod(t *testing.T) {
	out := render(t, "go.mod", sampleData())
	if out != "module example.com/biblioteca-bairro\n\ngo 1.24\n" {
		t.Errorf("go.mod = %q", out)
	}
}

func TestGitignore(t *testing.T) {
	out := render(t, ".gitignore", sampleData())
	if !slices.Contains(strings.Split(out, "\n"), "bin/") {
		t.Error(".gitignore sem bin/")
	}
}

var idLike = regexp.MustCompile(`\b[A-Z]{2,}-[0-9]+\b`)

func TestIndexTemplate(t *testing.T) {
	out := render(t, "docs-ai/deliveries/INDEX.md", sampleData())
	if !strings.Contains(out, "| ID | Título | Status | Data |\n|---|---|---|---|\n") {
		t.Errorf("INDEX.md sem a tabela ID | Título | Status | Data:\n%s", out)
	}
	if m := idLike.FindAllString(out, -1); m != nil {
		t.Errorf("INDEX.md com texto que parece ID (conta na sequência do delivery new): %v", m)
	}
}

func TestReportTemplateMarkers(t *testing.T) {
	out := render(t, "docs-ai/deliveries/_template/report.md", sampleData())
	for _, m := range []string{"<delivery_id>", "<titulo>", "<data>"} {
		if !strings.Contains(out, m) {
			t.Errorf("report.md sem o marcador %s", m)
		}
	}
	if m := idLike.FindAllString(out, -1); m != nil {
		t.Errorf("report.md com texto que parece ID: %v", m)
	}
}

type finding struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func runValidate(t *testing.T, root, id string) (int, []finding) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := validate.Run(validate.Options{Target: id, NoGit: true, JSON: true,
		Getwd: func() (string, error) { return root, nil }}, &out, &errOut)
	if code == validate.ExitBadUsage {
		t.Fatalf("validate: uso inválido: %s", errOut.String())
	}
	var rep struct {
		Findings []finding `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("saída do validate: %v: %s", err, out.String())
	}
	return code, rep.Findings
}

// TestDeliveryAndValidate: o projeto gerado conversa com delivery e validate.
func TestDeliveryAndValidate(t *testing.T) {
	for _, mods := range [][]string{nil, allModules()} {
		d := sampleData()
		d.Modules = mods
		root := t.TempDir()
		renderProject(t, root, d)
		cfg, err := config.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
		created, err := delivery.New(cfg, delivery.NewOptions{Title: "Cadastro de leitores"}, now)
		if err != nil {
			t.Fatalf("delivery.New: %v", err)
		}
		if created.ID != "DEL-001" {
			t.Fatalf("primeira delivery = %s, esperado DEL-001", created.ID)
		}
		reportPath := filepath.Join(root, filepath.FromSlash(created.ReportPath))
		content, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{"<delivery_id>", "<titulo>", "<data>"} {
			if bytes.Contains(content, []byte(m)) {
				t.Errorf("report criado ainda tem o marcador %s", m)
			}
		}
		if !bytes.Contains(content, []byte("2026-03-14")) || !bytes.Contains(content, []byte("Cadastro de leitores")) {
			t.Errorf("report criado sem data ou título:\n%s", content)
		}

		code, findings := runValidate(t, root, "DEL-001")
		if code != validate.ExitFail {
			t.Errorf("report sem preencher: exit %d, esperado %d", code, validate.ExitFail)
		}
		var errs int
		for _, f := range findings {
			if f.Level != report.LevelError {
				continue
			}
			errs++
			if !strings.Contains(f.Message, "placeholder") && !strings.Contains(f.Message, "checklist") {
				t.Errorf("report sem preencher reprovou por motivo diferente de placeholder/checklist: %s", f.Message)
			}
		}
		if errs == 0 {
			t.Errorf("report sem preencher sem erro: %v", findings)
		}

		filled := strings.ReplaceAll(string(content), "<preencher>", "preenchido na delivery de teste")
		filled = strings.ReplaceAll(filled, "- [ ]", "- [x]")
		if err := os.WriteFile(reportPath, []byte(filled), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, findings := runValidate(t, root, "DEL-001"); code != validate.ExitOK {
			t.Errorf("report preenchido reprovou (exit %d): %v\n%s", code, findings, filled)
		}

		created2, err := delivery.New(cfg, delivery.NewOptions{Title: "Renovação de empréstimo"}, now)
		if err != nil || created2.ID != "DEL-002" {
			t.Errorf("segunda delivery = %v, %v; esperado DEL-002", created2.ID, err)
		}
	}
}

func TestStarterCodeFormatted(t *testing.T) {
	d := sampleData()
	for _, p := range []string{"cmd/[[.Name]]/main.go", "cmd/[[.Name]]/main_test.go"} {
		out := render(t, p, d)
		got, err := format.Source([]byte(out))
		if err != nil {
			t.Errorf("%s: não compila no parser: %v", p, err)
			continue
		}
		if string(got) != out {
			t.Errorf("%s: não está no formato do gofmt", p)
		}
	}
}

// goEnv isola o go do teste: sem rede, sem baixar toolchain, sem workspace.
func goEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GOFLAGS", "GOTOOLCHAIN", "GOPROXY", "GOWORK", "GO111MODULE":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOFLAGS=-mod=mod")
}

func TestStarterCodeVetAndTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go não está no PATH")
	}
	root := t.TempDir()
	renderProject(t, root, sampleData())
	for _, args := range [][]string{{"vet", "./..."}, {"test", "-count=1", "./..."}, {"build", "./..."}} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = root
		cmd.Env = goEnv()
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Errorf("go %s no projeto gerado: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestPrePushBlocksMain(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh não está no PATH")
	}
	p := filepath.Join(t.TempDir(), "pre-push")
	if err := os.WriteFile(p, []byte(render(t, ".githooks/pre-push", sampleData())), 0o755); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	other := strings.Repeat("b", 40)
	tests := []struct {
		name  string
		stdin string
		want  int
	}{
		{"push na main", "refs/heads/main " + sha + " refs/heads/main " + other + "\n", 1},
		{"branch de trabalho para main", "refs/heads/task/x " + sha + " refs/heads/main " + other + "\n", 1},
		// A main ainda não existe no remoto (repositório vazio): o primeiro push
		// a cria, sem PR possível. Antes do TASK-046 isto exigia --no-verify.
		{"criar a main no remoto vazio", "refs/heads/main " + sha + " refs/heads/main " + zero + "\n", 0},
		{"criar a main a partir de outra branch", "refs/heads/task/x " + sha + " refs/heads/main " + zero + "\n", 1},
		{"criar a main junto com outra branch", "refs/heads/task/x " + sha + " refs/heads/task/x " + zero + "\nrefs/heads/main " + sha + " refs/heads/main " + zero + "\n", 0},
		{"criar a main e empurrar outra para ela", "refs/heads/main " + sha + " refs/heads/main " + zero + "\nrefs/heads/task/x " + sha + " refs/heads/main " + other + "\n", 1},
		{"push de branch de trabalho", "refs/heads/task/x " + sha + " refs/heads/task/x " + zero + "\n", 0},
		{"apagar branch remota", "(delete) " + zero + " refs/heads/task/x " + sha + "\n", 0},
		{"apagar a main remota", "(delete) " + zero + " refs/heads/main " + sha + "\n", 0},
		{"nada a enviar", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(sh, p, "origin", "https://example.com/repo.git")
			cmd.Stdin = strings.NewReader(tt.stdin)
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tt.want {
				t.Errorf("exit %d, esperado %d\n%s", code, tt.want, out)
			}
		})
	}
}

// antigravityTerms são resíduos do pipeline-ai/Antigravity que não podem ir
// para o projeto gerado (regra 3 do prompt da Fase 3).
var antigravityTerms = []string{
	"antigravity", ".agents/", "gemini", "go tool pipeline", "pipeline-validate",
	"pipeline-hook", "pipeline-ai.json", ".pipeline-ai", "wails", "desktop",
	"mongodb", "dynamodb", "write_to_file", "replace_file_content", "agents.md",
	"taskfile", "zustand", "tailwind", "cgo",
}

func eachSource(t *testing.T, fn func(name, content string)) {
	t.Helper()
	if embedded == nil {
		t.Fatal("nenhum arquivo embutido")
	}
	err := fs.WalkDir(embedded, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(embedded, p)
		if err != nil {
			return err
		}
		fn(p, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoAntigravityTerms(t *testing.T) {
	eachSource(t, func(name, content string) {
		lower := strings.ToLower(content)
		for _, term := range antigravityTerms {
			if strings.Contains(lower, term) {
				t.Errorf("%s contém %q", name, term)
			}
		}
	})
}

// TestNoBlocklistTerms: nenhum termo da lista privada (fora do repositório)
// aparece nos templates. Sem a variável, só registra (como
// TestFixturesVersionadas).
func TestNoBlocklistTerms(t *testing.T) {
	p := os.Getenv("PIPELINE_CLAUDE_FIXTURE_BLOCKLIST")
	if p == "" {
		t.Skip("PIPELINE_CLAUDE_FIXTURE_BLOCKLIST não definido")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("PIPELINE_CLAUDE_FIXTURE_BLOCKLIST definido, mas ilegível: %v", err)
	}
	defer f.Close()
	var terms []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		term := strings.ToLower(strings.TrimSpace(sc.Text()))
		if term != "" && !strings.HasPrefix(term, "#") {
			terms = append(terms, term)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	eachSource(t, func(name, content string) {
		lower := strings.ToLower(content)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				t.Errorf("%s contém termo bloqueado da lista privada", name)
			}
		}
	})
}

// toolkitGovulncheck lê o GOVULNCHECK_VERSION do Makefile do toolkit: o
// sensor vuln do projeto gerado usa a mesma versão.
func toolkitGovulncheck(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^GOVULNCHECK_VERSION := (\S+)$`).FindSubmatch(data)
	if m == nil {
		t.Fatal("Makefile do toolkit sem GOVULNCHECK_VERSION")
	}
	return string(m[1])
}

// TestNonEditorsReadOnly: fora de editor_agents, nenhum subagente tem
// ferramenta de escrita nem Bash, exceto shellAgents (TD-29).
func TestNonEditorsReadOnly(t *testing.T) {
	cfg, _ := loadGenerated(t, sampleData())
	d := sampleData()
	for _, f := range Catalog() {
		name, ok := agentName(f.Path)
		if !ok || cfg.IsEditorAgent(name) {
			continue
		}
		tools := splitTools(frontMatter(t, f.Path, render(t, f.Path, d))["tools"])
		for _, tl := range tools {
			if slices.Contains(writeTools, tl) {
				t.Errorf("%s fora de editor_agents com %s", name, tl)
			}
			if tl == "Bash" && !slices.Contains(shellAgents, name) {
				t.Errorf("%s fora de editor_agents com Bash (TD-29)", name)
			}
		}
	}
	for _, r := range reviewers {
		if slices.Contains(splitTools(agentTools[r].tools), "Bash") {
			t.Errorf("revisor %s com Bash na tabela", r)
		}
	}
}

// TestNoBashInstructions: subagente sem Bash não pode mandar rodar comando de
// shell (git, go, govulncheck, bin/pipeline-claude, make), exceto pedido ao
// usuário, nem citar o Bash.
func TestNoBashInstructions(t *testing.T) {
	d := sampleData()
	cmd := regexp.MustCompile("`(git|go|govulncheck|bin/pipeline-claude|make) [^`]*`|```(bash|sh)")
	for _, f := range Catalog() {
		name, ok := agentName(f.Path)
		if !ok {
			continue
		}
		if slices.Contains(splitTools(agentTools[name].tools), "Bash") {
			continue
		}
		body := string(render(t, f.Path, d))
		for _, line := range strings.Split(body, "\n") {
			// Comando pedido ao usuário (fora da sessão) não é instrução de shell
			// para o subagente.
			if m := cmd.FindString(line); m != "" && !strings.Contains(line, "usuário") {
				t.Errorf("%s não tem Bash e manda rodar %s", name, m)
			}
		}
		if strings.Contains(body, "Bash") {
			t.Errorf("%s não tem Bash e cita Bash", name)
		}
	}
}

// TestRequiredSensors: os sensores que os subagentes pedem pelo nome estão na
// config gerada, no texto do subagente e no RequiredSensors (TD-29).
func TestRequiredSensors(t *testing.T) {
	cfg, _ := loadGenerated(t, sampleData())
	var names []string
	for _, s := range cfg.Sensors {
		names = append(names, s.Name)
	}
	d := sampleData()
	for agent, sensors := range agentSensorsWant {
		p := ".claude/agents/" + agent + ".md"
		body := string(render(t, p, d))
		if !slices.Contains(splitTools(agentTools[agent].tools), mcpSensors) {
			t.Errorf("%s pede sensores sem %s", agent, mcpSensors)
		}
		for _, s := range sensors {
			if !slices.Contains(names, s) {
				t.Errorf("sensor %s de %s fora da config gerada", s, agent)
			}
			if !strings.Contains(body, `"`+s+`"`) {
				t.Errorf("%s não pede o sensor %q pelo nome", agent, s)
			}
		}
	}
	tests := []struct {
		modules []string
		want    []string
	}{
		{nil, []string{"mod_verify", "vuln"}},
		{[]string{"agent-auditor"}, []string{"mod_verify", "test", "vuln"}},
		{allModules(), []string{"mod_verify", "test", "vuln"}},
	}
	for _, tt := range tests {
		if got := RequiredSensors(tt.modules); !slices.Equal(got, tt.want) {
			t.Errorf("RequiredSensors(%v) = %v, quero %v", tt.modules, got, tt.want)
		}
	}
	for _, r := range RequiredSensors(allModules()) {
		if len(SensorUsers(r)) == 0 {
			t.Errorf("SensorUsers(%q) vazio", r)
		}
	}
	if got := SensorUsers("vuln"); !slices.Equal(got, []string{"security-review", "supply-chain-review"}) {
		t.Errorf("SensorUsers(vuln) = %v", got)
	}
	// DefaultSensors é a config gerada, comando por comando.
	def, err := DefaultSensors()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(def, cfg.Sensors, func(a, b config.Sensor) bool { return a.Name == b.Name && slices.Equal(a.Cmd, b.Cmd) }) {
		t.Errorf("DefaultSensors = %v, config gerada = %v", def, cfg.Sensors)
	}
}

// TestReportTemplateCodeReview: o veredito do code-reviewer tem lugar no
// report (atrito da demo: só aparecia no corpo da PR).
func TestReportTemplateCodeReview(t *testing.T) {
	out := render(t, "docs-ai/deliveries/_template/report.md", sampleData())
	sec := regexp.MustCompile(`(?s)\n## Code Review\n(.*?)\n## `).FindStringSubmatch(out)
	if sec == nil {
		t.Fatalf("report.md sem a seção Code Review:\n%s", out)
	}
	for _, want := range []string{"- Resultado: <preencher>", "- Bloqueios e sugestoes: <preencher>"} {
		if !strings.Contains(sec[1], want) {
			t.Errorf("seção Code Review sem %q:\n%s", want, sec[1])
		}
	}
	if dev, cr, qa := strings.Index(out, "\n## Dev\n"), strings.Index(out, "\n## Code Review\n"), strings.Index(out, "\n## QA\n"); !(dev < cr && cr < qa) {
		t.Errorf("Code Review fora da ordem Dev → Code Review → QA")
	}
	agent := render(t, ".claude/agents/code-reviewer.md", sampleData())
	if !strings.Contains(agent, "seção Code Review do report") {
		t.Error("code-reviewer não diz que a saída alimenta a seção Code Review do report")
	}
}

// TestReportTemplateSensorsChecklist: o item dos sensores no checklist não
// lista um subconjunto dos sensores do projeto (atrito da demo: "vet, test,
// build" com cinco sensores declarados).
func TestReportTemplateSensorsChecklist(t *testing.T) {
	d := sampleData()
	out := render(t, "docs-ai/deliveries/_template/report.md", d)
	var cfg struct {
		Sensors []struct {
			Name string `json:"name"`
		} `json:"sensors"`
	}
	if err := json.Unmarshal([]byte(render(t, ".pipeline-claude.json", d)), &cfg); err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "- [ ]") && strings.Contains(l, "pipeline-claude sensors") {
			line = l
		}
	}
	if line == "" {
		t.Fatal("checklist sem o item dos sensores")
	}
	var named, missing []string
	for _, s := range cfg.Sensors {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(s.Name) + `\b`).MatchString(line) {
			named = append(named, s.Name)
		} else {
			missing = append(missing, s.Name)
		}
	}
	if len(named) > 0 && len(missing) > 0 {
		t.Errorf("item %q cita %v e omite %v: cite todos ou nenhum", line, named, missing)
	}
}

// TestDeliveryIndexStatus: o fluxo leva a delivery a "em revisão" ao abrir o
// PR/MR e a "merged" quando o commit dela chega à base (atrito da demo: o
// índice ficava em draft e cada delivery corrigia as anteriores à mão).
func TestDeliveryIndexStatus(t *testing.T) {
	fluxo := render(t, ".claude/skills/fluxo/SKILL.md", sampleData())
	for _, want := range []string{"base_ref", "git fetch", "git log --fixed-strings --grep", "MESCLADAS", "docs-update"} {
		if !strings.Contains(fluxo, want) {
			t.Errorf("fluxo sem %q", want)
		}
	}
	if i, j := strings.Index(fluxo, "MESCLADAS"), strings.Index(fluxo, "Comece pelo `pm-triage`"); i < 0 || j < 0 || i > j {
		t.Error("fluxo confere as deliveries mescladas depois de começar pelo pm-triage")
	}
	docs := render(t, ".claude/agents/docs-update.md", sampleData())
	for _, want := range []string{"`em revisão`", "`merged`", "MESCLADAS", "só a coluna Status"} {
		if !strings.Contains(docs, want) {
			t.Errorf("docs-update sem %q", want)
		}
	}
}
