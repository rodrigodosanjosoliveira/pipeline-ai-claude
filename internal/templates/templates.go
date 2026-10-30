// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/scaffold/scaffold.go @ 0eac73a (MIT).
// Alterações: só o embed e a renderização; catálogo explícito com destino,
// classe gerenciado/projeto e condição por módulo ou provedor (TD-22, TD-15);
// text/template com delimitadores [[ ]] e missingkey=error no lugar de
// substituição de texto (TD-22); campo vazio não vira chave, então template que usa campo ausente
// falha; valores validados antes de renderizar (TD-17); destino com o nome do
// projeto renderizado e conferido como caminho relativo limpo (TD-17); camada
// do Claude Code no lugar de .agents/ (TD-26).

// Package templates embute os arquivos que o toolkit gera num projeto e
// declara cada um num catálogo: destino, classe (gerenciado ou do projeto,
// TD-15) e condição (sempre, com um módulo de enabled_modules ou, no CI, com o
// provedor) (TD-22).
//
// O catálogo é a fonte única do que o toolkit gera e do que é dono: init grava
// ExpectedFor(Project, módulos, provedor) e Expected(Managed, módulos); sync só
// re-renderiza Expected(Managed, módulos). O CI do projeto é o único arquivo
// condicionado ao provedor (TD-07). Arquivo gerenciado não depende de nome, módulo
// Go, resumo, provedor nem versão do projeto, só de enabled_modules, para o
// sync conseguir re-renderizá-lo só com a configuração.
package templates

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"unicode"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// Class é a classe de um arquivo gerado (TD-15). O valor zero é inválido, para
// que um File montado sem classe nunca passe por gerenciado ou do projeto.
type Class int

// Classes de arquivo.
const (
	// Managed é arquivo gerenciado: o sync atualiza enquanto o projeto não
	// editar (TD-25).
	Managed Class = iota + 1
	// Project é arquivo do projeto: gerado uma vez pelo init, nunca tocado
	// pelo sync.
	Project
)

func (c Class) String() string {
	switch c {
	case Managed:
		return "gerenciado"
	case Project:
		return "projeto"
	}
	return fmt.Sprintf("Class(%d)", int(c))
}

// Data são os valores que os templates usam. Campo vazio fica fora da
// renderização: template que o usa falha (missingkey=error). Campo preenchido
// é validado antes de renderizar.
type Data struct {
	// Name é o nome do projeto, ^[a-z][a-z0-9-]*$ (TD-17).
	Name string
	// Module é o caminho do módulo Go.
	Module string
	// Summary é o resumo do projeto, uma linha.
	Summary string
	// Provider é o provedor git: github ou gitlab (TD-07).
	Provider string
	// ToolkitVersion é a versão do toolkit fixada no Makefile (TD-06).
	ToolkitVersion string
	// Modules são os módulos de enabled_modules, sem repetição; a ordem não
	// importa (a renderização ordena).
	Modules []string
}

// File é uma entrada do catálogo.
type File struct {
	// Path é o destino relativo à raiz do projeto, com "/". Em arquivo do
	// projeto pode conter [[.Name]] (código inicial em cmd/<nome>/); o
	// destino final sai de DestPath. Em arquivo gerenciado é fixo.
	Path string
	// Class é a classe do arquivo.
	Class Class
	// Module vazio: gerado sempre; senão, só com o módulo em enabled_modules.
	Module string
	// Provider vazio: gerado com qualquer provedor; senão, só com esse
	// git_provider. Só arquivo do projeto tem provedor (o CI).
	Provider string
	// Mode é a permissão do arquivo gravado.
	Mode fs.FileMode
}

//go:embed all:files
var embeddedFS embed.FS

// embedded é a árvore de templates, com raiz em files/.
var embedded fs.FS = mustSub(embeddedFS, "files")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		// fs.Sub só falha com nome de diretório inválido, fixo aqui.
		return f
	}
	return sub
}

// entry é uma entrada do catálogo com a origem embutida.
type entry struct {
	File
	src string // caminho em files/
}

