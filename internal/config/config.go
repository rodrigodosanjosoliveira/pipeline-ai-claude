// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/config/config.go @ 0eac73a (MIT).
// Alterações: arquivo .pipeline-claude.json e DisallowUnknownFields (TD-14); v1 só
// api + postgres, git_provider github|gitlab (TD-07); casamento de padrão sobre
// caminho relativo e sem diferenciar caixa, IsNoAgent (TD-09, TD-10); auto_flags e
// FlagsTriggeredBy (TD-13); report_schema (TD-12); sensors; validação de caminhos
// e nomes; erro de acesso no Find não vira "não encontrado" (TD-08); módulo
// repetido em enabled_modules recusado (TD-28).

// Package config carrega a configuração do toolkit a partir do arquivo
// .pipeline-claude.json na raiz do projeto.
//
// A configuração fica no projeto, e não no binário: caminhos protegidos,
// agentes editores, esquema de report e flags variam por projeto. Um binário
// único atende a todos.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// FileName é o nome do arquivo de configuração, procurado na raiz do projeto.
const FileName = ".pipeline-claude.json"

// ErrNotFound indica que nenhum arquivo de configuração foi encontrado a partir
// do diretório informado até a raiz do filesystem.
var ErrNotFound = errors.New("config: " + FileName + " não encontrado")

// Modules é o catálogo de módulos opcionais aceitos em enabled_modules.
var Modules = map[string]bool{
	"data-model":         true,
	"migration-reviewer": true,
	"query-performance":  true,
	"release-ops":        true,
	"agent-auditor":      true,
	"context-loader":     true,
	"tenant-isolation":   true,
}

// Config é o conteúdo de .pipeline-claude.json.
//
// Caminhos e padrões de caminho são relativos à raiz do projeto, com "/" como
// separador, sem ".." que saia da raiz e sem "/" inicial. Um padrão casa com o
// caminho exato ou com tudo abaixo dele como diretório; "/" final é opcional e
// só documenta a intenção. O casamento não diferencia caixa (TD-09).
type Config struct {
	// Version é a versão do formato. Obrigatória; só 1 é aceita (TD-14).
	Version int `json:"version"`

	// ProjectType é o tipo de projeto. Na v1, só "api" (TD-07).
	ProjectType string `json:"project_type"`

	// Database é o banco de dados do projeto. Na v1, só "postgres" (TD-07).
	Database string `json:"database"`

	// GitProvider é o provedor do repositório: "github" ou "gitlab" (TD-07).
	GitProvider string `json:"git_provider"`

	// BaseRef é a referência git contra a qual o validador calcula o diff da
	// delivery. Padrão: "origin/main".
	BaseRef string `json:"base_ref"`

	// DeliveriesDir é o diretório dos relatórios de entrega, um subdiretório
	// por delivery. Padrão: "docs-ai/deliveries".
	DeliveriesDir string `json:"deliveries_dir"`

	// IndexFile é o índice em que toda delivery precisa estar registrada.
	// Padrão: "<deliveries_dir>/INDEX.md".
	IndexFile string `json:"index_file"`

	// ReportTemplate é o modelo de report usado para criar deliveries. Opcional.
	ReportTemplate string `json:"report_template"`

	// DeliveryIDPattern é a expressão regular que todo delivery_id precisa
	// casar. Opcional; se informada, tem de ser ancorada ("^...$").
	DeliveryIDPattern string `json:"delivery_id_pattern"`

	// DeliveryPrefixes são os prefixos de delivery_id aceitos pelo projeto
	// (por exemplo "FEAT", "FIX"). Opcional.
	DeliveryPrefixes []string `json:"delivery_prefixes"`

	// InvariantsFiles são os documentos de invariantes que os agentes leem
	// antes de alterar o projeto. Opcional.
	InvariantsFiles []string `json:"invariants_files"`

	// ProtectedPaths são os padrões que só agentes editores podem alterar; a
	// sessão principal nunca altera (TD-10). Obrigatório e não vazio.
	ProtectedPaths []string `json:"protected_paths"`

	// NoAgentPaths são os padrões que nenhum agente altera, nem editor: a
	// mudança é feita fora da sessão (TD-10). Todo padrão daqui precisa estar
	// coberto por ProtectedPaths.
	NoAgentPaths []string `json:"no_agent_paths"`

	// EditorAgents são os subagentes (agent_type) autorizados a alterar
	// ProtectedPaths. Nomes no formato ^[a-z][a-z0-9-]*$.
	EditorAgents []string `json:"editor_agents"`

	// EnabledModules são os módulos opcionais ativos no projeto, do catálogo
	// Modules.
	EnabledModules []string `json:"enabled_modules"`

	// SecuritySurfacePaths são os padrões cuja alteração exige
	// require_security no report, mesmo que o triage não tenha marcado.
	SecuritySurfacePaths []string `json:"security_surface_paths"`

	// AutoFlags liga flag do report a padrões de caminho: se o diff tocar um
	// padrão, a flag precisa estar true (TD-13). Chaves ^require_[a-z_]+$.
	AutoFlags map[string][]string `json:"auto_flags"`

	// ReportSchema é o esquema do report de delivery (TD-12). Ausente, vale
	// DefaultReportSchema; presente, substitui o padrão por inteiro.
	ReportSchema *ReportSchema `json:"report_schema"`

	// Sensors são as verificações do projeto, executadas por
	// `pipeline-claude sensors` e pela ferramenta MCP pipeline_sensors (TD-19).
	Sensors []Sensor `json:"sensors"`

	// root é a raiz do projeto, preenchida no carregamento.
	root string
}

