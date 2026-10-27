// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/report/report.go @ 0eac73a (MIT).
// Alterações: seções, campos, flags, marcadores e placeholders vêm do
// report_schema da configuração em vez de listas fixas (TD-12); seção por nível
// de título configurável, títulos em bloco de código ignorados e campos
// "- Nome: valor" com sub-itens; booleano estrito true/false (TD-12); front
// matter, bloco de código não fechado e esquema inválido viram erro em vez de
// serem ignorados (TD-08); marcadores por palavra inteira com fronteira Unicode;
// flag_front: flag que exige campos do front matter (TD-30).

// Package report faz o parsing e a checagem do report de delivery: um arquivo
// Markdown com front matter.
//
// Parse lê o arquivo sem conhecer o esquema, a não ser pelo nível de título que
// abre seção. Check confere o report contra o report_schema da configuração
// (TD-12). As checagens que dependem do repositório (diff, índice, ID da
// delivery) ficam no validador.
//
// O front matter é lido por um parser próprio, restrito a "chave: valor", para
// manter o núcleo só stdlib (TD-03). Na dúvida, o parser e as checagens
// reprovam: nada é liberado por não ter sido entendido (TD-08).
package report

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// Níveis de Finding.
const (
	LevelError   = "ERRO"
	LevelWarning = "AVISO"
)

// Finding é um problema encontrado na checagem do report.
type Finding struct {
	// Level é LevelError ou LevelWarning.
	Level string `json:"level"`
	// Message descreve o problema, em português, citando seção, campo e linha
	// quando houver.
	Message string `json:"message"`
}

func (f Finding) String() string { return f.Level + ": " + f.Message }

// Report é um report de delivery já parseado. Só um Report devolvido por Parse
// pode ser passado a Check.
type Report struct {
	// Level é o nível de título que abriu as seções (2 = "## ").
	Level int
	// Front são os pares do front matter, com a chave em minúsculas e o valor
	// sem espaços nas pontas e sem o par de aspas externo.
	Front map[string]string
	// Sections são as seções na ordem em que aparecem. O mesmo título pode
	// aparecer mais de uma vez; Check reprova a repetição de seção que o
	// esquema usa.
	Sections []Section

	frontLines []line // linhas do front matter, entre os "---"
	body       []line // linhas depois do front matter
	parsed     bool
}

// Section é uma seção do report, aberta por um título do nível Report.Level.
type Section struct {
	// Title é o título como escrito, sem os "#".
	Title string
	// Key é o título normalizado por NormalizeTitle.
	Key string
	// Line é a linha do título no arquivo (a partir de 1).
	Line int
	// Body é o texto da seção sem o título, sem espaços nas pontas. Inclui
	// títulos de nível mais baixo e blocos de código.
	Body string
	// Fields são os campos "- Nome: valor" de topo da seção, na ordem do
	// arquivo. O mesmo nome pode aparecer mais de uma vez.
	Fields []Field

	lines []line
}

// Field é um campo "- Nome: valor" de uma seção.
type Field struct {
	// Name é o nome como escrito.
	Name string
	// Key é o nome normalizado por NormalizeTitle.
	Key string
	// Value é o valor sem espaços nas pontas. Se a linha do campo não tem
	// valor, é a junção (com "\n") dos sub-itens indentados logo abaixo, sem o
	// marcador de lista, até o próximo item de topo, linha em branco ou fim da
	// seção.
	Value string
	// Line é a linha do campo no arquivo (a partir de 1).
	Line int
}

// line é uma linha do arquivo com o número (a partir de 1) e se está dentro de
// um bloco de código cercado (a própria cerca conta como código).
type line struct {
	num  int
	text string
	code bool
}

