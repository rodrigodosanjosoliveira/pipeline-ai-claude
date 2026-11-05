package delivery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// HandoffsDir é a pasta dos handoffs dentro do diretório da delivery (TD-41).
const HandoffsDir = "handoffs"

// MaxHandoffSize é o tamanho máximo de um handoff, em bytes.
const MaxHandoffSize = 64 << 10

// maxHandoffs é o maior número de handoff (NN de dois dígitos).
const maxHandoffs = 99

// agentsDir é onde fica o arquivo de cada subagente.
const agentsDir = ".claude/agents"

var (
	agentNameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	handoffFileRe = regexp.MustCompile(`^([0-9]{2})-([a-z][a-z0-9-]*)\.md$`)
)

// Handoff é um handoff gravado de uma delivery.
type Handoff struct {
	// N é o número de ordem (o NN do nome do arquivo).
	N int
	// Agent é o subagente que gravou.
	Agent string
	// Path é o caminho relativo à raiz, com "/".
	Path string
	// Content é o conteúdo do arquivo.
	Content string
}

// PutHandoff grava o handoff de agent na delivery id, em
// <deliveries_dir>/<id>/handoffs/NN-<agent>.md, com NN uma unidade acima do
// maior existente, e devolve o caminho relativo à raiz. Só cria: a criação é
// exclusiva e, se outro processo pegou o número, tenta o próximo. Nada é
// gravado quando a delivery não existe, o subagente não tem arquivo em
// .claude/agents, o conteúdo é inválido ou o caminho sai da raiz (TD-17,
// TD-41).
func PutHandoff(cfg *config.Config, id, agent, content string) (string, error) {
	if err := checkHandoffContent(content); err != nil {
		return "", err
	}
	if err := checkAgentName(agent); err != nil {
		return "", err
	}
	root, realRoot, deliveryDir, err := deliveryDirOf(cfg, id)
	if err != nil {
		return "", err
	}
	agentFile, err := confine(root, agentsDir+"/"+agent+".md")
	if err != nil {
		return "", fmt.Errorf("handoff: %w", err)
	}
	if err := regularInside(realRoot, agentFile); err != nil {
		return "", fmt.Errorf("handoff: subagente %q sem arquivo em %s/%s.md: %v", agent, agentsDir, agent, err)
	}

	dir := filepath.Join(deliveryDir, HandoffsDir)
	existing, err := listHandoffs(realRoot, dir)
	if err != nil {
		return "", err
	}
	next := 1
	if len(existing) > 0 {
		next = existing[len(existing)-1].n + 1
	}
	if next > maxHandoffs {
		return "", fmt.Errorf("handoff: a delivery %s já tem %d handoffs, o máximo", id, maxHandoffs)
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("handoff: falha ao criar a pasta de handoffs: %w", err)
	}
	// Depois do Mkdir, de novo: a pasta pode ter sido trocada por symlink.
	if err := dirInside(realRoot, dir); err != nil {
		return "", err
	}
	for n := next; n <= maxHandoffs; n++ {
		name := fmt.Sprintf("%02d-%s.md", n, agent)
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("handoff: falha ao criar %s: %w", name, err)
		}
		_, werr := f.WriteString(content)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(p)
			return "", fmt.Errorf("handoff: falha ao gravar %s: %w", name, errors.Join(werr, cerr))
		}
		return path.Join(cfg.DeliveriesDir, id, HandoffsDir, name), nil
	}
	return "", fmt.Errorf("handoff: a delivery %s já tem %d handoffs, o máximo", id, maxHandoffs)
}

// Handoffs devolve os handoffs da delivery id em ordem de número; com agent
// não vazio, só os daquele subagente. Pasta de handoffs ausente devolve lista
// vazia. Arquivo fora do padrão NN-<agente>.md é ignorado.
func Handoffs(cfg *config.Config, id, agent string) ([]Handoff, error) {
	if agent != "" {
		if err := checkAgentName(agent); err != nil {
			return nil, err
		}
	}
	_, realRoot, deliveryDir, err := deliveryDirOf(cfg, id)
	if err != nil {
		return nil, err
	}
	entries, err := listHandoffs(realRoot, filepath.Join(deliveryDir, HandoffsDir))
	if err != nil {
		return nil, err
	}
	out := []Handoff{}
	for _, e := range entries {
		if agent != "" && e.agent != agent {
			continue
		}
		if err := regularInside(realRoot, e.path); err != nil {
			return nil, fmt.Errorf("handoff: %s: %v", e.name, err)
		}
		info, err := os.Stat(e.path)
		if err != nil {
			return nil, fmt.Errorf("handoff: %s: %w", e.name, err)
		}
		if info.Size() > MaxHandoffSize {
			return nil, fmt.Errorf("handoff: %s acima de 64 KiB", e.name)
		}
		data, err := os.ReadFile(e.path)
		if err != nil {
			return nil, fmt.Errorf("handoff: %s: %w", e.name, err)
		}
		out = append(out, Handoff{N: e.n, Agent: e.agent, Path: path.Join(cfg.DeliveriesDir, id, HandoffsDir, e.name), Content: string(data)})
	}
	return out, nil
}

