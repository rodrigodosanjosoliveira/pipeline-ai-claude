// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/validate/validate.go @ 0eac73a (MIT).
// Alterações: alvo por ID da delivery ou caminho; checagens do report pelo
// report_schema da config (TD-12); delivery_id contra delivery_id_pattern e o
// diretório; ID procurado nas células da tabela do índice, não em qualquer
// texto; diff completo via gitdiff com flags de auto_flags e
// security_surface_paths (TD-11, TD-13), caminhos convertidos para a raiz do
// projeto; --no-git com aviso; saída --json; config ausente é uso inválido.

// Package validate confere o report de uma delivery: o conteúdo contra o
// esquema da configuração e a coerência com o repositório (ID, índice e as
// flags que o diff exige).
package validate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/gitdiff"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/report"
)

// Códigos de saída.
const (
	ExitOK       = 0
	ExitFail     = 1
	ExitBadUsage = 2
)

// maxFiles limita quantos arquivos uma mensagem cita.
const maxFiles = 10

// Options configura uma validação.
type Options struct {
	// Target é o ID da delivery (resolve <deliveries_dir>/<ID>/report.md) ou o
	// caminho do report. É caminho se tiver separador ou terminar em ".md".
	Target string
	// BaseRef substitui o base_ref da configuração.
	BaseRef string
	// NoGit pula as checagens que dependem do diff, com aviso.
	NoGit bool
	// JSON troca a saída humana por {"report", "ok", "findings"}.
	JSON bool
	// Getwd fornece o diretório atual; nil usa os.Getwd.
	Getwd func() (string, error)
}

// Run valida o report indicado em opts e devolve o código de saída: 0
// aprovado, 1 reprovado, 2 uso inválido (alvo inexistente, configuração
// ausente ou inválida). Erros de uso vão para stderr; o resultado, para stdout.
func Run(opts Options, stdout, stderr io.Writer) int {
	usage := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "pipeline-claude validate: "+format+"\n", args...)
		return ExitBadUsage
	}

	getwd := opts.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	wd, err := getwd()
	if err != nil {
		return usage("falha ao obter o diretório atual: %v", err)
	}
	target := opts.Target
	if target == "" {
		return usage("informe o alvo: ID da delivery ou caminho do report")
	}

	var cfg *config.Config
	var reportPath, dirName string
	if isPath(target) {
		if !filepath.IsAbs(target) {
			target = filepath.Join(wd, target)
		}
		reportPath = filepath.Clean(target)
		if cfg, err = config.Load(filepath.Dir(reportPath)); err != nil {
			return usage("%v", err)
		}
		dirName = filepath.Base(filepath.Dir(reportPath))
	} else {
		if target == "." || target == ".." || strings.ContainsRune(target, 0) {
			return usage("ID de delivery inválido: %q", target)
		}
		if cfg, err = config.Load(wd); err != nil {
			return usage("%v", err)
		}
		reportPath = filepath.Join(cfg.Root(), filepath.FromSlash(cfg.DeliveriesDir), target, "report.md")
		dirName = target
	}
	info, err := os.Stat(reportPath)
	if err != nil {
		return usage("report da delivery %q não encontrado: %v", opts.Target, err)
	}
	if !info.Mode().IsRegular() {
		return usage("%s não é um arquivo: informe o report de %q", reportPath, opts.Target)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return usage("falha ao ler %s: %v", reportPath, err)
	}

	display := reportPath
	if rel, err := filepath.Rel(cfg.Root(), reportPath); err == nil && !strings.HasPrefix(rel, "..") {
		display = filepath.ToSlash(rel)
	}

	findings, err := check(cfg, data, dirName, opts)
	if err != nil {
		return usage("%v", err)
	}
	return output(stdout, display, findings, opts.JSON)
}

// isPath diz se o alvo é caminho (e não ID).
func isPath(target string) bool {
	return strings.ContainsAny(target, `/\`) || strings.HasSuffix(strings.ToLower(target), ".md")
}

// check reúne as checagens. Erro só para configuração incompatível com o
// report (uso inválido); problemas no report viram findings.
func check(cfg *config.Config, data []byte, dirName string, opts Options) ([]report.Finding, error) {
	schema := *cfg.ReportSchema
	r, err := report.Parse(data, schema.SectionLevel)
	if err != nil {
		return []report.Finding{errorf("%v", err)}, nil
	}
	findings, err := report.Check(r, schema)
	if err != nil {
		return nil, fmt.Errorf("configuração incompatível com o report: %w", err)
	}
	findings = append(findings, checkID(cfg, r, dirName)...)

	if opts.NoGit {
		findings = append(findings, report.Finding{Level: report.LevelWarning,
			Message: "--no-git: diff não verificado; flags exigidas por auto_flags e security_surface_paths não foram conferidas"})
		return findings, nil
	}
	base := opts.BaseRef
	if base == "" {
		base = cfg.BaseRef
	}
	return append(findings, checkDiff(cfg, r, base)...), nil
}

func errorf(format string, args ...any) report.Finding {
	return report.Finding{Level: report.LevelError, Message: fmt.Sprintf(format, args...)}
}

// checkID confere o delivery_id contra o padrão, o diretório e o índice.
func checkID(cfg *config.Config, r *report.Report, dirName string) []report.Finding {
	id, ok := r.Front["delivery_id"]
	if !ok || id == "" {
		return nil // ausência já reprovada pelo front_required
	}
	var out []report.Finding
	if cfg.DeliveryIDPattern == "" {
		out = append(out, report.Finding{Level: report.LevelWarning,
			Message: "delivery_id_pattern não configurado: formato do delivery_id não verificado"})
	} else if re, err := regexp.Compile(cfg.DeliveryIDPattern); err != nil || !re.MatchString(id) {
		out = append(out, errorf("delivery_id %q não casa delivery_id_pattern %q", id, cfg.DeliveryIDPattern))
	}
	if id != dirName {
		out = append(out, errorf("delivery_id %q diferente do diretório do report (%q)", id, dirName))
	}

	indexPath := filepath.Join(cfg.Root(), filepath.FromSlash(cfg.IndexFile))
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return append(out, errorf("índice %s ilegível: %v", cfg.IndexFile, err))
	}
	if !inIndexTable(string(data), id) {
		out = append(out, errorf("delivery %q não está registrada na tabela de %s", id, cfg.IndexFile))
	}
	return out
}

var linkCellRe = regexp.MustCompile(`^\[([^\]]*)\]\([^)]*\)$`)

// inIndexTable diz se alguma célula de linha de tabela Markdown do índice é
// exatamente id, aceitando `id`, **id** e [id](link).
func inIndexTable(index, id string) bool {
	for _, line := range strings.Split(index, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		for _, cell := range strings.Split(strings.Trim(line, "|"), "|") {
			cell = strings.TrimSpace(cell)
			if m := linkCellRe.FindStringSubmatch(cell); m != nil {
				cell = strings.TrimSpace(m[1])
			}
			cell = strings.Trim(cell, "`*_ ")
			if cell == id {
				return true
			}
		}
	}
	return false
}

