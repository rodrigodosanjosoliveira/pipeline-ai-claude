package gitdiff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// requireGit pula o teste se o git não estiver no PATH, exceto quando
// PIPELINE_CLAUDE_REQUIRE_GIT=1 (CI): aí a ausência do git é falha.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("PIPELINE_CLAUDE_REQUIRE_GIT") == "1" {
			t.Fatalf("git não encontrado no PATH e PIPELINE_CLAUDE_REQUIRE_GIT=1: %v", err)
		}
		t.Skipf("git não encontrado no PATH: %v", err)
	}
}

// isolateGit desliga a configuração global e de sistema do git e fixa autor e
// committer, para o teste não depender do ambiente de quem roda. Usa t.Setenv:
// os testes deste pacote não podem usar t.Parallel.
func isolateGit(t *testing.T) {
	t.Helper()
	// Variáveis que apontariam o git para outro repositório (ex.: teste rodando
	// dentro de um hook do git) são removidas; t.Setenv restaura no fim.
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES",
		"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
	}
	env := map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Exemplo",
		"GIT_AUTHOR_EMAIL":    "exemplo@exemplo.invalid",
		"GIT_AUTHOR_DATE":     "2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME":  "Exemplo",
		"GIT_COMMITTER_EMAIL": "exemplo@exemplo.invalid",
		"GIT_COMMITTER_DATE":  "2026-01-01T00:00:00Z",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// git roda um comando git de preparação no repositório de teste.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// write grava um arquivo (criando diretórios) dentro do repositório de teste.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newRepo cria um repositório vazio (sem commits) com branch inicial "main".
func newRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	isolateGit(t)
	dir := t.TempDir()
	git(t, dir, "-c", "init.defaultBranch=main", "init", "-q")
	return dir
}

// scenarioRepo monta o cenário completo:
//
//	main: .gitignore, manter.txt, renomear-de.txt, modificar.txt, staged.txt
//	task (a partir de main): commitado.txt, docs/ação.md, renomear-de.txt → renomear-para.txt
//	working tree: modificar.txt alterado, staged.txt alterado e no índice,
//	commitado.txt alterado de novo, novo.txt e "dir/relatório final.md" não
//	rastreados, ignorado.log ignorado pelo .gitignore.
func scenarioRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, ".gitignore", "*.log\n")
	write(t, dir, "manter.txt", "manter\n")
	write(t, dir, "renomear-de.txt", "conteúdo estável do arquivo renomeado\n")
	write(t, dir, "modificar.txt", "original\n")
	write(t, dir, "staged.txt", "original\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")

	git(t, dir, "checkout", "-q", "-b", "task")
	write(t, dir, "commitado.txt", "na branch\n")
	write(t, dir, "docs/ação.md", "acento\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "commit na branch")
	git(t, dir, "mv", "renomear-de.txt", "renomear-para.txt")
	git(t, dir, "commit", "-q", "-m", "renomeia")

	write(t, dir, "modificar.txt", "alterado\n")
	write(t, dir, "staged.txt", "alterado\n")
	git(t, dir, "add", "staged.txt")
	write(t, dir, "commitado.txt", "alterado de novo\n")
	write(t, dir, "novo.txt", "novo\n")
	write(t, dir, "dir/relatório final.md", "espaço e acento\n")
	write(t, dir, "ignorado.log", "ignorado\n")
	return dir
}

var scenarioWant = []string{
	"commitado.txt",
	"dir/relatório final.md",
	"docs/ação.md",
	"modificar.txt",
	"novo.txt",
	"renomear-de.txt",
	"renomear-para.txt",
	"staged.txt",
}

func TestChangedFiles(t *testing.T) {
	dir := scenarioRepo(t)

	got, err := ChangedFiles(context.Background(), dir, "main")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if !reflect.DeepEqual(got, scenarioWant) {
		t.Errorf("ChangedFiles:\n got %q\nwant %q", got, scenarioWant)
	}
}