// ReportSchema descreve o que um report de delivery precisa conter (TD-12).
type ReportSchema struct {
	// SectionLevel é o nível de título que abre seção (2 = "## "), de 1 a 6.
	SectionLevel int `json:"section_level"`

	// FrontRequired são as chaves obrigatórias e não vazias do front matter.
	FrontRequired []string `json:"front_required"`

	// SectionsRequired são as seções obrigatórias e não vazias.
	SectionsRequired []string `json:"sections_required"`

	// FieldsRequired lista, por seção, os campos ("- Nome: valor") que
	// precisam de valor fora de PlaceholderValues.
	FieldsRequired map[string][]string `json:"fields_required"`

	// FlagSections liga uma flag do front matter à seção e aos campos exigidos
	// quando ela é true. Chaves ^require_[a-z_]+$.
	FlagSections map[string]FlagSection `json:"flag_sections"`

	// ChecklistSection é a seção em que item "- [ ]" reprova o report.
	ChecklistSection string `json:"checklist_section"`

	// PlaceholderValues são valores tratados como não preenchidos, comparados
	// sem diferenciar caixa e sem espaços nas pontas.
	PlaceholderValues []string `json:"placeholder_values"`

	// ForbidMarkers são marcadores que reprovam o report quando aparecem como
	// palavra inteira fora de bloco de código.
	ForbidMarkers []string `json:"forbid_markers"`
}

// FlagSection é a seção e os campos exigidos por uma flag do report.
type FlagSection struct {
	// Section é o título da seção exigida.
	Section string `json:"section"`
	// Fields são os campos obrigatórios dessa seção.
	Fields []string `json:"fields"`
}

// Sensor é uma verificação executada sobre a delivery.
type Sensor struct {
	// Name identifica o sensor; único, no formato ^[a-z][a-z0-9_]*$.
	Name string `json:"name"`
	// Cmd é o comando e seus argumentos, executado sem shell. Não vazio.
	Cmd []string `json:"cmd"`
}

