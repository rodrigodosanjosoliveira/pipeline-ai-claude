// Package gitdiff lista os arquivos alterados de um repositório git em relação
// a uma base, para o validador decidir o que o diff toca, e monta o diff de
// revisão que o MCP entrega aos revisores (TD-29).
//
// O diff é completo (TD-11): commits da branch desde a base, working tree,
// índice (staged) e arquivos novos não rastreados, sem detecção de renomeação
// (as duas pontas de um rename aparecem). Qualquer falha do git é erro com o
// comando e o stderr; nunca há lista parcial.
package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"slices"
	"strings"
)

// ChangedFiles devolve, ordenada e sem repetição, a união de:
//
//	git diff --name-only --no-renames -z <base>...HEAD
//	git diff --name-only --no-renames -z HEAD
//	git ls-files --others --exclude-standard -z
//
// Os caminhos são relativos à raiz do repositório, com "/", como o git os
// devolve, mesmo quando root é um subdiretório. Sem mudanças, devolve slice
// vazio (não nil). Em erro, devolve nil.
func ChangedFiles(ctx context.Context, root, base string) ([]string, error) {
	if root == "" {
		return nil, errors.New("gitdiff: root vazio")
	}
	if base == "" {
		return nil, errors.New("gitdiff: base vazio")
	}
	// base vira argumento do git: um valor como "--output=arquivo" seria lido
	// como opção. Além da rejeição, os comandos usam --end-of-options.
	if strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("gitdiff: base %q não pode começar com \"-\"", base)
	}

	// Confirma que root está num working tree antes de qualquer diff.
	if _, err := run(ctx, root, "rev-parse", "--show-toplevel"); err != nil {
		return nil, err
	}
	// Repositório sem commits: HEAD não resolve. --quiet faz o rev-parse sair 1
	// sem stderr nesse caso; qualquer outra falha segue como erro do git.
	if _, err := run(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && ctx.Err() == nil {
			return nil, fmt.Errorf("gitdiff: repositório sem commits em %s (HEAD não existe): faça o primeiro commit antes de validar", root)
		}
		return nil, err
	}

	commands := [][]string{
		{"diff", "--name-only", "--no-renames", "-z", "--end-of-options", base + "...HEAD", "--"},
		{"diff", "--name-only", "--no-renames", "-z", "--end-of-options", "HEAD", "--"},
		// ":/" e --full-name: não rastreados do repositório inteiro, com caminho
		// relativo à raiz, como no git diff, mesmo se root for subdiretório.
		{"ls-files", "--others", "--exclude-standard", "--full-name", "-z", "--", ":/"},
	}
	files := []string{}
	for _, args := range commands {
		out, err := run(ctx, root, args...)
		if err != nil {
			return nil, err
		}
		files = append(files, splitNUL(out)...)
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// run executa git com core.quotepath=off em dir e devolve o stdout. O erro traz
// o comando e o stderr e embrulha o erro original (ex.: *exec.ExitError,
// context.Canceled).
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.quotepath=off"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = fmt.Errorf("%w: %w", ctxErr, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "sem stderr"
		}
		return nil, fmt.Errorf("gitdiff: git %s: %w: %s", strings.Join(full, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// splitNUL separa a saída -z do git. O NUL final gera um campo vazio, que é
// descartado; nomes com espaço, acento ou quebra de linha chegam intactos.
func splitNUL(b []byte) []string {
	var out []string
	for _, f := range bytes.Split(b, []byte{0}) {
		if len(f) > 0 {
			out = append(out, string(f))
		}
	}
	return out
}

// Diff é o diff de revisão de uma branch contra a base (TD-29).
type Diff struct {
	// MergeBase é o commit comum entre a base e HEAD, contra o qual o patch é
	// calculado.
	MergeBase string
	// Files são os arquivos alterados como em ChangedFiles, filtrados pelos
	// caminhos pedidos; relativos à raiz do repositório, com "/".
	Files []string
	// Untracked são os de Files ainda não rastreados: o patch não traz o
	// conteúdo deles.
	Untracked []string
	// Patch é git diff <MergeBase> (commits da branch, índice e working tree),
	// cortado em fim de linha se passar do limite.
	Patch string
	// Truncated diz se Patch foi cortado.
	Truncated bool
	// Size é o tamanho do patch completo, em bytes.
	Size int
}

// Review devolve o diff de revisão de root contra base: a lista do TD-11 e o
// patch contra o merge-base, sem cor, diff externo, textconv nem detecção de
// renomeação. paths filtra por arquivo ou diretório, relativos à raiz do
// repositório, como pathspec literal (sem mágica como ":!"); vazio é tudo.
// max limita o patch em bytes. Qualquer falha do git é erro; nunca há diff
// parcial além do corte declarado em Truncated.
func Review(ctx context.Context, root, base string, paths []string, max int) (Diff, error) {
	if max <= 0 {
		return Diff{}, fmt.Errorf("gitdiff: limite do patch %d inválido", max)
	}
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		c, err := cleanPath(p)
		if err != nil {
			return Diff{}, err
		}
		clean = append(clean, c)
	}
	files, err := ChangedFiles(ctx, root, base)
	if err != nil {
		return Diff{}, err
	}
	top, err := run(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return Diff{}, err
	}
	dir := strings.TrimSpace(string(top))
	mb, err := run(ctx, dir, "merge-base", "--end-of-options", base, "HEAD")
	if err != nil {
		return Diff{}, err
	}
	d := Diff{MergeBase: strings.TrimSpace(string(mb)), Files: []string{}}
	for _, f := range files {
		if matchAny(f, clean) {
			d.Files = append(d.Files, f)
		}
	}
	untracked, err := run(ctx, dir, "ls-files", "--others", "--exclude-standard", "--full-name", "-z", "--", ":/")
	if err != nil {
		return Diff{}, err
	}
	for _, f := range splitNUL(untracked) {
		if matchAny(f, clean) {
			d.Untracked = append(d.Untracked, f)
		}
	}
	slices.Sort(d.Untracked)

	args := []string{"--literal-pathspecs", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames",
		"--src-prefix=a/", "--dst-prefix=b/", "--end-of-options", d.MergeBase, "--"}
	patch, err := run(ctx, dir, append(args, clean...)...)
	if err != nil {
		return Diff{}, err
	}
	d.Size = len(patch)
	if len(patch) > max {
		cut := bytes.LastIndexByte(patch[:max], '\n')
		patch = patch[:cut+1]
		d.Truncated = true
	}
	d.Patch = string(patch)
	return d, nil
}

// cleanPath valida um caminho de filtro: relativo, sem subir da raiz, sem
// contrabarra e sem começar com "-".
func cleanPath(p string) (string, error) {
	bad := func(why string) error { return fmt.Errorf("gitdiff: caminho %q inválido: %s", p, why) }
	switch {
	case p == "":
		return "", bad("vazio")
	case strings.HasPrefix(p, "-"):
		return "", bad("não pode começar com \"-\"")
	case strings.ContainsRune(p, '\\'):
		return "", bad("use \"/\" como separador")
	case path.IsAbs(p):
		return "", bad("precisa ser relativo à raiz do repositório")
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", bad("sai da raiz do repositório")
	}
	return c, nil
}

// matchAny diz se f é um dos caminhos ou está dentro de um deles; sem
// caminhos, tudo casa.
func matchAny(f string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if p == "." || f == p || strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}
