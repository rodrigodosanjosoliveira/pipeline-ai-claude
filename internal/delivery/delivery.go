// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/mcp/scaffold.go @ 0eac73a (MIT).
// Alterações: caminhos, prefixos, padrão de ID e esquema do report vêm da config
// em vez de fixos (TD-12); ID <prefixo>-<NNN> pelo maior número no índice e em
// deliveries_dir, conferido contra delivery_id_pattern (TD-20); nada é
// sobrescrito: Mkdir e report com O_EXCL, índice por temporário + rename com
// permissão e fim de linha preservados, rollback do diretório se o índice
// falhar (TD-20); escrita confinada à raiz, inclusive contra symlink (TD-17);
// título validado; linha nova no fim da primeira tabela com coluna de ID
// (tabela antes dela sem ID, como a de prefixos, é pulada), colunas pelo
// cabeçalho; data injetada; células em comentário contam para o número;
// escopo e flags da triagem gravados no report na criação, porque o pm-triage
// não tem ferramenta de escrita (TASK-053).

// Package delivery cria deliveries: o diretório, o report inicial e a linha no
// índice, com o próximo ID da sequência do prefixo (TD-20).
//
// A criação nunca sobrescreve nada e nunca grava fora da raiz do projeto
// (TD-17). Na dúvida (índice ilegível, configuração incoerente, caminho fora da
// raiz), falha antes de gravar.
package delivery

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/report"
)

// MaxTitleLen é o comprimento máximo do título, em caracteres.
const MaxTitleLen = 120

// ReportFile é o nome do report dentro do diretório da delivery, o mesmo que o
// validador procura.
const ReportFile = "report.md"

// MaxScopeLen é o comprimento máximo do escopo, em caracteres.
const MaxScopeLen = 500

// scopeTag é o marcador do escopo no report_template, trocado pelo escopo
// informado (ou por placeholder, se não houver).
const scopeTag = "<escopo>"

// placeholder é o valor deixado nos campos do report gerado do esquema. O
// formato <...> reprova no validador até ser preenchido.
const placeholder = "<preencher>"

// NewOptions são os parâmetros de New.
type NewOptions struct {
	// Title é o título da delivery: obrigatório, uma linha, até MaxTitleLen
	// caracteres, sem "|" nem caractere de controle.
	Title string
	// Prefix é o prefixo do ID, um de delivery_prefixes. Vazio vale o único
	// prefixo configurado, se houver só um.
	Prefix string
	// Scope é o escopo fechado da triagem, gravado no lugar de <escopo> do
	// report_template. Opcional; uma linha, até MaxScopeLen caracteres. Se
	// informado, o report_template precisa ter <escopo>: nada é descartado em
	// silêncio.
	Scope string
	// Flags são as flags de report_schema.flag_sections a ligar (por exemplo
	// require_security): a chave vai a true no front matter e a seção que ela
	// exige é acrescentada, com os campos em placeholder, se o report ainda
	// não a tiver.
	Flags []string
}

// Created descreve a delivery criada. Caminhos relativos à raiz do projeto,
// com "/" como separador.
type Created struct {
	// ID é o delivery_id gerado.
	ID string
	// ReportPath é o report criado.
	ReportPath string
	// IndexPath é o índice atualizado (index_file).
	IndexPath string
}

// New cria a próxima delivery do prefixo: o diretório <deliveries_dir>/<ID>,
// o report e a linha no índice. now dá a data gravada (2006-01-02).
//
// Nada existente é sobrescrito: o diretório é criado com os.Mkdir, o report em
// modo exclusivo e o índice substituído por rename de um temporário, depois de
// conferir que ele não mudou desde a leitura. Se o report ou o índice falhar,
// o que foi criado é removido e o erro é devolvido.
func New(cfg *config.Config, opts NewOptions, now time.Time) (Created, error) {
	p, err := prepare(cfg, opts, now)
	if err != nil {
		return Created{}, err
	}
	if err := p.apply(); err != nil {
		return Created{}, err
	}
	return p.created, nil
}

// plan é uma criação calculada e ainda não gravada.
type plan struct {
	created    Created
	dir        string // diretório da delivery, absoluto
	reportPath string // absoluto
	report     []byte
	indexPath  string // absoluto
	indexOld   []byte
	indexNew   []byte
	indexMode  os.FileMode
}

