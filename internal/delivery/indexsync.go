package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/gitdiff"
)

// StatusMerged é o status de delivery mesclada.
const StatusMerged = "merged"

// syncable são os status que o index sync promove a merged (TD-42).
var syncable = []string{"draft", "em revisão"}

// SyncOptions são as opções de SyncIndex.
type SyncOptions struct {
	// Base é a ref onde procurar os commits; vazia usa base_ref da config.
	Base string
	// DryRun calcula as mudanças sem gravar.
	DryRun bool
}

// StatusChange é uma delivery promovida pelo SyncIndex.
type StatusChange struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Commit string `json:"commit"`
}

// SyncIndex marca como merged cada delivery draft ou em revisão do índice que
// tenha commit com "[<ID>]" na mensagem, alcançável da base: a coluna Status
// da linha do índice e a chave status do front matter do report (TD-42). Não
// mexe em mais nada, e o resto dos dois arquivos fica byte a byte. Erro de git,
// índice ilegível ou report que não dá para atualizar falham antes de gravar
// qualquer arquivo.
func SyncIndex(ctx context.Context, cfg *config.Config, opts SyncOptions) ([]StatusChange, error) {
	base := opts.Base
	if base == "" {
		base = cfg.BaseRef
	}
	root := cfg.Root()
	if err := gitdiff.VerifyCommit(ctx, root, base); err != nil {
		return nil, fmt.Errorf("index sync: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("index sync: raiz do projeto: %w", err)
	}

	indexPath, indexData, indexMode, err := readInside(root, realRoot, cfg.IndexFile)
	if err != nil {
		return nil, fmt.Errorf("index sync: %w", err)
	}
	idx, err := parseIndex(string(indexData))
	if err != nil {
		return nil, fmt.Errorf("index sync: %s: %w", cfg.IndexFile, err)
	}
	idCol := slices.IndexFunc(idx.header, isIDColumn)
	statusCol := slices.Index(idx.header, "status")
	if statusCol < 0 {
		return nil, fmt.Errorf("index sync: %s: a tabela de registro não tem coluna Status", cfg.IndexFile)
	}

	type reportEdit struct {
		path      string
		old, data []byte
		mode      os.FileMode
	}
	var (
		changes []StatusChange
		edits   []reportEdit
	)
	lines := slices.Clone(idx.lines)
	// Linhas de dados: depois do cabeçalho e do separador, até last.
	for i := idx.head + 2; i <= idx.last; i++ {
		parts := strings.Split(strings.TrimRight(lines[i], "\r\n"), "|")
		// parts[0] é o que vem antes do primeiro "|"; a célula c é parts[c+1].
		if idCol+1 >= len(parts)-1 || statusCol+1 >= len(parts)-1 {
			continue
		}
		id := cellValue(parts[idCol+1])
		status := strings.ToLower(cellValue(parts[statusCol+1]))
		if id == "" || !slices.Contains(syncable, status) {
			continue
		}
		commit, err := gitdiff.FindCommit(ctx, root, base, "["+id+"]")
		if err != nil {
			return nil, fmt.Errorf("index sync: %w", err)
		}
		if commit == "" {
			continue
		}
		reportRel := path.Join(cfg.DeliveriesDir, id, ReportFile)
		rp, rdata, rmode, err := readInside(root, realRoot, reportRel)
		if err != nil {
			return nil, fmt.Errorf("index sync: report de %s: %w", id, err)
		}
		updated, err := setFrontStatus(string(rdata), StatusMerged)
		if err != nil {
			return nil, fmt.Errorf("index sync: %s: %w", reportRel, err)
		}
		if updated != string(rdata) {
			edits = append(edits, reportEdit{rp, rdata, []byte(updated), rmode})
		}
		cell := parts[statusCol+1]
		parts[statusCol+1] = strings.Replace(cell, strings.TrimSpace(cell), StatusMerged, 1)
		eol := lines[i][len(strings.TrimRight(lines[i], "\r\n")):]
		lines[i] = strings.Join(parts, "|") + eol
		changes = append(changes, StatusChange{ID: id, From: cellValue(cell), To: StatusMerged, Commit: commit})
	}
	if len(changes) == 0 || opts.DryRun {
		return nonNil(changes), nil
	}
	for _, e := range edits {
		if err := replaceIndex(e.path, e.old, e.data, e.mode); err != nil {
			return nil, fmt.Errorf("index sync: %s: %w", e.path, err)
		}
	}
	if err := replaceIndex(indexPath, indexData, []byte(strings.Join(lines, "")), indexMode); err != nil {
		return nil, fmt.Errorf("index sync: %s: %w", cfg.IndexFile, err)
	}
	return changes, nil
}

func nonNil(c []StatusChange) []StatusChange {
	if c == nil {
		return []StatusChange{}
	}
	return c
}

// readInside lê rel, relativo à raiz, se for arquivo regular dentro da raiz
// real.
func readInside(root, realRoot, rel string) (string, []byte, os.FileMode, error) {
	p, err := confine(root, rel)
	if err != nil {
		return "", nil, 0, err
	}
	if err := regularInside(realRoot, p); err != nil {
		return "", nil, 0, fmt.Errorf("%s: %v", rel, err)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%s: %w", rel, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%s: %w", rel, err)
	}
	return p, data, info.Mode().Perm(), nil
}

// setFrontStatus troca o valor da chave status do front matter de content
// por status, mantendo o resto byte a byte. Sem front matter ou sem a chave é
// erro.
func setFrontStatus(content, status string) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	if strings.TrimRight(lines[0], "\r\n") != "---" {
		return "", errors.New("report sem front matter (--- na primeira linha)")
	}
	end := slices.IndexFunc(lines[1:], func(l string) bool { return strings.TrimRight(l, "\r\n") == "---" })
	if end < 0 {
		return "", errors.New("front matter sem o --- de fechamento")
	}
	for i := 1; i <= end; i++ {
		text := strings.TrimRight(lines[i], "\r\n")
		key, _, ok := strings.Cut(text, ":")
		// Chave indentada pertence a um valor aninhado, não ao front matter.
		if !ok || strings.TrimRight(key, " \t") != "status" {
			continue
		}
		lines[i] = "status: " + status + lines[i][len(text):]
		return strings.Join(lines, ""), nil
	}
	return "", errors.New("front matter sem a chave status")
}

// FormatChanges é a saída humana do index sync, a mesma na CLI e no MCP.
func FormatChanges(changes []StatusChange, dryRun bool) string {
	if len(changes) == 0 {
		return "Nenhuma delivery a marcar como merged.\n"
	}
	var b strings.Builder
	for _, c := range changes {
		fmt.Fprintf(&b, "%s: %s -> %s (commit %.12s)\n", c.ID, c.From, c.To, c.Commit)
	}
	if dryRun {
		fmt.Fprintf(&b, "%d delivery(s) a marcar como merged (--dry-run: nada foi gravado).\n", len(changes))
	} else {
		fmt.Fprintf(&b, "%d delivery(s) marcada(s) como merged no índice e no report.\n", len(changes))
	}
	return b.String()
}