func TestChangedFilesFontes(t *testing.T) {
	// Cada fonte do diff isolada: só um tipo de mudança por vez.
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  []string
	}{
		{
			name: "sem mudanças",
			want: []string{},
		},
		{
			name: "commitado na branch",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "c.txt", "c\n")
				git(t, dir, "add", "c.txt")
				git(t, dir, "commit", "-q", "-m", "c")
			},
			want: []string{"c.txt"},
		},
		{
			name: "modificado não commitado",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "a.txt", "mudou\n")
			},
			want: []string{"a.txt"},
		},
		{
			name: "staged",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "s.txt", "s\n")
				git(t, dir, "add", "s.txt")
			},
			want: []string{"s.txt"},
		},
		{
			name: "removido no working tree",
			setup: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"a.txt"},
		},
		{
			name: "novo não rastreado",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "n.txt", "n\n")
			},
			want: []string{"n.txt"},
		},
		{
			name: "ignorado não aparece",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "x.log", "x\n")
			},
			want: []string{},
		},
		{
			name: "renomeado staged mostra as duas pontas",
			setup: func(t *testing.T, dir string) {
				git(t, dir, "mv", "a.txt", "b.txt")
			},
			want: []string{"a.txt", "b.txt"},
		},
		{
			name: "nome com espaço e acento",
			setup: func(t *testing.T, dir string) {
				write(t, dir, "pasta com espaço/ção é.txt", "x\n")
			},
			want: []string{"pasta com espaço/ção é.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newRepo(t)
			write(t, dir, ".gitignore", "*.log\n")
			write(t, dir, "a.txt", "conteúdo estável para detecção de renomeação\n")
			git(t, dir, "add", "-A")
			git(t, dir, "commit", "-q", "-m", "base")
			git(t, dir, "branch", "base")
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			got, err := ChangedFiles(context.Background(), dir, "base")
			if err != nil {
				t.Fatalf("ChangedFiles: %v", err)
			}
			if got == nil {
				t.Fatal("ChangedFiles devolveu nil sem erro; esperado slice vazio")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChangedFilesSubdiretorio(t *testing.T) {
	// root dentro de um subdiretório do repositório: os caminhos continuam
	// relativos à raiz do repositório e nada fora do subdiretório se perde.
	dir := scenarioRepo(t)
	sub := filepath.Join(dir, "docs")

	got, err := ChangedFiles(context.Background(), sub, "main")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if !reflect.DeepEqual(got, scenarioWant) {
		t.Errorf("ChangedFiles(subdir):\n got %q\nwant %q", got, scenarioWant)
	}
}

func TestChangedFilesErros(t *testing.T) {
	tests := []struct {
		name string
		// setup devolve root e base.
		setup   func(t *testing.T) (root, base string)
		ctx     func() context.Context
		wantErr string
	}{
		{
			name:    "base vazio",
			setup:   func(t *testing.T) (string, string) { return scenarioRepo(t), "" },
			wantErr: "base vazio",
		},
		{
			name:    "base começando com hífen",
			setup:   func(t *testing.T) (string, string) { return scenarioRepo(t), "-p" },
			wantErr: "não pode começar com",
		},
		{
			name: "base tentando injetar opção --output",
			setup: func(t *testing.T) (string, string) {
				dir := scenarioRepo(t)
				return dir, "--output=" + filepath.Join(dir, "injetado")
			},
			wantErr: "não pode começar com",
		},
		{
			name:    "base inexistente",
			setup:   func(t *testing.T) (string, string) { return scenarioRepo(t), "nao-existe" },
			wantErr: "nao-existe",
		},
		{
			name:    "root vazio",
			setup:   func(t *testing.T) (string, string) { requireGit(t); return "", "main" },
			wantErr: "root vazio",
		},
		{
			name: "root fora de repositório",
			setup: func(t *testing.T) (string, string) {
				requireGit(t)
				isolateGit(t)
				dir := t.TempDir()
				// Impede o git de subir até um repositório acima do TempDir.
				t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
				return dir, "main"
			},
			wantErr: "rev-parse --show-toplevel",
		},
		{
			name: "root inexistente",
			setup: func(t *testing.T) (string, string) {
				requireGit(t)
				return filepath.Join(t.TempDir(), "nao-existe"), "main"
			},
			wantErr: "rev-parse --show-toplevel",
		},
		{
			name:    "repositório sem commits",
			setup:   func(t *testing.T) (string, string) { return newRepo(t), "main" },
			wantErr: "sem commits",
		},
		{
			name:  "contexto cancelado",
			setup: func(t *testing.T) (string, string) { return scenarioRepo(t), "main" },
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: "context canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, base := tt.setup(t)
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}
			got, err := ChangedFiles(ctx, root, base)
			if err == nil {
				t.Fatalf("esperado erro, veio nil (lista %q)", got)
			}
			if got != nil {
				t.Errorf("erro deve vir sem lista parcial, veio %q", got)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("erro %q não contém %q", err, tt.wantErr)
			}
			if root != "" {
				if _, statErr := os.Stat(filepath.Join(root, "injetado")); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("arquivo injetado existe ou stat falhou: %v", statErr)
				}
			}
		})
	}
}

func TestChangedFilesErroTrazComandoEStderr(t *testing.T) {
	// TD-11: a falha do git chega ao chamador com o comando e o stderr.
	dir := scenarioRepo(t)
	_, err := ChangedFiles(context.Background(), dir, "nao-existe")
	if err == nil {
		t.Fatal("esperado erro")
	}
	msg := err.Error()
	for _, want := range []string{"git ", "nao-existe", "fatal:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("erro %q não contém %q", msg, want)
		}
	}
}

func TestSplitNUL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"vazio", "", nil},
		{"um", "a\x00", []string{"a"}},
		{"vários", "a\x00b c\x00d/ç\x00", []string{"a", "b c", "d/ç"}},
		{"sem NUL final", "a\x00b", []string{"a", "b"}},
		{"nova linha no nome", "a\nb\x00", []string{"a\nb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitNUL([]byte(tt.in))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitNUL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