// prepare valida a entrada, lê o índice e deliveries_dir e monta o plano, sem
// gravar nada.
func prepare(cfg *config.Config, opts NewOptions, now time.Time) (*plan, error) {
	if cfg == nil || cfg.Root() == "" {
		return nil, errors.New("delivery: configuração não carregada")
	}
	if cfg.ReportSchema == nil {
		return nil, errors.New("delivery: configuração sem report_schema")
	}
	title, err := checkTitle(opts.Title)
	if err != nil {
		return nil, err
	}
	prefix, err := choosePrefix(cfg.DeliveryPrefixes, opts.Prefix)
	if err != nil {
		return nil, err
	}
	scope, err := checkScope(opts.Scope)
	if err != nil {
		return nil, err
	}
	flags, err := checkFlags(cfg.ReportSchema, opts.Flags)
	if err != nil {
		return nil, err
	}

	root := filepath.Clean(cfg.Root())
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("delivery: raiz do projeto %s: %w", root, err)
	}

	indexPath, err := confine(root, cfg.IndexFile)
	if err != nil {
		return nil, fmt.Errorf("delivery: index_file: %w", err)
	}
	if _, err := os.Lstat(indexPath); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("delivery: índice %s não existe: crie-o com uma tabela com cabeçalho", cfg.IndexFile)
	}
	if err := insideReal(realRoot, indexPath); err != nil {
		return nil, fmt.Errorf("delivery: índice %s: %w", cfg.IndexFile, err)
	}
	info, err := os.Lstat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("delivery: índice %s: %w", cfg.IndexFile, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("delivery: índice %s não é um arquivo regular", cfg.IndexFile)
	}
	indexOld, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("delivery: falha ao ler o índice %s: %w", cfg.IndexFile, err)
	}
	idx, err := parseIndex(string(indexOld))
	if err != nil {
		return nil, fmt.Errorf("delivery: índice %s: %w", cfg.IndexFile, err)
	}

	deliveriesDir, err := confine(root, cfg.DeliveriesDir)
	if err != nil {
		return nil, fmt.Errorf("delivery: deliveries_dir: %w", err)
	}
	if err := insideReal(realRoot, deliveriesDir); err != nil {
		return nil, fmt.Errorf("delivery: deliveries_dir %s: %w", cfg.DeliveriesDir, err)
	}
	entries, err := os.ReadDir(deliveriesDir)
	if err != nil {
		return nil, fmt.Errorf("delivery: deliveries_dir %s: %w", cfg.DeliveriesDir, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}

	n, err := nextNumber(prefix, idx.cells, names)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%s-%03d", prefix, n)
	if err := checkID(id, cfg.DeliveryIDPattern); err != nil {
		return nil, err
	}

	reportRel := path.Join(cfg.DeliveriesDir, id, ReportFile)
	dir, err := confine(root, path.Join(cfg.DeliveriesDir, id))
	if err != nil {
		return nil, fmt.Errorf("delivery: diretório da delivery: %w", err)
	}
	reportPath, err := confine(root, reportRel)
	if err != nil {
		return nil, fmt.Errorf("delivery: report: %w", err)
	}

	date := now.Format("2006-01-02")
	content, err := renderReport(cfg, root, id, title, date, scope)
	if err != nil {
		return nil, err
	}
	if content, err = applyFlags(content, cfg.ReportSchema, flags); err != nil {
		return nil, err
	}
	row, err := idx.row(id, title, date)
	if err != nil {
		return nil, fmt.Errorf("delivery: índice %s: %w", cfg.IndexFile, err)
	}

	return &plan{
		created:    Created{ID: id, ReportPath: reportRel, IndexPath: cfg.IndexFile},
		dir:        dir,
		reportPath: reportPath,
		report:     []byte(content),
		indexPath:  indexPath,
		indexOld:   indexOld,
		indexNew:   []byte(idx.insert(row)),
		indexMode:  info.Mode().Perm(),
	}, nil
}

// testHookBeforeRename, se não nil, roda antes do rename do índice (testes).
var testHookBeforeRename func()