// DefaultReportSchema devolve o esquema de report padrão. Cada chamada devolve
// uma cópia independente.
func DefaultReportSchema() ReportSchema {
	return ReportSchema{
		SectionLevel:     2,
		FrontRequired:    []string{"delivery_id", "status"},
		SectionsRequired: []string{"PM/Triage", "Architecture/Guardrails", "Dev", "QA", "Docs", "Final Checklist"},
		FieldsRequired: map[string][]string{
			"PM/Triage":               {"Escopo fechado", "Criterios de aceite", "Riscos"},
			"Architecture/Guardrails": {"Invariantes aplicaveis", "Arquivos provaveis de impacto"},
			"Dev":                     {"Arquivos alterados", "Resumo tecnico", "Testes criados/ajustados", "Evidencias (codigo/teste)"},
			"QA":                      {"Resultado", "Cenarios executados"},
			"Docs":                    {"Docs atualizados"},
		},
		FlagSections: map[string]FlagSection{
			"require_security":    {Section: "Security", Fields: []string{"Resultado"}},
			"require_release_ops": {Section: "Release/Ops", Fields: []string{"Plano de rollback"}},
		},
		ChecklistSection:  "Final Checklist",
		PlaceholderValues: []string{"", "todo", "tbd", "pendente", "n/a", "<delivery_id>", "<modulo>"},
		ForbidMarkers:     []string{"TODO", "TBD", "FIXME", "XXX"},
	}
}

// Root devolve o diretório raiz do projeto (onde o arquivo foi encontrado).
func (c *Config) Root() string { return c.root }

// Find sobe a partir de start procurando FileName, até a raiz do filesystem.
// Devolve o caminho do arquivo encontrado, ErrNotFound se não houver nenhum, ou
// erro se um candidato não puder ser verificado ou não for arquivo regular.
func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("config: caminho inicial inválido: %w", err)
	}
	for {
		candidate := filepath.Join(dir, FileName)
		info, err := os.Stat(candidate)
		switch {
		case err == nil && info.Mode().IsRegular():
			return candidate, nil
		case err == nil:
			return "", fmt.Errorf("config: %s não é um arquivo regular", candidate)
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("config: falha ao procurar %s: %w", candidate, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotFound
		}
		dir = parent
	}
}

// Load procura e carrega a configuração a partir de start.
func Load(start string) (*Config, error) {
	file, err := Find(start)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("config: falha ao ler %s: %w", file, err)
	}
	c, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", file, err)
	}
	c.root = filepath.Dir(file)
	return c, nil
}