const (
	modeFile = 0o644
	modeExec = 0o755
)

// coreAgents são os subagentes do núcleo (TD-26).
var coreAgents = []string{
	"architecture-guardrails", "code-reviewer", "dev-implementation", "docs-update",
	"git-delivery", "pm-triage", "qa-validation", "security-review",
	"supply-chain-review", "test-writer",
}

// skills são as skills de fluxo (TD-26).
var skills = []string{"fluxo", "nova-delivery", "validar"}

// projectFiles são os arquivos do projeto: destino → origem.
var projectFiles = [][2]string{
	{".claude/settings.json", "project/settings.json.tmpl"},
	{".gitignore", "project/gitignore.tmpl"},
	{".mcp.json", "project/mcp.json.tmpl"},
	{".pipeline-claude.json", "project/pipeline-claude.json.tmpl"},
	{"CLAUDE.md", "project/CLAUDE.md.tmpl"},
	{"Makefile", "project/Makefile.tmpl"},
	{"cmd/[[.Name]]/main.go", "project/main.go.tmpl"},
	{"cmd/[[.Name]]/main_test.go", "project/main_test.go.tmpl"},
	{"docs-ai/01-INVARIANTES-GLOBAIS.md", "project/docs-ai/01-INVARIANTES-GLOBAIS.md.tmpl"},
	{"docs-ai/03-MAPA-RAPIDO-MODULOS.md", "project/docs-ai/03-MAPA-RAPIDO-MODULOS.md.tmpl"},
	{"docs-ai/DECISOES.md", "project/docs-ai/DECISOES.md.tmpl"},
	{"docs-ai/agents/08-HANDOFF-CONTRACT.md", "project/docs-ai/agents/08-HANDOFF-CONTRACT.md.tmpl"},
	{"docs-ai/deliveries/INDEX.md", "project/docs-ai/deliveries/INDEX.md.tmpl"},
	{"docs-ai/deliveries/_template/report.md", "project/docs-ai/deliveries/_template/report.md.tmpl"},
	{"go.mod", "project/go.mod.tmpl"},
}

// ciFiles são os CIs do projeto, um por provedor: destino → origem, provedor.
var ciFiles = [][3]string{
	{".github/workflows/delivery.yml", "project/ci/github-delivery.yml.tmpl", "github"},
	{".gitlab-ci.yml", "project/ci/gitlab-ci.yml.tmpl", "gitlab"},
}

// catalog é montado uma vez, ordenado por destino.
var catalog = buildCatalog()

func buildCatalog() []entry {
	var c []entry
	for _, a := range coreAgents {
		c = append(c, entry{File{Path: ".claude/agents/" + a + ".md", Class: Managed, Mode: modeFile}, "managed/agents/" + a + ".md.tmpl"})
	}
	for m := range config.Modules {
		c = append(c, entry{File{Path: ".claude/agents/" + m + ".md", Class: Managed, Module: m, Mode: modeFile}, "managed/agents/" + m + ".md.tmpl"})
	}
	for _, s := range skills {
		c = append(c, entry{File{Path: ".claude/skills/" + s + "/SKILL.md", Class: Managed, Mode: modeFile}, "managed/skills/" + s + ".md.tmpl"})
	}
	c = append(c, entry{File{Path: ".githooks/pre-push", Class: Managed, Mode: modeExec}, "managed/pre-push.tmpl"})
	for _, p := range projectFiles {
		c = append(c, entry{File{Path: p[0], Class: Project, Mode: modeFile}, p[1]})
	}
	for _, p := range ciFiles {
		c = append(c, entry{File{Path: p[0], Class: Project, Provider: p[2], Mode: modeFile}, p[1]})
	}
	slices.SortFunc(c, func(a, b entry) int { return strings.Compare(a.Path, b.Path) })
	return c
}

// Catalog devolve uma cópia do catálogo, ordenado por Path.
func Catalog() []File {
	out := make([]File, len(catalog))
	for i, e := range catalog {
		out[i] = e.File
	}
	return out
}