// apply grava o plano: diretório, report e índice, nessa ordem. Em falha
// depois do diretório criado, desfaz a criação.
func (p *plan) apply() error {
	c := p.created
	if err := os.Mkdir(p.dir, 0o755); err != nil {
		return fmt.Errorf("delivery: falha ao criar o diretório de %s (nada foi alterado): %w", c.ID, err)
	}
	rollback := func(cause error, reportCreated bool) error {
		var errs []error
		if reportCreated {
			if err := os.Remove(p.reportPath); err != nil {
				errs = append(errs, fmt.Errorf("falha ao remover %s: %w", c.ReportPath, err))
			}
		}
		if err := os.Remove(p.dir); err != nil {
			errs = append(errs, fmt.Errorf("falha ao remover o diretório de %s: %w", c.ID, err))
		}
		if len(errs) > 0 {
			return errors.Join(append([]error{cause, errors.New("delivery: rollback incompleto")}, errs...)...)
		}
		return cause
	}

	f, err := os.OpenFile(p.reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return rollback(fmt.Errorf("delivery: falha ao criar %s: %w", c.ReportPath, err), false)
	}
	_, werr := f.Write(p.report)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return rollback(fmt.Errorf("delivery: falha ao gravar %s: %w", c.ReportPath, err), true)
	}

	if err := replaceIndex(p.indexPath, p.indexOld, p.indexNew, p.indexMode); err != nil {
		return rollback(fmt.Errorf("delivery: índice %s: %w", c.IndexPath, err), true)
	}
	return nil
}

// replaceIndex troca o conteúdo do índice por data via temporário no mesmo
// diretório + rename, com a permissão mode. Falha, sem tocar no índice, se
// ele não for mais igual a old.
func replaceIndex(indexPath string, old, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(indexPath), "."+filepath.Base(indexPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("falha ao criar temporário: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			if rerr := os.Remove(tmpName); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("falha ao remover o temporário %s: %w", tmpName, rerr))
			}
		}
	}()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(mode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if err := errors.Join(werr, tmp.Close()); err != nil {
		return fmt.Errorf("falha ao gravar temporário: %w", err)
	}
	if testHookBeforeRename != nil {
		testHookBeforeRename()
	}
	cur, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("falha ao reler: %w", err)
	}
	if !bytes.Equal(cur, old) {
		return errors.New("o índice mudou durante a criação (outra delivery criada ao mesmo tempo?); nada foi gravado, tente de novo")
	}
	if err := os.Rename(tmpName, indexPath); err != nil {
		return fmt.Errorf("falha ao substituir: %w", err)
	}
	return nil
}

// checkTitle valida o título e o devolve sem espaços nas pontas.
func checkTitle(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("delivery: título com UTF-8 inválido")
	}
	if strings.ContainsAny(s, "\r\n") {
		return "", errors.New("delivery: o título precisa ter uma linha só")
	}
	t := strings.TrimSpace(s)
	switch {
	case t == "":
		return "", errors.New("delivery: título é obrigatório")
	case strings.Contains(t, "|"):
		return "", errors.New(`delivery: o título não pode ter "|" (quebraria a tabela do índice)`)
	case strings.ContainsFunc(t, unicode.IsControl):
		return "", errors.New("delivery: o título não pode ter caractere de controle")
	case utf8.RuneCountInString(t) > MaxTitleLen:
		return "", fmt.Errorf("delivery: título com %d caracteres; o máximo é %d", utf8.RuneCountInString(t), MaxTitleLen)
	case strings.Contains(t, "<!--") || strings.Contains(t, "-->"):
		return "", errors.New("delivery: o título não pode abrir nem fechar comentário HTML")
	}
	return t, nil
}

// choosePrefix escolhe o prefixo: o informado, que precisa estar em
// configured (diferenciando caixa), ou o único configurado.
func choosePrefix(configured []string, prefix string) (string, error) {
	if prefix == "" {
		switch len(configured) {
		case 0:
			return "", errors.New("delivery: delivery_prefixes está vazio na configuração: configure os prefixos aceitos")
		case 1:
			return configured[0], nil
		}
		return "", fmt.Errorf("delivery: informe o prefixo (um de %s)", strings.Join(configured, ", "))
	}
	for _, p := range configured {
		if p == prefix {
			return p, nil
		}
	}
	if len(configured) == 0 {
		return "", fmt.Errorf("delivery: prefixo %q não está em delivery_prefixes (vazio na configuração)", prefix)
	}
	return "", fmt.Errorf("delivery: prefixo %q não está em delivery_prefixes (%s)", prefix, strings.Join(configured, ", "))
}