// decode interpreta, completa com os padrões e valida o conteúdo do arquivo.
func decode(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON inválido: conteúdo depois do objeto de configuração")
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

var (
	nameRe         = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	flagRe         = regexp.MustCompile(`^require_[a-z_]+$`)
	sensorRe       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	driveRe        = regexp.MustCompile(`^[A-Za-z]:`)
	errVersion     = errors.New("version é obrigatória e precisa ser 1")
	errNoProtected = errors.New("protected_paths está vazio: o hook não protegeria nada")
)

// normalize aplica os padrões, valida cada campo e deixa os caminhos na forma
// limpa. Erro descreve o campo e o motivo.
func (c *Config) normalize() error {
	if c.Version != 1 {
		if c.Version == 0 {
			return errVersion
		}
		return fmt.Errorf("version %d não suportada por este binário (esperado 1)", c.Version)
	}
	if err := oneOf("project_type", c.ProjectType, "api"); err != nil {
		return err
	}
	if err := oneOf("database", c.Database, "postgres"); err != nil {
		return err
	}
	if err := oneOf("git_provider", c.GitProvider, "github", "gitlab"); err != nil {
		return err
	}

	if c.BaseRef == "" {
		c.BaseRef = "origin/main"
	}
	if strings.HasPrefix(c.BaseRef, "-") || strings.ContainsFunc(c.BaseRef, isSpaceOrControl) {
		return fmt.Errorf("base_ref %q inválido: não pode começar com \"-\" nem ter espaço", c.BaseRef)
	}

	if c.DeliveriesDir == "" {
		c.DeliveriesDir = "docs-ai/deliveries"
	}
	dd, err := cleanRel(c.DeliveriesDir)
	if err != nil {
		return fmt.Errorf("deliveries_dir: %w", err)
	}
	c.DeliveriesDir = strings.TrimSuffix(dd, "/")
	if c.IndexFile == "" {
		c.IndexFile = path.Join(c.DeliveriesDir, "INDEX.md")
	}
	if c.IndexFile, err = cleanFile(c.IndexFile); err != nil {
		return fmt.Errorf("index_file: %w", err)
	}
	if c.ReportTemplate != "" {
		if c.ReportTemplate, err = cleanFile(c.ReportTemplate); err != nil {
			return fmt.Errorf("report_template: %w", err)
		}
	}
	for i, f := range c.InvariantsFiles {
		if c.InvariantsFiles[i], err = cleanFile(f); err != nil {
			return fmt.Errorf("invariants_files[%d]: %w", i, err)
		}
	}

	if c.DeliveryIDPattern != "" {
		if err := checkAnchored(c.DeliveryIDPattern); err != nil {
			return fmt.Errorf("delivery_id_pattern %q: %w", c.DeliveryIDPattern, err)
		}
	}
	for i, p := range c.DeliveryPrefixes {
		if p == "" || p != strings.TrimSpace(p) {
			return fmt.Errorf("delivery_prefixes[%d] %q: vazio ou com espaço nas pontas", i, p)
		}
	}

	if len(c.ProtectedPaths) == 0 {
		return errNoProtected
	}
	if err := cleanAll("protected_paths", c.ProtectedPaths); err != nil {
		return err
	}
	if err := cleanAll("no_agent_paths", c.NoAgentPaths); err != nil {
		return err
	}
	for i, n := range c.NoAgentPaths {
		if !matchesAny(strings.TrimSuffix(n, "/"), c.ProtectedPaths) {
			return fmt.Errorf("no_agent_paths[%d] %q não está coberto por protected_paths", i, n)
		}
	}
	if err := cleanAll("security_surface_paths", c.SecuritySurfacePaths); err != nil {
		return err
	}
	for flag, patterns := range c.AutoFlags {
		if !flagRe.MatchString(flag) {
			return fmt.Errorf("auto_flags: chave %q inválida (formato ^require_[a-z_]+$)", flag)
		}
		if len(patterns) == 0 {
			return fmt.Errorf("auto_flags[%q]: lista de padrões vazia", flag)
		}
		if err := cleanAll(fmt.Sprintf("auto_flags[%q]", flag), patterns); err != nil {
			return err
		}
	}

	for i, a := range c.EditorAgents {
		if !nameRe.MatchString(a) {
			return fmt.Errorf("editor_agents[%d] %q inválido (formato ^[a-z][a-z0-9-]*$)", i, a)
		}
	}
	seenModule := map[string]bool{}
	for i, m := range c.EnabledModules {
		if !nameRe.MatchString(m) {
			return fmt.Errorf("enabled_modules[%d] %q inválido (formato ^[a-z][a-z0-9-]*$)", i, m)
		}
		if !Modules[m] {
			return fmt.Errorf("enabled_modules[%d] %q não está no catálogo de módulos", i, m)
		}
		if seenModule[m] {
			return fmt.Errorf("enabled_modules[%d] %q repetido; deixe uma ocorrência só (TD-28)", i, m)
		}
		seenModule[m] = true
	}

	if c.ReportSchema == nil {
		s := DefaultReportSchema()
		c.ReportSchema = &s
	}
	if err := c.ReportSchema.validate(); err != nil {
		return fmt.Errorf("report_schema: %w", err)
	}

	seen := make(map[string]bool, len(c.Sensors))
	for i, s := range c.Sensors {
		if !sensorRe.MatchString(s.Name) {
			return fmt.Errorf("sensors[%d]: name %q inválido (formato ^[a-z][a-z0-9_]*$)", i, s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("sensors[%d]: name %q repetido", i, s.Name)
		}
		seen[s.Name] = true
		if len(s.Cmd) == 0 || s.Cmd[0] == "" {
			return fmt.Errorf("sensors[%d] %q: cmd vazio", i, s.Name)
		}
	}
	return nil
}

// validate aplica as mesmas regras que report.Check aplica ao esquema
// (report.validateSchema), para que uma config aceita aqui nunca falhe lá.
// Mudou uma, mude a outra.
func (s *ReportSchema) validate() error {
	if s.SectionLevel < 1 || s.SectionLevel > 6 {
		return fmt.Errorf("section_level %d fora do intervalo 1 a 6", s.SectionLevel)
	}
	for i, k := range s.FrontRequired {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("front_required[%d] vazio", i)
		}
	}
	for i, t := range s.SectionsRequired {
		if blankTitle(t) {
			return fmt.Errorf("sections_required[%d] %q vazio", i, t)
		}
	}
	for sec, fields := range s.FieldsRequired {
		if blankTitle(sec) {
			return fmt.Errorf("fields_required: seção %q vazia", sec)
		}
		for i, f := range fields {
			if blankTitle(f) {
				return fmt.Errorf("fields_required[%q][%d] %q vazio", sec, i, f)
			}
		}
	}
	for flag, fs := range s.FlagSections {
		if !flagRe.MatchString(flag) {
			return fmt.Errorf("flag_sections: chave %q inválida (formato ^require_[a-z_]+$)", flag)
		}
		if blankTitle(fs.Section) {
			return fmt.Errorf("flag_sections[%q]: section %q vazia", flag, fs.Section)
		}
		for i, f := range fs.Fields {
			if blankTitle(f) {
				return fmt.Errorf("flag_sections[%q].fields[%d] %q vazio", flag, i, f)
			}
		}
	}
	if strings.TrimSpace(s.ChecklistSection) != "" && blankTitle(s.ChecklistSection) {
		return fmt.Errorf("checklist_section %q inválida", s.ChecklistSection)
	}
	for i, m := range s.ForbidMarkers {
		if m == "" || strings.TrimSpace(m) != m {
			return fmt.Errorf("forbid_markers[%d] %q vazio ou com espaço nas pontas", i, m)
		}
	}
	return nil
}

// blankTitle diz se um título ou nome de campo fica vazio na normalização do
// report (report.NormalizeTitle), que descarta espaços, ":", "." e marcas de
// acento combinantes. Acentos pré-compostos viram letra e nunca somem.
func blankTitle(s string) bool {
	for _, r := range s {
		if r != ':' && r != '.' && !unicode.IsSpace(r) && !unicode.Is(unicode.Mn, r) {
			return false
		}
	}
	return true
}

func oneOf(field, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	want := `"` + strings.Join(allowed, `" ou "`) + `"`
	if value == "" {
		return fmt.Errorf("%s é obrigatório (%s)", field, want)
	}
	return fmt.Errorf("%s %q não suportado (use %s)", field, value, want)
}

func isSpaceOrControl(r rune) bool {
	return r <= ' ' || r == 0x7f
}

// cleanRel valida um caminho relativo à raiz e devolve a forma limpa, mantendo
// a "/" final se houver.
func cleanRel(p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("caminho vazio")
	case p != strings.TrimSpace(p):
		return "", fmt.Errorf("%q tem espaço nas pontas", p)
	case strings.Contains(p, `\`):
		return "", fmt.Errorf(`%q usa "\"; use "/" como separador`, p)
	case strings.HasPrefix(p, "/"), driveRe.MatchString(p):
		return "", fmt.Errorf("%q é absoluto; use caminho relativo à raiz do projeto", p)
	}
	clean := path.Clean(p)
	switch {
	case clean == ".":
		return "", fmt.Errorf("%q aponta para a raiz do projeto inteira", p)
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return "", fmt.Errorf("%q sai da raiz do projeto", p)
	}
	if strings.HasSuffix(p, "/") {
		clean += "/"
	}
	return clean, nil
}

// cleanFile é cleanRel para caminho que precisa ser arquivo.
func cleanFile(p string) (string, error) {
	clean, err := cleanRel(p)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(clean, "/") {
		return "", fmt.Errorf("%q termina em \"/\"; esperado um arquivo", p)
	}
	return clean, nil
}

// cleanAll aplica cleanRel a cada item de list, no lugar.
func cleanAll(field string, list []string) error {
	for i, p := range list {
		clean, err := cleanRel(p)
		if err != nil {
			return fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		list[i] = clean
	}
	return nil
}

// checkAnchored exige que expr compile e só case o texto inteiro: a árvore,
// sem capturas externas, precisa ser ^ seguido de algo seguido de $. Isso
// rejeita "^a|b$" e "(?m)^a$", que passariam numa checagem de prefixo/sufixo.
func checkAnchored(expr string) error {
	if _, err := regexp.Compile(expr); err != nil {
		return fmt.Errorf("não compila: %w", err)
	}
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return fmt.Errorf("não compila: %w", err)
	}
	re = re.Simplify()
	for re.Op == syntax.OpCapture && len(re.Sub) == 1 {
		re = re.Sub[0]
	}
	if re.Op == syntax.OpConcat && len(re.Sub) >= 2 &&
		re.Sub[0].Op == syntax.OpBeginText && re.Sub[len(re.Sub)-1].Op == syntax.OpEndText {
		return nil
	}
	return errors.New(`precisa ser ancorada no texto inteiro ("^...$")`)
}

// IsModuleEnabled informa se o módulo opcional name está habilitado no projeto.
func (c *Config) IsModuleEnabled(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, m := range c.EnabledModules {
		if strings.EqualFold(m, name) {
			return true
		}
	}
	return false
}

// IsEditorAgent informa se agentType pode editar caminho protegido.
// Um agentType vazio significa sessão principal, que nunca pode (TD-10).
func (c *Config) IsEditorAgent(agentType string) bool {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return false
	}
	for _, a := range c.EditorAgents {
		if strings.EqualFold(a, agentType) {
			return true
		}
	}
	return false
}

