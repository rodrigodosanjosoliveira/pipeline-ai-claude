// Package gitdiff lista os arquivos alterados de um repositório git em relação
// a uma base, para o validador decidir o que o diff toca.
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