// checkID confere que o ID gerado serve de nome de diretório e de célula do
// índice e casa delivery_id_pattern, se configurado.
func checkID(id, pattern string) error {
	if strings.ContainsAny(id, `/\|:`) || strings.ContainsFunc(id, unicode.IsControl) ||
		strings.Contains(id, "<!--") || strings.Contains(id, "-->") || id != strings.TrimSpace(id) {
		return fmt.Errorf("delivery: ID gerado %q inválido como nome de diretório ou célula do índice: revise delivery_prefixes", id)
	}
	if pattern == "" {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("delivery: delivery_id_pattern %q: %w", pattern, err)
	}
	if !re.MatchString(id) {
		return fmt.Errorf("delivery: ID gerado %q não casa delivery_id_pattern %q: configuração incoerente (delivery_prefixes × delivery_id_pattern)", id, pattern)
	}
	return nil
}

// nextNumber devolve o maior N entre os IDs <prefix>-<N> de cells e names,
// mais um. Comparação do prefixo sensível a caixa. Número que não cabe em int
// é erro: a sequência não pode continuar com garantia.
func nextNumber(prefix string, cells, names []string) (int, error) {
	re := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + "-([0-9]+)$")
	highest := 0
	for _, list := range [][]string{cells, names} {
		for _, s := range list {
			m := re.FindStringSubmatch(s)
			if m == nil {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err != nil || n == math.MaxInt {
				return 0, fmt.Errorf("delivery: %q tem número grande demais para continuar a sequência", s)
			}
			highest = max(highest, n)
		}
	}
	return highest + 1, nil
}

// confine junta rel (relativo à raiz, "/" como separador) a root e confere,
// depois de filepath.Clean, que o resultado fica estritamente dentro de root
// (TD-17).
func confine(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("caminho vazio")
	}
	if filepath.IsAbs(rel) || path.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("%q é absoluto: fora da raiz do projeto", rel)
	}
	root = filepath.Clean(root)
	p := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !strictlyInside(root, p) {
		return "", fmt.Errorf("%q fica fora da raiz do projeto", rel)
	}
	return p, nil
}

// insideReal confere que p, com os symlinks resolvidos, continua dentro da
// raiz real do projeto.
func insideReal(realRoot, p string) error {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return err
	}
	if !strictlyInside(realRoot, real) {
		return fmt.Errorf("resolve para %s, fora da raiz do projeto", real)
	}
	return nil
}

func strictlyInside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// renderReport monta o report: do report_template, se configurado, ou do
// report_schema. scope, se não vazio, exige o template com <escopo>.
func renderReport(cfg *config.Config, root, id, title, date, scope string) (string, error) {
	if cfg.ReportTemplate == "" {
		if scope != "" {
			return "", errors.New("delivery: o escopo precisa de report_template com " + scopeTag + ": o report do esquema não tem onde gravá-lo")
		}
		return schemaReport(cfg.ReportSchema, id, title), nil
	}
	p, err := confine(root, cfg.ReportTemplate)
	if err != nil {
		return "", fmt.Errorf("delivery: report_template: %w", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("delivery: report_template %s: %w", cfg.ReportTemplate, err)
	}
	if scope != "" && !strings.Contains(string(data), scopeTag) {
		return "", fmt.Errorf("delivery: report_template %s não tem %s: acrescente o marcador onde o escopo deve entrar (por exemplo em \"- Escopo fechado: %s\") ou crie a delivery sem escopo", cfg.ReportTemplate, scopeTag, scopeTag)
	}
	if scope == "" {
		scope = placeholder
	}
	r := strings.NewReplacer("<delivery_id>", id, "<titulo>", title, "<data>", date, scopeTag, scope)
	return r.Replace(string(data)), nil
}

// checkScope valida o escopo e o devolve sem espaços nas pontas; vazio vale
// "sem escopo".
func checkScope(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("delivery: escopo com UTF-8 inválido")
	}
	if strings.ContainsAny(s, "\r\n") {
		return "", errors.New("delivery: o escopo precisa ter uma linha só")
	}
	t := strings.TrimSpace(s)
	switch {
	case strings.ContainsFunc(t, unicode.IsControl):
		return "", errors.New("delivery: o escopo não pode ter caractere de controle")
	case utf8.RuneCountInString(t) > MaxScopeLen:
		return "", fmt.Errorf("delivery: escopo com %d caracteres; o máximo é %d", utf8.RuneCountInString(t), MaxScopeLen)
	case strings.Contains(t, "<!--") || strings.Contains(t, "-->"):
		return "", errors.New("delivery: o escopo não pode abrir nem fechar comentário HTML")
	}
	return t, nil
}