// Expected devolve, ordenados por Path, os arquivos da classe que o projeto
// tem com os módulos dados e que não dependem do provedor: os sem módulo e os
// de módulo presente em modules. Módulo fora de config.Modules não traz
// arquivo (a config já o reprova). O CI, que depende do provedor, só sai por
// ExpectedFor: quem não sabe o provedor (sync, adopt) nunca o recebe.
func Expected(class Class, modules []string) []File {
	return ExpectedFor(class, modules, "")
}

// ExpectedFor é Expected mais os arquivos do provedor dado (o CI do projeto,
// TD-07). Provedor vazio ou desconhecido não traz arquivo de provedor.
func ExpectedFor(class Class, modules []string, provider string) []File {
	var out []File
	for _, e := range catalog {
		if e.Class != class {
			continue
		}
		if e.Module != "" && !slices.Contains(modules, e.Module) {
			continue
		}
		if e.Provider != "" && e.Provider != provider {
			continue
		}
		out = append(out, e.File)
	}
	return out
}

// source devolve a origem embutida de f, ou "" se f não está no catálogo.
func source(f File) string {
	for _, e := range catalog {
		if e.File == f {
			return e.src
		}
	}
	return ""
}

// Render renderiza o conteúdo de f com d. Erro se f não estiver no catálogo,
// se um valor de d for inválido ou se o template usar campo vazio de d.
func Render(f File, d Data) ([]byte, error) {
	src := source(f)
	if src == "" {
		return nil, fmt.Errorf("templates: %s não está no catálogo", f.Path)
	}
	vars, err := d.vars()
	if err != nil {
		return nil, err
	}
	text, err := fs.ReadFile(embedded, src)
	if err != nil {
		return nil, fmt.Errorf("templates: %s: %w", f.Path, err)
	}
	out, err := execute(src, string(text), vars)
	if err != nil {
		return nil, fmt.Errorf("templates: %s: %w", f.Path, err)
	}
	return out, nil
}

// DestPath devolve o destino de f relativo à raiz do projeto, com "/":
// Path com os marcadores renderizados por d, conferido como caminho relativo
// limpo, sem ".." e sem "/" inicial (TD-17).
func DestPath(f File, d Data) (string, error) {
	if source(f) == "" {
		return "", fmt.Errorf("templates: %s não está no catálogo", f.Path)
	}
	vars, err := d.vars()
	if err != nil {
		return "", err
	}
	out, err := execute("caminho", f.Path, vars)
	if err != nil {
		return "", fmt.Errorf("templates: destino de %s: %w", f.Path, err)
	}
	p := string(out)
	if p == "" || p != path.Clean(p) || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") ||
		strings.ContainsAny(p, `\:`) || strings.ContainsFunc(p, unicode.IsControl) {
		return "", fmt.Errorf("templates: destino %q de %s inválido", p, f.Path)
	}
	return p, nil
}

func execute(name, text string, vars map[string]any) ([]byte, error) {
	tmpl, err := template.New(name).Delims("[[", "]]").Option("missingkey=error").Funcs(funcs).Parse(text)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, vars); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

var funcs = template.FuncMap{
	// has diz se a lista contém o valor.
	"has": func(list []string, v string) bool { return slices.Contains(list, v) },
	// json devolve o valor em JSON; lista nil vira [].
	"json": func(v any) (string, error) {
		if s, ok := v.([]string); ok && s == nil {
			v = []string{}
		}
		b, err := json.Marshal(v)
		return string(b), err
	},
}

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	versionRe  = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	moduleRe   = regexp.MustCompile(`^[A-Za-z0-9._~/-]+$`)
	errSummary = errors.New("templates: resumo precisa ser uma linha, sem caractere de controle")
)