type handoffEntry struct {
	n           int
	agent, name string
	path        string
}

// listHandoffs lista os arquivos NN-<agente>.md de dir em ordem de número.
// dir ausente devolve lista vazia; dir que não é diretório ou que resolve para
// fora da raiz é erro.
func listHandoffs(realRoot, dir string) ([]handoffEntry, error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err := dirInside(realRoot, dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("handoff: falha ao ler a pasta de handoffs: %w", err)
	}
	var out []handoffEntry
	for _, e := range entries {
		m := handoffFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		out = append(out, handoffEntry{n: n, agent: m[2], name: e.Name(), path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n < out[j].n
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// deliveryDirOf confere o ID e a existência do report e devolve a raiz, a raiz
// real e o diretório da delivery.
func deliveryDirOf(cfg *config.Config, id string) (root, realRoot, dir string, err error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\|:`) ||
		strings.ContainsFunc(id, unicode.IsControl) || id != strings.TrimSpace(id) {
		return "", "", "", fmt.Errorf("handoff: ID de delivery inválido: %q", id)
	}
	if cfg.DeliveryIDPattern != "" {
		re, err := regexp.Compile(cfg.DeliveryIDPattern)
		if err != nil {
			return "", "", "", fmt.Errorf("handoff: delivery_id_pattern %q: %w", cfg.DeliveryIDPattern, err)
		}
		if !re.MatchString(id) {
			return "", "", "", fmt.Errorf("handoff: ID %q não casa delivery_id_pattern %q", id, cfg.DeliveryIDPattern)
		}
	}
	root = cfg.Root()
	realRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", fmt.Errorf("handoff: raiz do projeto: %w", err)
	}
	dir, err = confine(root, cfg.DeliveriesDir+"/"+id)
	if err != nil {
		return "", "", "", fmt.Errorf("handoff: %w", err)
	}
	reportRel := path.Join(cfg.DeliveriesDir, id, ReportFile)
	reportPath := filepath.Join(dir, ReportFile)
	if _, err := os.Lstat(reportPath); errors.Is(err, fs.ErrNotExist) {
		return "", "", "", fmt.Errorf("handoff: a delivery %s não existe (%s ausente)", id, reportRel)
	}
	if err := regularInside(realRoot, reportPath); err != nil {
		return "", "", "", fmt.Errorf("handoff: %s: %v", reportRel, err)
	}
	if err := dirInside(realRoot, dir); err != nil {
		return "", "", "", err
	}
	return root, realRoot, dir, nil
}

// regularInside confere que p existe, é arquivo regular e, com symlinks
// resolvidos, fica dentro da raiz real.
func regularInside(realRoot, p string) error {
	if err := insideReal(realRoot, p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("não existe")
		}
		return fmt.Errorf("fora da raiz do projeto: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("não é arquivo regular")
	}
	return nil
}

// dirInside confere que p é diretório e, com symlinks resolvidos, fica dentro
// da raiz real.
func dirInside(realRoot, p string) error {
	if err := insideReal(realRoot, p); err != nil {
		return fmt.Errorf("handoff: %s fora da raiz do projeto: %v", filepath.Base(p), err)
	}
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("handoff: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("handoff: %s não é diretório", filepath.Base(p))
	}
	return nil
}

func checkAgentName(agent string) error {
	if !agentNameRe.MatchString(agent) {
		return fmt.Errorf("handoff: agent %q inválido: use o nome do subagente (^[a-z][a-z0-9-]*$)", agent)
	}
	return nil
}

func checkHandoffContent(content string) error {
	switch {
	case strings.TrimSpace(content) == "":
		return errors.New("handoff: conteúdo vazio")
	case strings.ContainsRune(content, 0):
		return errors.New("handoff: conteúdo com NUL")
	case len(content) > MaxHandoffSize:
		return fmt.Errorf("handoff: conteúdo com %d bytes, acima de 64 KiB", len(content))
	case !utf8.ValidString(content):
		return errors.New("handoff: conteúdo não é UTF-8 válido")
	}
	return nil
}