// checkFlags confere que cada flag é uma chave de flag_sections e devolve a
// lista sem repetição, na ordem recebida.
func checkFlags(s *config.ReportSchema, flags []string) ([]string, error) {
	var out []string
	for _, f := range flags {
		f = strings.TrimSpace(f)
		if f == "" {
			return nil, errors.New("delivery: flag vazia")
		}
		if _, ok := s.FlagSections[f]; !ok {
			valid := sortedKeys(s.FlagSections)
			if len(valid) == 0 {
				return nil, fmt.Errorf("delivery: flag %q desconhecida: report_schema.flag_sections está vazio", f)
			}
			return nil, fmt.Errorf("delivery: flag %q desconhecida; use uma de %s", f, strings.Join(valid, ", "))
		}
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// applyFlags liga cada flag no front matter do report e acrescenta ao fim a
// seção que ela exige, se o report ainda não a tiver (comentário HTML e bloco
// de código não contam como seção). Sem front matter, ou com a flag num valor
// que não é true nem false, é erro.
func applyFlags(content string, s *config.ReportSchema, flags []string) (string, error) {
	if len(flags) == 0 {
		return content, nil
	}
	lines := strings.SplitAfter(content, "\n")
	if n := len(lines); lines[n-1] == "" {
		lines = lines[:n-1]
	}
	eol := "\n"
	if len(lines) > 0 && strings.HasSuffix(lines[0], "\r\n") {
		eol = "\r\n"
	}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", errors.New("delivery: o report não tem front matter (--- na primeira linha) para ligar as flags")
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		return "", errors.New("delivery: o front matter do report não fecha (--- ausente) para ligar as flags")
	}
	for _, f := range flags {
		found := false
		for i := 1; i < closing; i++ {
			key, value, ok := strings.Cut(lines[i], ":")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), f) {
				continue
			}
			found = true
			switch strings.TrimSpace(value) {
			case "false":
				lines[i] = f + ": true" + eol
			case "true":
			default:
				return "", fmt.Errorf("delivery: a flag %s do front matter vale %q; esperava true ou false", f, strings.TrimSpace(value))
			}
			break
		}
		if !found {
			lines = slices.Insert(lines, closing, f+": true"+eol)
			closing++
		}
	}

	heading := strings.Repeat("#", s.SectionLevel) + " "
	var b strings.Builder
	b.WriteString(strings.Join(lines, ""))
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		b.WriteString(eol)
	}
	var added []string
	for _, f := range flags {
		fs := s.FlagSections[f]
		if hasSection(strings.Join(lines, ""), heading, fs.Section) {
			continue
		}
		b.WriteString(eol + heading + strings.TrimSpace(fs.Section) + eol)
		for _, field := range fs.Fields {
			b.WriteString("- " + strings.TrimSpace(field) + ": " + placeholder + eol)
		}
		added = append(added, fs.Section)
	}
	out := b.String()
	// Comentário ou bloco de código sem fechar engole o que vem depois: a seção
	// acrescentada não contaria para o validador.
	for _, sec := range added {
		if !hasSection(out, heading, sec) {
			return "", fmt.Errorf("delivery: a seção %q não pôde ser acrescentada ao fim do report: ele termina dentro de um comentário HTML ou bloco de código sem fechar", sec)
		}
	}
	return out, nil
}

// hasSection diz se content tem um título heading+title fora de comentário
// HTML e de bloco de código.
func hasSection(content, heading, title string) bool {
	lines := strings.SplitAfter(content, "\n")
	hidden := hiddenLines(lines)
	want := report.NormalizeTitle(title)
	for i, l := range lines {
		if hidden[i] {
			continue
		}
		if rest, ok := strings.CutPrefix(strings.TrimRight(l, "\r\n"), heading); ok && report.NormalizeTitle(rest) == want {
			return true
		}
	}
	return false
}