// vars valida d e devolve os valores da renderização: só os campos não vazios
// viram chave, para missingkey=error reprovar template que use campo ausente.
// Modules está sempre presente, ordenado.
func (d Data) vars() (map[string]any, error) {
	v := map[string]any{}
	if d.Name != "" {
		if !nameRe.MatchString(d.Name) {
			return nil, fmt.Errorf("templates: nome %q inválido (formato ^[a-z][a-z0-9-]*$)", d.Name)
		}
		v["Name"] = d.Name
	}
	if d.Module != "" {
		if !moduleRe.MatchString(d.Module) || strings.HasPrefix(d.Module, "/") || strings.HasSuffix(d.Module, "/") ||
			strings.Contains(d.Module, "..") {
			return nil, fmt.Errorf("templates: módulo Go %q inválido", d.Module)
		}
		v["Module"] = d.Module
	}
	if d.Summary != "" {
		if strings.ContainsFunc(d.Summary, unicode.IsControl) || strings.TrimSpace(d.Summary) == "" {
			return nil, errSummary
		}
		v["Summary"] = strings.TrimSpace(d.Summary)
	}
	if d.Provider != "" {
		if d.Provider != "github" && d.Provider != "gitlab" {
			return nil, fmt.Errorf("templates: provedor %q não suportado (use \"github\" ou \"gitlab\")", d.Provider)
		}
		v["Provider"] = d.Provider
	}
	if d.ToolkitVersion != "" {
		if !versionRe.MatchString(d.ToolkitVersion) {
			return nil, fmt.Errorf("templates: versão do toolkit %q inválida (esperado vX.Y.Z)", d.ToolkitVersion)
		}
		v["ToolkitVersion"] = d.ToolkitVersion
	}
	mods := slices.Clone(d.Modules)
	slices.Sort(mods)
	for i, m := range mods {
		if !config.Modules[m] {
			return nil, fmt.Errorf("templates: módulo opcional %q fora do catálogo de módulos", m)
		}
		if i > 0 && mods[i-1] == m {
			return nil, fmt.Errorf("templates: módulo opcional %q repetido", m)
		}
	}
	if mods == nil {
		mods = []string{}
	}
	v["Modules"] = mods
	return v, nil
}

// agentSensors são os sensores que cada subagente pede pelo nome em
// pipeline_sensors, no lugar dos comandos que rodava pelo Bash (TD-29).
var agentSensors = map[string][]string{
	"agent-auditor":       {"test"},
	"security-review":     {"vuln"},
	"supply-chain-review": {"mod_verify", "vuln"},
}

// RequiredSensors devolve, ordenados e sem repetição, os sensores que os
// subagentes do projeto com os módulos dados pedem pelo nome: a config precisa
// declará-los (o doctor confere).
func RequiredSensors(modules []string) []string {
	var out []string
	for _, f := range Expected(Managed, modules) {
		name, ok := strings.CutPrefix(f.Path, ".claude/agents/")
		if !ok {
			continue
		}
		out = append(out, agentSensors[strings.TrimSuffix(name, ".md")]...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// DefaultSensors devolve os sensores da config gerada pelo init, na ordem do
// template: é a referência para quem precisa acrescentar um sensor à mão.
func DefaultSensors() ([]config.Sensor, error) {
	for _, e := range catalog {
		if e.Path != config.FileName {
			continue
		}
		out, err := Render(e.File, Data{
			Name: "projeto", Module: "projeto", Summary: "Projeto.",
			Provider: "github", ToolkitVersion: "v0.0.0",
		})
		if err != nil {
			return nil, err
		}
		var cfg struct {
			Sensors []config.Sensor `json:"sensors"`
		}
		if err := json.Unmarshal(out, &cfg); err != nil {
			return nil, fmt.Errorf("templates: %s gerado ilegível: %w", config.FileName, err)
		}
		return cfg.Sensors, nil
	}
	return nil, fmt.Errorf("templates: %s fora do catálogo", config.FileName)
}

// SensorUsers devolve, ordenados, os subagentes do catálogo que pedem o
// sensor pelo nome.
func SensorUsers(sensor string) []string {
	var out []string
	for agent, sensors := range agentSensors {
		if slices.Contains(sensors, sensor) {
			out = append(out, agent)
		}
	}
	slices.Sort(out)
	return out
}