// flagRe é o formato de chave de flag, o mesmo de auto_flags e flag_sections
// na configuração.
var (
	flagRe     = regexp.MustCompile(`^require_[a-z_]+$`)
	frontKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Parse lê um report. level é o nível de título que abre seção (1 a 6; o
// section_level do esquema). Erros: nível fora do intervalo, front matter
// ausente, não fechado ou com linha inválida ou chave repetida, e bloco de
// código cercado não fechado.
func Parse(data []byte, level int) (*Report, error) {
	if level < 1 || level > 6 {
		return nil, fmt.Errorf("report: nível de seção %d fora do intervalo 1 a 6", level)
	}
	content := strings.TrimPrefix(string(data), "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	raw := strings.Split(content, "\n")

	if trimRight(raw[0]) != "---" {
		return nil, errors.New("report: o arquivo não começa com front matter (---)")
	}
	end := -1
	for i := 1; i < len(raw); i++ {
		if trimRight(raw[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, errors.New("report: front matter não foi fechado com ---")
	}

	r := &Report{Level: level, Front: map[string]string{}, parsed: true}
	for i := 1; i < end; i++ {
		r.frontLines = append(r.frontLines, line{num: i + 1, text: raw[i]})
		if err := r.parseFrontLine(raw[i], i+1); err != nil {
			return nil, err
		}
	}
	for i := end + 1; i < len(raw); i++ {
		r.body = append(r.body, line{num: i + 1, text: raw[i]})
	}
	if err := markCode(r.body); err != nil {
		return nil, err
	}
	r.splitSections()
	return r, nil
}

func (r *Report) parseFrontLine(text string, num int) error {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "#") {
		return nil
	}
	key, value, ok := strings.Cut(t, ":")
	if !ok {
		return fmt.Errorf("report: front matter, linha %d: esperava \"chave: valor\"", num)
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return fmt.Errorf("report: front matter, linha %d: chave vazia", num)
	}
	if _, dup := r.Front[key]; dup {
		return fmt.Errorf("report: front matter, linha %d: chave %q repetida", num, key)
	}
	r.Front[key] = unquote(strings.TrimSpace(value))
	return nil
}

// unquote tira um par de aspas (simples ou duplas) que envolva o valor inteiro.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func trimRight(s string) string { return strings.TrimRight(s, " \t") }

// markCode marca as linhas dentro de blocos de código cercados por ``` ou ~~~
// (até três espaços de recuo; o fechamento usa o mesmo caractere, pelo menos o
// mesmo comprimento e nada além de espaços). Bloco não fechado é erro: o resto
// do arquivo ficaria fora das checagens.
func markCode(lines []line) error {
	var open bool
	var fenceChar byte
	var fenceLen, openLine int
	for i := range lines {
		ch, n, rest := fenceOf(lines[i].text)
		switch {
		case !open && n > 0:
			open, fenceChar, fenceLen, openLine = true, ch, n, lines[i].num
			lines[i].code = true
		case open:
			lines[i].code = true
			if n >= fenceLen && ch == fenceChar && strings.TrimSpace(rest) == "" {
				open = false
			}
		}
	}
	if open {
		return fmt.Errorf("report: bloco de código aberto na linha %d não foi fechado", openLine)
	}
	return nil
}

// fenceOf devolve o caractere e o comprimento da cerca no início da linha (0
// se não houver) e o que vem depois dela.
func fenceOf(text string) (byte, int, string) {
	t := text
	for i := 0; i < 3 && strings.HasPrefix(t, " "); i++ {
		t = t[1:]
	}
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return 0, 0, ""
	}
	ch := t[0]
	n := 0
	for n < len(t) && t[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, ""
	}
	return ch, n, t[n:]
}

// heading devolve o nível e o título de uma linha de título ATX ("## Título"),
// ou ok=false. O título começa na coluna 0; a sequência de "#" de fechamento é
// removida.
func heading(text string) (level int, title string, ok bool) {
	for level < len(text) && text[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	rest := text[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	title = strings.TrimSpace(rest)
	if trimmed := strings.TrimRight(title, "#"); trimmed != title {
		if trimmed == "" || strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t") {
			title = strings.TrimSpace(trimmed)
		}
	}
	return level, title, true
}

// splitSections abre uma seção a cada título do nível r.Level fora de bloco de
// código. Título de nível acima (menos "#") fecha a seção corrente sem abrir
// outra; título de nível abaixo faz parte do corpo.
func (r *Report) splitSections() {
	cur := -1
	for _, l := range r.body {
		if !l.code {
			if lvl, title, ok := heading(l.text); ok && lvl <= r.Level {
				cur = -1
				if lvl == r.Level {
					r.Sections = append(r.Sections, Section{
						Title: title,
						Key:   NormalizeTitle(title),
						Line:  l.num,
					})
					cur = len(r.Sections) - 1
				}
				continue
			}
		}
		if cur >= 0 {
			r.Sections[cur].lines = append(r.Sections[cur].lines, l)
		}
	}
	for i := range r.Sections {
		s := &r.Sections[i]
		texts := make([]string, len(s.lines))
		for j, l := range s.lines {
			texts[j] = l.text
		}
		s.Body = strings.TrimSpace(strings.Join(texts, "\n"))
		s.Fields = parseFields(s.lines)
	}
}

// parseFields lê os campos de topo ("- Nome: valor" na coluna 0) fora de bloco
// de código.
func parseFields(lines []line) []Field {
	var out []Field
	for i, l := range lines {
		if l.code || !strings.HasPrefix(l.text, "- ") {
			continue
		}
		name, value, ok := strings.Cut(l.text[2:], ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.HasPrefix(name, "[") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			var subs []string
			for _, sub := range lines[i+1:] {
				if sub.code || strings.TrimSpace(sub.text) == "" ||
					(!strings.HasPrefix(sub.text, " ") && !strings.HasPrefix(sub.text, "\t")) {
					break
				}
				if v := stripBullet(strings.TrimSpace(sub.text)); v != "" {
					subs = append(subs, v)
				}
			}
			value = strings.Join(subs, "\n")
		}
		out = append(out, Field{Name: name, Key: NormalizeTitle(name), Value: value, Line: l.num})
	}
	return out
}

func stripBullet(s string) string {
	for _, b := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(s, b) {
			return strings.TrimSpace(s[len(b):])
		}
	}
	if s == "-" || s == "*" || s == "+" {
		return ""
	}
	return s
}

// accents mapeia letras minúsculas acentuadas para a letra base. Acentos
// combinantes (forma decomposta) são removidos à parte, por unicode.Mn.
var accents = map[rune]rune{}

func init() {
	for base, list := range map[rune]string{
		'a': "àáâãäåāăą",
		'c': "çćĉċč",
		'd': "ď",
		'e': "èéêëēĕėęě",
		'g': "ĝğġģ",
		'i': "ìíîïĩīĭįı",
		'n': "ñńņň",
		'o': "òóôõöøōŏő",
		'r': "ŕŗř",
		's': "śŝşš",
		't': "ţť",
		'u': "ùúûüũūŭůűų",
		'y': "ýÿŷ",
		'z': "źżž",
	} {
		for _, r := range list {
			accents[r] = base
		}
	}
}

// NormalizeTitle deixa títulos e nomes de campo comparáveis: minúsculas, sem
// acento, sem ":" e ".", com espaços internos colapsados e sem espaços nas
// pontas.
func NormalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ':' || r == '.' || unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			if base, ok := accents[r]; ok {
				r = base
			}
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Flag lê uma flag booleana do front matter. Ausente vale false. Presente, o
// valor tem de ser exatamente "true" ou "false"; qualquer outro é erro (TD-12).
func (r *Report) Flag(name string) (bool, error) {
	v, ok := r.Front[strings.ToLower(name)]
	if !ok {
		return false, nil
	}
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("flag %q com valor %q inválido (use true ou false)", strings.ToLower(name), v)
}

// Section devolve a primeira seção cujo título normalizado é igual ao de title.
func (r *Report) Section(title string) (*Section, bool) {
	key := NormalizeTitle(title)
	for i := range r.Sections {
		if r.Sections[i].Key == key {
			return &r.Sections[i], true
		}
	}
	return nil, false
}

// Field devolve o primeiro campo name da primeira seção section.
func (r *Report) Field(section, name string) (Field, bool) {
	s, ok := r.Section(section)
	if !ok {
		return Field{}, false
	}
	return s.Field(name)
}

// Field devolve o primeiro campo da seção cujo nome normalizado é igual ao de
// name.
func (s *Section) Field(name string) (Field, bool) {
	key := NormalizeTitle(name)
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

func (r *Report) sectionsByKey(key string) []*Section {
	var out []*Section
	for i := range r.Sections {
		if r.Sections[i].Key == key {
			out = append(out, &r.Sections[i])
		}
	}
	return out
}

// validateSchema confere o esquema recebido. A configuração já valida parte
// dele no carregamento; aqui o report se protege de esquema zerado ou montado
// sem passar por ela.
func validateSchema(s config.ReportSchema) error {
	if s.SectionLevel < 1 || s.SectionLevel > 6 {
		return fmt.Errorf("section_level %d fora do intervalo 1 a 6", s.SectionLevel)
	}
	for i, k := range s.FrontRequired {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("front_required[%d] vazio", i)
		}
	}
	for i, t := range s.SectionsRequired {
		if NormalizeTitle(t) == "" {
			return fmt.Errorf("sections_required[%d] vazio", i)
		}
	}
	for sec, fields := range s.FieldsRequired {
		if NormalizeTitle(sec) == "" {
			return errors.New("fields_required: seção vazia")
		}
		for i, f := range fields {
			if NormalizeTitle(f) == "" {
				return fmt.Errorf("fields_required[%q][%d] vazio", sec, i)
			}
		}
	}
	for flag, fs := range s.FlagSections {
		if !flagRe.MatchString(flag) {
			return fmt.Errorf("flag_sections: chave %q inválida (formato ^require_[a-z_]+$)", flag)
		}
		if NormalizeTitle(fs.Section) == "" {
			return fmt.Errorf("flag_sections[%q]: section vazia", flag)
		}
		for i, f := range fs.Fields {
			if NormalizeTitle(f) == "" {
				return fmt.Errorf("flag_sections[%q].fields[%d] vazio", flag, i)
			}
		}
	}
	for flag, keys := range s.FlagFront {
		if !frontKeyRe.MatchString(flag) {
			return fmt.Errorf("flag_front: chave %q inválida (formato ^[a-z][a-z0-9_]*$)", flag)
		}
		if len(keys) == 0 {
			return fmt.Errorf("flag_front[%q] sem campos", flag)
		}
		for i, k := range keys {
			if !frontKeyRe.MatchString(k) {
				return fmt.Errorf("flag_front[%q][%d] %q inválido", flag, i, k)
			}
		}
	}
	if strings.TrimSpace(s.ChecklistSection) != "" && NormalizeTitle(s.ChecklistSection) == "" {
		return errors.New("checklist_section inválida")
	}
	for i, m := range s.ForbidMarkers {
		if m == "" || strings.TrimSpace(m) != m {
			return fmt.Errorf("forbid_markers[%d] %q vazio ou com espaço nas pontas", i, m)
		}
	}
	return nil
}

// Check confere o report contra o esquema e devolve os problemas encontrados
// (lista vazia = report aprovado). Erro, que o chamador deve tratar como
// reprovação, quando r é nil ou não veio de Parse, quando o esquema é inválido
// ou quando o report foi lido com nível de seção diferente do esquema.
//
// Checagens: front matter obrigatório presente e preenchido; flags com valor
// estrito; seções obrigatórias presentes, únicas e não vazias; campos
// obrigatórios com valor fora de placeholder_values e sem "<...>"; para cada
// flag true de flag_sections, a seção e os campos dela; para cada flag true de
// flag_front, os campos do front matter ligados a ela; checklist sem "- [ ]"
// e com ao menos um item; forbid_markers como palavra inteira fora de bloco de
// código, no front matter e no corpo.
func Check(r *Report, s config.ReportSchema) ([]Finding, error) {
	if r == nil || !r.parsed {
		return nil, errors.New("report: Check exige um report devolvido por Parse")
	}
	if err := validateSchema(s); err != nil {
		return nil, fmt.Errorf("report: esquema inválido: %w", err)
	}
	if r.Level != s.SectionLevel {
		return nil, fmt.Errorf("report: lido com nível de seção %d, esquema pede %d", r.Level, s.SectionLevel)
	}
	c := &checker{r: r, s: s, reported: map[string]bool{}}
	c.front()
	c.flags()
	c.flagFront()
	for _, title := range s.SectionsRequired {
		c.requireSection(title, "obrigatória")
	}
	for _, title := range sortedKeys(s.FieldsRequired) {
		if sec := c.section(title, "exigida por fields_required"); sec != nil {
			c.requireFields(sec, title, s.FieldsRequired[title])
		}
	}
	for _, flag := range sortedKeys(s.FlagSections) {
		if on, err := r.Flag(flag); err != nil || !on {
			continue // valor inválido já reprovado em flags
		}
		fs := s.FlagSections[flag]
		if sec := c.requireSection(fs.Section, "exigida por "+flag+"=true"); sec != nil {
			c.requireFields(sec, fs.Section, fs.Fields)
		}
	}
	c.checklist()
	c.markers()
	return c.out, nil
}

type checker struct {
	r        *Report
	s        config.ReportSchema
	out      []Finding
	reported map[string]bool // evita repetir ausência/duplicação da mesma seção
}

func (c *checker) errorf(format string, args ...any) {
	c.out = append(c.out, Finding{Level: LevelError, Message: fmt.Sprintf(format, args...)})
}

func (c *checker) front() {
	for _, k := range c.s.FrontRequired {
		key := strings.ToLower(strings.TrimSpace(k))
		v, ok := c.r.Front[key]
		switch {
		case !ok:
			c.errorf("front matter: campo obrigatório %q ausente", key)
		case c.isPlaceholder(v):
			c.errorf("front matter: campo obrigatório %q vazio ou com placeholder (%q)", key, v)
		case hasAnglePlaceholder(v):
			c.errorf("front matter: campo obrigatório %q com placeholder no formato <...> (%q)", key, v)
		}
	}
}

// flagFront exige, para cada flag true de flag_front, os campos do front
// matter ligados a ela, preenchidos (TD-30).
func (c *checker) flagFront() {
	for _, flag := range sortedKeys(c.s.FlagFront) {
		if on, err := c.r.Flag(flag); err != nil || !on {
			continue // valor inválido já reprovado em flags
		}
		for _, k := range c.s.FlagFront[flag] {
			v, ok := c.r.Front[k]
			switch {
			case !ok:
				c.errorf("front matter: campo %q obrigatório com %s=true ausente", k, flag)
			case c.isPlaceholder(v):
				c.errorf("front matter: campo %q obrigatório com %s=true vazio ou com placeholder (%q)", k, flag, v)
			case hasAnglePlaceholder(v):
				c.errorf("front matter: campo %q obrigatório com %s=true com placeholder no formato <...> (%q)", k, flag, v)
			}
		}
	}
}

// flags reprova valor não estrito em toda chave de flag do front matter
// (formato require_*) e em toda chave de flag_sections e flag_front presente.
func (c *checker) flags() {
	keys := sortedKeys(c.r.Front)
	for _, k := range keys {
		_, inSections := c.s.FlagSections[k]
		_, inFront := c.s.FlagFront[k]
		if !inSections && !inFront && !flagRe.MatchString(k) {
			continue
		}
		if _, err := c.r.Flag(k); err != nil {
			c.errorf("front matter: %v", err)
		}
	}
}

// section devolve a primeira seção com o título, reprovando ausência e
// repetição (uma vez por título).
func (c *checker) section(title, why string) *Section {
	key := NormalizeTitle(title)
	found := c.r.sectionsByKey(key)
	if c.reported[key] {
		if len(found) == 0 {
			return nil
		}
		return found[0]
	}
	switch {
	case len(found) == 0:
		c.reported[key] = true
		c.errorf("seção %q ausente (%s)", title, why)
		return nil
	case len(found) > 1:
		c.reported[key] = true
		lines := make([]string, len(found))
		for i, f := range found {
			lines[i] = fmt.Sprint(f.Line)
		}
		c.errorf("seção %q duplicada (linhas %s)", title, strings.Join(lines, ", "))
	}
	return found[0]
}

func (c *checker) requireSection(title, why string) *Section {
	sec := c.section(title, why)
	if sec != nil && sec.Body == "" && !c.reported["vazia:"+sec.Key] {
		c.reported["vazia:"+sec.Key] = true
		c.errorf("seção %q vazia (%s)", title, why)
	}
	return sec
}

func (c *checker) requireFields(sec *Section, title string, fields []string) {
	for _, name := range fields {
		key := NormalizeTitle(name)
		if c.reported["campo:"+sec.Key+"/"+key] {
			continue
		}
		c.reported["campo:"+sec.Key+"/"+key] = true
		var n int
		for _, f := range sec.Fields {
			if f.Key != key {
				continue
			}
			n++
			switch {
			case c.isPlaceholder(f.Value):
				c.errorf("seção %q: campo obrigatório %q vazio ou com placeholder (linha %d)", title, name, f.Line)
			case hasAnglePlaceholder(f.Value):
				c.errorf("seção %q: campo obrigatório %q com placeholder no formato <...> (linha %d)", title, name, f.Line)
			}
		}
		if n == 0 {
			c.errorf("seção %q: campo obrigatório %q ausente", title, name)
		}
	}
}

func (c *checker) checklist() {
	title := c.s.ChecklistSection
	if strings.TrimSpace(title) == "" {
		return
	}
	sec := c.section(title, "seção de checklist")
	if sec == nil {
		return
	}
	var total int
	for _, l := range sec.lines {
		if l.code {
			continue
		}
		t := strings.TrimSpace(l.text)
		if len(t) < 5 || !strings.ContainsRune("-*+", rune(t[0])) || t[1] != ' ' {
			continue
		}
		switch t[2:5] {
		case "[ ]":
			total++
			c.errorf("checklist %q: item não concluído (linha %d): %s", title, l.num, strings.TrimSpace(t[5:]))
		case "[x]", "[X]":
			total++
		}
	}
	if total == 0 {
		c.errorf("checklist %q não contém nenhum item", title)
	}
}

func (c *checker) markers() {
	for _, l := range c.r.frontLines {
		for _, m := range c.s.ForbidMarkers {
			if containsWord(l.text, m) {
				c.errorf("front matter: marcador %q na linha %d", m, l.num)
			}
		}
	}
	for _, l := range c.r.body {
		if l.code {
			continue
		}
		for _, m := range c.s.ForbidMarkers {
			if containsWord(l.text, m) {
				c.errorf("marcador %q na linha %d", m, l.num)
			}
		}
	}
}

// isPlaceholder diz se o valor está vazio ou é um dos placeholder_values,
// comparando sem diferenciar caixa e sem espaço algum.
func (c *checker) isPlaceholder(v string) bool {
	norm := squash(v)
	if norm == "" {
		return true
	}
	for _, p := range c.s.PlaceholderValues {
		if squash(p) == norm {
			return true
		}
	}
	return false
}

func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// hasAnglePlaceholder diz se alguma linha do valor é um placeholder "<...>".
func hasAnglePlaceholder(v string) bool {
	for _, l := range strings.Split(v, "\n") {
		l = strings.TrimSpace(l)
		if len(l) >= 2 && l[0] == '<' && l[len(l)-1] == '>' {
			return true
		}
	}
	return false
}

// containsWord diz se word aparece em text como palavra inteira: sem letra,
// dígito ou "_" colado antes ou depois. Diferencia caixa: "todo" em português
// não é o marcador "TODO".
func containsWord(text, word string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(word)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if !isWordRune(before) && !isWordRune(after) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		from = start + size
	}
}

func isWordRune(r rune) bool {
	return r != utf8.RuneError && (r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