// schemaReport gera o report do esquema: front matter com delivery_id e
// status: draft, as chaves de front_required vazias e as flags de
// flag_sections false; cada seção exigida com os campos em <preencher>; a
// seção de checklist com um item aberto.
func schemaReport(s *config.ReportSchema, id, title string) string {
	var b strings.Builder
	b.WriteString("---\n")
	seen := map[string]bool{}
	front := func(key, value string) {
		key = strings.TrimSpace(key)
		lower := strings.ToLower(key)
		if seen[lower] {
			return
		}
		seen[lower] = true
		if value == "" {
			b.WriteString(key + ":\n")
			return
		}
		b.WriteString(key + ": " + value + "\n")
	}
	flags := sortedKeys(s.FlagSections)
	isFlag := func(k string) bool {
		_, ok := s.FlagSections[strings.ToLower(strings.TrimSpace(k))]
		return ok
	}
	front("delivery_id", id)
	front("status", "draft")
	for _, k := range s.FrontRequired {
		if isFlag(k) {
			front(k, "false")
		} else {
			front(k, "")
		}
	}
	for _, k := range flags {
		front(k, "false")
	}
	b.WriteString("---\n")

	if s.SectionLevel > 1 {
		b.WriteString("\n" + strings.Repeat("#", s.SectionLevel-1) + " " + id + " — " + title + "\n")
	}

	// Seções na ordem: sections_required, as demais de fields_required em
	// ordem alfabética e a de checklist, sem repetir título normalizado.
	var order []string
	have := map[string]bool{}
	add := func(t string) {
		k := report.NormalizeTitle(t)
		if k == "" || have[k] {
			return
		}
		have[k] = true
		order = append(order, t)
	}
	for _, t := range s.SectionsRequired {
		add(t)
	}
	for _, t := range sortedKeys(s.FieldsRequired) {
		add(t)
	}
	add(s.ChecklistSection)

	heading := strings.Repeat("#", s.SectionLevel) + " "
	checklist := report.NormalizeTitle(s.ChecklistSection)
	for _, t := range order {
		key := report.NormalizeTitle(t)
		b.WriteString("\n" + heading + strings.TrimSpace(t) + "\n")
		for _, sec := range sortedKeys(s.FieldsRequired) {
			if report.NormalizeTitle(sec) != key {
				continue
			}
			for _, f := range s.FieldsRequired[sec] {
				b.WriteString("- " + strings.TrimSpace(f) + ": " + placeholder + "\n")
			}
		}
		if key == checklist {
			b.WriteString("- [ ] " + placeholder + "\n")
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// index é o índice de deliveries lido para receber uma linha.
type index struct {
	lines  []string // linhas com o terminador
	eol    string   // "\n" ou "\r\n", do original
	header []string // títulos das colunas da tabela de registro, normalizados
	last   int      // última linha da tabela de registro
	// cells é o conteúdo de toda célula de linha de tabela, em qualquer parte
	// do arquivo (inclusive comentário e código, como o validador enxerga),
	// sem link, crases e ênfase. Nunca contém célula vazia.
	cells []string
}

var (
	linkCellRe  = regexp.MustCompile(`^\[([^\]]*)\]\([^)]*\)$`)
	separatorRe = regexp.MustCompile(`^:?-+:?$`)
)

// parseIndex lê o índice. A tabela de registro é a primeira com cabeçalho
// (linha de títulos seguida de linha separadora) e coluna de ID, fora de
// comentário HTML e de bloco de código: uma tabela antes dela sem coluna de ID
// (de prefixos, por exemplo) é pulada. Erro se não houver tabela com
// cabeçalho ou se nenhuma tiver coluna de ID.
func parseIndex(content string) (*index, error) {
	lines := strings.SplitAfter(content, "\n")
	if n := len(lines); lines[n-1] == "" {
		lines = lines[:n-1]
	}
	idx := &index{lines: lines, eol: "\n", last: -1}
	if i := strings.IndexByte(content, '\n'); i > 0 && content[i-1] == '\r' {
		idx.eol = "\r\n"
	}

	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "|") {
			continue
		}
		for _, c := range splitCells(t) {
			if v := cellValue(c); v != "" {
				idx.cells = append(idx.cells, v)
			}
		}
	}

	hidden := hiddenLines(lines)
	isRow := func(i int) bool {
		return i < len(lines) && !hidden[i] && strings.HasPrefix(strings.TrimSpace(lines[i]), "|")
	}
	sawTable := false
	for i := 0; i+1 < len(lines); i++ {
		if !isRow(i) || !isRow(i+1) || !isSeparator(lines[i+1]) {
			continue
		}
		sawTable = true
		var header []string
		for _, c := range splitCells(strings.TrimSpace(lines[i])) {
			header = append(header, report.NormalizeTitle(cellValue(c)))
		}
		last := i + 1
		for isRow(last + 1) {
			last++
		}
		if !slices.ContainsFunc(header, isIDColumn) {
			i = last
			continue
		}
		idx.header, idx.last = header, last
		return idx, nil
	}
	if sawTable {
		return nil, errors.New("nenhuma tabela com coluna de ID (título id, delivery ou delivery_id) fora de comentário e de bloco de código")
	}
	return nil, errors.New("nenhuma tabela com cabeçalho (linha de títulos seguida de |---|) fora de comentário e de bloco de código")
}

// isIDColumn diz se o título normalizado é de coluna de ID.
func isIDColumn(h string) bool { return h == "id" || h == "delivery" || h == "delivery_id" }

// splitCells divide uma linha de tabela (sem espaços nas pontas) em células,
// como o validador: sem os "|" das pontas e sem escape.
func splitCells(line string) []string {
	return strings.Split(strings.Trim(line, "|"), "|")
}

// cellValue tira espaços, link [x](y), crases e ênfase de uma célula, como o
// validador faz ao procurar o ID.
func cellValue(cell string) string {
	cell = strings.TrimSpace(cell)
	if m := linkCellRe.FindStringSubmatch(cell); m != nil {
		cell = strings.TrimSpace(m[1])
	}
	return strings.Trim(cell, "`*_ ")
}

func isSeparator(line string) bool {
	cells := splitCells(strings.TrimSpace(line))
	for _, c := range cells {
		if !separatorRe.MatchString(strings.TrimSpace(c)) {
			return false
		}
	}
	return len(cells) > 0
}

// hiddenLines marca as linhas que não podem receber nem ser tabela: as que
// têm qualquer parte de comentário HTML e as de bloco de código cercado
// (inclusive as cercas).
func hiddenLines(lines []string) []bool {
	hidden := make([]bool, len(lines))
	var inComment, inFence bool
	var fenceChar byte
	var fenceLen int
	for i, l := range lines {
		text := strings.TrimRight(l, "\r\n")
		if inFence {
			hidden[i] = true
			if ch, n, rest := fenceOf(text); n >= fenceLen && ch == fenceChar && strings.TrimSpace(rest) == "" {
				inFence = false
			}
			continue
		}
		h := inComment
		for rest := text; ; {
			if inComment {
				j := strings.Index(rest, "-->")
				if j < 0 {
					break
				}
				rest, inComment = rest[j+3:], false
				continue
			}
			j := strings.Index(rest, "<!--")
			if j < 0 {
				break
			}
			rest, inComment, h = rest[j+4:], true, true
		}
		if h {
			hidden[i] = true
			continue
		}
		if ch, n, _ := fenceOf(text); n > 0 {
			hidden[i], inFence, fenceChar, fenceLen = true, true, ch, n
		}
	}
	return hidden
}

// fenceOf devolve o caractere e o comprimento da cerca de bloco de código no
// início da linha (0 se não houver) e o que vem depois dela. Mesma regra do
// pacote report.
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

// row monta a linha nova com o número de colunas do cabeçalho: id, delivery
// ou delivery_id recebem o ID; titulo ou title, o título; status, draft; data
// ou date, a data. Sem coluna de ID é erro.
func (idx *index) row(id, title, date string) (string, error) {
	cells := make([]string, len(idx.header))
	hasID := false
	for i, h := range idx.header {
		switch {
		case isIDColumn(h):
			cells[i], hasID = id, true
		case h == "titulo" || h == "title":
			cells[i] = title
		case h == "status":
			cells[i] = "draft"
		case h == "data" || h == "date":
			cells[i] = date
		}
	}
	if !hasID {
		return "", errors.New("a tabela de registro não tem coluna de ID (título id, delivery ou delivery_id)")
	}
	return "| " + strings.Join(cells, " | ") + " |", nil
}

// insert devolve o índice com row (sem terminador) depois da última linha da
// tabela de registro, com o fim de linha do original.
func (idx *index) insert(row string) string {
	var b strings.Builder
	for i, l := range idx.lines {
		b.WriteString(l)
		if i != idx.last {
			continue
		}
		if strings.HasSuffix(l, "\n") {
			b.WriteString(row + idx.eol)
		} else {
			b.WriteString(idx.eol + row)
		}
	}
	return b.String()
}