// checkDiff exige true nas flags disparadas pelos arquivos do diff (TD-13).
func checkDiff(cfg *config.Config, r *report.Report, base string) []report.Finding {
	hint := "corrija o repositório ou rode com --no-git para pular a checagem do diff"
	ctx := context.Background()
	changed, err := gitdiff.ChangedFiles(ctx, cfg.Root(), base)
	if err != nil {
		return []report.Finding{errorf("não foi possível calcular o diff contra %q: %v (%s)", base, err, hint)}
	}
	prefix, err := repoPrefix(ctx, cfg.Root())
	if err != nil {
		return []report.Finding{errorf("não foi possível localizar o projeto no repositório: %v (%s)", err, hint)}
	}

	byFlag := map[string]map[string]bool{}
	add := func(flag, file string) {
		if byFlag[flag] == nil {
			byFlag[flag] = map[string]bool{}
		}
		byFlag[flag][file] = true
	}
	for _, f := range changed {
		// gitdiff devolve caminhos relativos à raiz do repositório; os padrões
		// são relativos à raiz do projeto.
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		rel := f[len(prefix):]
		for _, flag := range cfg.FlagsTriggeredBy(rel) {
			add(flag, rel)
		}
		if cfg.IsSecuritySurface(rel) {
			add("require_security", rel)
		}
	}

	flags := make([]string, 0, len(byFlag))
	for flag := range byFlag {
		flags = append(flags, flag)
	}
	sort.Strings(flags)
	var out []report.Finding
	for _, flag := range flags {
		on, err := r.Flag(flag)
		if err != nil || on {
			continue // valor inválido já reprovado por report.Check
		}
		files := make([]string, 0, len(byFlag[flag]))
		for f := range byFlag[flag] {
			files = append(files, f)
		}
		sort.Strings(files)
		list := strings.Join(files, ", ")
		if len(files) > maxFiles {
			list = strings.Join(files[:maxFiles], ", ") + fmt.Sprintf(" e mais %d", len(files)-maxFiles)
		}
		state := "ausente"
		if _, present := r.Front[flag]; present {
			state = "false"
		}
		out = append(out, errorf("o diff toca %s, que exige %s, mas a flag está %s no front matter: ligue a flag e preencha a seção exigida", list, flag, state))
	}
	return out
}

// repoPrefix devolve o caminho da raiz do projeto relativo à raiz do
// repositório git, com "/" final ("" se forem a mesma).
func repoPrefix(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.quotepath=off", "rev-parse", "--show-prefix")
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git rev-parse --show-prefix: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git rev-parse --show-prefix: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func output(stdout io.Writer, display string, findings []report.Finding, asJSON bool) int {
	var errs, warns int
	for _, f := range findings {
		if f.Level == report.LevelError {
			errs++
		} else {
			warns++
		}
	}
	code := ExitOK
	if errs > 0 {
		code = ExitFail
	}

	if asJSON {
		if findings == nil {
			findings = []report.Finding{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		rep := struct {
			Report   string           `json:"report"`
			OK       bool             `json:"ok"`
			Findings []report.Finding `json:"findings"`
		}{display, errs == 0, findings}
		if err := enc.Encode(rep); err != nil {
			return ExitFail
		}
		return code
	}

	fmt.Fprintf(stdout, "pipeline-claude validate: %s\n", display)
	for _, f := range findings {
		fmt.Fprintf(stdout, "  %s\n", f)
	}
	fmt.Fprintf(stdout, "\n%d erro(s), %d aviso(s)\n", errs, warns)
	switch {
	case errs > 0:
		fmt.Fprintln(stdout, "REPROVADO")
	case warns > 0:
		fmt.Fprintln(stdout, "APROVADO com avisos")
	default:
		fmt.Fprintln(stdout, "APROVADO")
	}
	return code
}