// IsProtected informa se rel, relativo à raiz do projeto, cai em protected_paths.
// A resolução de caminho absoluto e symlinks é do chamador (TD-09).
func (c *Config) IsProtected(rel string) bool {
	return matchesAny(rel, c.ProtectedPaths)
}

// IsNoAgent informa se rel, relativo à raiz do projeto, cai em no_agent_paths.
func (c *Config) IsNoAgent(rel string) bool {
	return matchesAny(rel, c.NoAgentPaths)
}

// IsSecuritySurface informa se rel, relativo à raiz do projeto, toca a
// superfície de segurança.
func (c *Config) IsSecuritySurface(rel string) bool {
	return matchesAny(rel, c.SecuritySurfacePaths)
}

// FlagsTriggeredBy devolve, em ordem alfabética, as flags de auto_flags cujos
// padrões casam com rel (TD-13). Nil se nenhuma casar.
func (c *Config) FlagsTriggeredBy(rel string) []string {
	var flags []string
	for flag, patterns := range c.AutoFlags {
		if matchesAny(rel, patterns) {
			flags = append(flags, flag)
		}
	}
	sort.Strings(flags)
	return flags
}

// matchesAny aplica as regras de casamento de padrão a rel: o padrão, sem a
// "/" final, casa com o caminho exato ou com tudo abaixo dele. A comparação não
// diferencia caixa, errando para o lado do bloqueio em filesystem
// case-insensitive (TD-09). rel fora da raiz ("../...") nunca casa.
func matchesAny(rel string, patterns []string) bool {
	if rel == "" {
		return false
	}
	rel = strings.ToLower(path.Clean(filepath.ToSlash(rel)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSuffix(p, "/"))
		if p == "" {
			continue
		}
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}
