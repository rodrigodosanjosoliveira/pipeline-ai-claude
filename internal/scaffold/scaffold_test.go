package scaffold

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

const (
	testName    = "biblioteca-bairro"
	testModule  = "example.com/biblioteca-bairro"
	testSummary = "API de empréstimos da biblioteca do bairro."
	testVersion = "v0.3.0"
)

func allModules() []string { return slices.Sorted(maps.Keys(config.Modules)) }

// validOpts devolve opções válidas com destino parent/biblioteca-bairro.
func validOpts(parent string) Options {
	return Options{
		Name:           testName,
		Dir:            filepath.Join(parent, testName),
		Module:         testModule,
		Summary:        testSummary,
		ToolkitVersion: testVersion,
	}
}

// dataFor monta o templates.Data que o init deve usar com opts já completas.
func dataFor(o Options) templates.Data {
	mods := slices.Clone(o.Modules)
	slices.Sort(mods)
	p := o.Provider
	if p == "" {
		p = "github"
	}
	return templates.Data{Name: o.Name, Module: o.Module, Summary: o.Summary, Provider: p, ToolkitVersion: o.ToolkitVersion, Modules: mods}
}

// snapshot descreve a árvore abaixo de root sem seguir symlinks:
// caminho relativo → tipo, permissão e hash ou alvo.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "l " + target
		case info.IsDir():
			out[rel] = fmt.Sprintf("d %o", info.Mode().Perm())
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out[rel] = fmt.Sprintf("f %o %s", info.Mode().Perm(), hex.EncodeToString(sum[:]))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot de %s: %v", root, err)
	}
	return out
}

func assertUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshot(t, root)
	if !maps.Equal(before, after) {
		t.Errorf("a árvore em %s mudou:\nantes:  %v\ndepois: %v", root, before, after)
	}
}

// regularFiles lista os arquivos regulares abaixo de root, relativos, com "/".
func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listar %s: %v", root, err)
	}
	slices.Sort(out)
	return out
}

// checkProject confere o projeto gerado em root contra o catálogo e o lock.
func checkProject(t *testing.T, root string, o Options, res Result) {
	t.Helper()
	d := dataFor(o)
	want := []string{manifest.FileName}
	managed := map[string]string{}
	for _, class := range []templates.Class{templates.Project, templates.Managed} {
		for _, f := range templates.Expected(class, d.Modules) {
			dest, err := templates.DestPath(f, d)
			if err != nil {
				t.Fatalf("DestPath(%s): %v", f.Path, err)
			}
			want = append(want, dest)
			content, err := templates.Render(f, d)
			if err != nil {
				t.Fatalf("Render(%s): %v", f.Path, err)
			}
			p := filepath.Join(root, filepath.FromSlash(dest))
			got, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s: %v", dest, err)
				continue
			}
			if !bytes.Equal(got, content) {
				t.Errorf("%s: conteúdo diferente do renderizado", dest)
			}
			info, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			// Windows não tem permissão Unix (TD-39).
			if runtime.GOOS != "windows" && info.Mode().Perm() != f.Mode.Perm() {
				t.Errorf("%s: permissão %o, quero %o", dest, info.Mode().Perm(), f.Mode.Perm())
			}
			if class == templates.Managed {
				managed[dest] = manifest.Hash(content)
			}
		}
	}
	slices.Sort(want)
	if got := regularFiles(t, root); !slices.Equal(got, want) {
		t.Errorf("arquivos gerados:\n got %v\nwant %v", got, want)
	}
	if !slices.Equal(res.Files, want) {
		t.Errorf("Result.Files:\n got %v\nwant %v", res.Files, want)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Root != absRoot {
		t.Errorf("Result.Root = %q, quero %q", res.Root, absRoot)
	}

	lock, err := manifest.Read(root)
	if err != nil {
		t.Fatalf("manifest.Read: %v", err)
	}
	if lock.Version != 1 || lock.ToolkitVersion != o.ToolkitVersion || len(lock.Released) != 0 {
		t.Errorf("lock: version %d, toolkit %q, released %v", lock.Version, lock.ToolkitVersion, lock.Released)
	}
	if !maps.Equal(lock.Files, managed) {
		t.Errorf("lock.Files:\n got %v\nwant %v", lock.Files, managed)
	}
	// O lock confere com o disco.
	for rel, h := range lock.Files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("lock cita %s: %v", rel, err)
			continue
		}
		if manifest.Hash(data) != h {
			t.Errorf("hash de %s no lock não confere com o disco", rel)
		}
	}

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load no projeto gerado: %v", err)
	}
	if cfg.GitProvider != d.Provider {
		t.Errorf("git_provider = %q, quero %q", cfg.GitProvider, d.Provider)
	}
	gotMods := slices.Clone(cfg.EnabledModules)
	slices.Sort(gotMods)
	if !slices.Equal(gotMods, d.Modules) && !(len(gotMods) == 0 && len(d.Modules) == 0) {
		t.Errorf("enabled_modules = %v, quero %v", gotMods, d.Modules)
	}
}

// assertNoStaging falha se sobrou diretório de staging em parent.
func assertNoStaging(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pipeline-claude-init-") {
			t.Errorf("sobrou staging %s", e.Name())
		}
	}
}

func TestInitGeneratesProject(t *testing.T) {
	tests := []struct {
		name     string
		modules  []string
		provider string
	}{
		{"sem módulos, provedor padrão", nil, ""},
		{"todos os módulos", allModules(), "github"},
		{"um módulo, gitlab", []string{"tenant-isolation"}, "gitlab"},
		{"módulos fora de ordem", []string{"release-ops", "data-model"}, "github"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			o := validOpts(parent)
			o.Modules = tt.modules
			o.Provider = tt.provider
			res, err := Init(o)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			checkProject(t, o.Dir, o, res)
			assertNoStaging(t, parent)
			if _, err := os.Stat(filepath.Join(o.Dir, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("sem Git não deveria haver .git: %v", err)
			}
			info, err := os.Stat(o.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o055 != 0o055 {
				t.Errorf("raiz do projeto com permissão %o: o staging (0700) não pode vazar", info.Mode().Perm())
			}
		})
	}
}

func TestInitPrePushExecutable(t *testing.T) {
	parent := t.TempDir()
	o := validOpts(parent)
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(o.Dir, ".githooks", "pre-push"))
	if err != nil {
		t.Fatal(err)
	}
	// No Windows o git ignora o bit de execução e o Go não o grava (TD-39).
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("pre-push com permissão %o, quero 755", info.Mode().Perm())
	}
}

func TestInitDefaults(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Init(Options{Name: testName, ToolkitVersion: testVersion})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	root := filepath.Join(wd, testName)
	if res.Root != root {
		t.Errorf("Root = %q, quero %q (padrão ./<nome>)", res.Root, root)
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(gomod), "module "+testName+"\n") {
		t.Errorf("módulo padrão deveria ser o nome:\n%s", gomod)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitProvider != "github" {
		t.Errorf("provedor padrão = %q, quero github", cfg.GitProvider)
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(claude), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[2]) == "" {
		t.Errorf("CLAUDE.md sem resumo padrão:\n%s", claude)
	}
}

// goEnv isola o go do teste: sem rede, sem baixar toolchain, sem workspace
// (o mesmo de internal/templates).
func goEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GOFLAGS", "GOTOOLCHAIN", "GOPROXY", "GOWORK", "GO111MODULE":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GOTOOLCHAIN=local", "GOPROXY=off", "GOWORK=off", "GOFLAGS=-mod=mod")
}

func TestInitGoVetAndTest(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go não está no PATH")
	}
	parent := t.TempDir()
	o := validOpts(parent)
	o.Modules = allModules()
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"vet", "./..."}, {"test", "-count=1", "./..."}} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = o.Dir
		cmd.Env = goEnv()
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Errorf("go %s no projeto gerado: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestInitValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
		want   string
	}{
		{"nome vazio", func(o *Options) { o.Name = "" }, "nome"},
		{"nome com maiúscula", func(o *Options) { o.Name = "A" }, "nome"},
		{"nome com ..", func(o *Options) { o.Name = "../x" }, "nome"},
		{"nome com barra", func(o *Options) { o.Name = "a/b" }, "nome"},
		{"nome começa com dígito", func(o *Options) { o.Name = "1biblioteca" }, "nome"},
		{"nome começa com hífen", func(o *Options) { o.Name = "-biblioteca" }, "nome"},
		{"nome com espaço", func(o *Options) { o.Name = "biblioteca bairro" }, "nome"},
		{"nome só ponto", func(o *Options) { o.Name = "." }, "nome"},
		{"módulo com espaço", func(o *Options) { o.Module = "example.com/biblioteca bairro" }, "módulo Go"},
		{"módulo com ..", func(o *Options) { o.Module = "example.com/../x" }, "módulo Go"},
		{"módulo com / inicial", func(o *Options) { o.Module = "/example.com/x" }, "módulo Go"},
		{"módulo com / final", func(o *Options) { o.Module = "example.com/x/" }, "módulo Go"},
		{"módulo com quebra de linha", func(o *Options) { o.Module = "example.com/x\nrequire y" }, "módulo Go"},
		{"provedor inválido", func(o *Options) { o.Provider = "bitbucket" }, "provedor"},
		{"provedor em maiúsculas", func(o *Options) { o.Provider = "GitHub" }, "provedor"},
		{"módulo opcional desconhecido", func(o *Options) { o.Modules = []string{"pagamentos"} }, "módulo opcional \"pagamentos\" desconhecido"},
		{"módulo opcional repetido", func(o *Options) { o.Modules = []string{"data-model", "release-ops", "data-model"} }, "repetido"},
		{"módulo opcional vazio", func(o *Options) { o.Modules = []string{""} }, "módulo opcional \"\" desconhecido"},
		{"versão vazia", func(o *Options) { o.ToolkitVersion = "" }, "versão"},
		{"versão sem v", func(o *Options) { o.ToolkitVersion = "0.3.0" }, "versão"},
		{"versão incompleta", func(o *Options) { o.ToolkitVersion = "v0.3" }, "versão"},
		{"versão dev", func(o *Options) { o.ToolkitVersion = "dev" }, "versão"},
		{"versão com espaço", func(o *Options) { o.ToolkitVersion = "v0.3.0 " }, "versão"},
		{"resumo com duas linhas", func(o *Options) { o.Summary = "linha um\nlinha dois" }, "resumo"},
		{"resumo com CR", func(o *Options) { o.Summary = "linha um\rlinha dois" }, "resumo"},
		{"resumo com caractere de controle", func(o *Options) { o.Summary = "resumo\x00" }, "resumo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			before := snapshot(t, parent)
			o := validOpts(parent)
			tt.mutate(&o)
			var stdout, stderr bytes.Buffer
			o.Stdout, o.Stderr = &stdout, &stderr
			res, err := Init(o)
			if err == nil {
				t.Fatalf("Init aceitou entrada inválida; Root %q", res.Root)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("erro %v não é ErrInvalid (a CLI sai com 2 só para entrada inválida)", err)
			}
			// A validação é do próprio init, antes da renderização; o caminho
			// sai da mensagem (o TempDir leva o nome do teste).
			msg := strings.ReplaceAll(err.Error(), parent, "<pai>")
			if !strings.HasPrefix(msg, "scaffold: ") || strings.Contains(msg, "templates:") || !strings.Contains(msg, tt.want) {
				t.Errorf("erro %q: quero validação do scaffold mencionando %q", msg, tt.want)
			}
			if res.Root != "" || res.Files != nil {
				t.Errorf("Result não vazio em erro de validação: %+v", res)
			}
			assertUnchanged(t, parent, before)
		})
	}
}

func TestInitDestination(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, dest string)
		wantErr string // "" = sucesso
	}{
		{"destino inexistente", func(t *testing.T, dest string) {}, ""},
		{"destino diretório vazio", func(t *testing.T, dest string) {
			if err := os.Mkdir(dest, 0o750); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"destino é arquivo", func(t *testing.T, dest string) {
			if err := os.WriteFile(dest, []byte("conteúdo\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "não é um diretório"},
		{"destino com conteúdo", func(t *testing.T, dest string) {
			if err := os.Mkdir(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "LEIAME.md"), []byte("meu\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "não está vazio"},
		{"destino com só um diretório oculto", func(t *testing.T, dest string) {
			if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "não está vazio"},
		{"destino é symlink para diretório vazio", func(t *testing.T, dest string) {
			target := filepath.Join(filepath.Dir(dest), "alvo")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("alvo", dest); err != nil {
				t.Skipf("symlink indisponível: %v", err)
			}
		}, "symlink"},
		{"destino é symlink pendurado", func(t *testing.T, dest string) {
			if err := os.Symlink("inexistente", dest); err != nil {
				t.Skipf("symlink indisponível: %v", err)
			}
		}, "symlink"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			o := validOpts(parent)
			tt.prepare(t, o.Dir)
			before := snapshot(t, parent)
			res, err := Init(o)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Init: %v", err)
				}
				checkProject(t, o.Dir, o, res)
				assertNoStaging(t, parent)
				return
			}
			if err == nil {
				t.Fatal("Init aceitou destino inválido")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("erro %v não é ErrInvalid", err)
			}
			// O caminho sai da mensagem: o TempDir leva o nome do teste.
			if msg := strings.ReplaceAll(err.Error(), parent, "<pai>"); !strings.Contains(msg, tt.wantErr) {
				t.Errorf("erro %q não menciona %q", msg, tt.wantErr)
			}
			assertUnchanged(t, parent, before)
		})
	}
}

func TestInitParentMissing(t *testing.T) {
	parent := t.TempDir()
	before := snapshot(t, parent)
	o := validOpts(filepath.Join(parent, "nao-existe"))
	_, err := Init(o)
	if err == nil {
		t.Fatal("Init aceitou destino com diretório pai inexistente")
	}
	if errors.Is(err, ErrInvalid) {
		t.Errorf("falha de escrita %v marcada como ErrInvalid", err)
	}
	assertUnchanged(t, parent, before)
}

func TestInitReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows não tem permissão Unix de arquivo nem de diretório (TD-39)")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignora permissão de diretório")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	before := snapshot(t, parent)
	_, err := Init(validOpts(parent))
	if err == nil {
		t.Fatal("Init gravou em diretório somente leitura")
	}
	if errors.Is(err, ErrInvalid) {
		t.Errorf("falha de escrita %v marcada como ErrInvalid", err)
	}
	assertUnchanged(t, parent, before)
}

func TestInitFailureLeavesNoResidue(t *testing.T) {
	nFiles := len(templates.Expected(templates.Project, nil)) + len(templates.Expected(templates.Managed, nil))
	errInjected := errors.New("falha injetada")

	type tc struct {
		name      string
		emptyDest bool
		failWrite int // n-ésima gravação falha (0 = nenhuma)
		failMove  int // n-ésimo rename falha (0 = nenhum)
	}
	var tests []tc
	for _, empty := range []bool{false, true} {
		kind := "destino inexistente"
		if empty {
			kind = "destino vazio"
		}
		for _, n := range []int{1, nFiles / 2, nFiles} {
			tests = append(tests, tc{fmt.Sprintf("%s, gravação %d", kind, n), empty, n, 0})
		}
		tests = append(tests, tc{kind + ", primeiro move", empty, 0, 1})
	}
	tests = append(tests,
		tc{"destino vazio, quinto move", true, 0, 5},
		tc{"destino vazio, último move", true, 0, -1},
	)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			o := validOpts(parent)
			if tt.emptyDest {
				if err := os.Mkdir(o.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, parent)
			d := defaultDeps()
			realWrite, realRename := d.writeFile, d.rename
			writes, renames := 0, 0
			d.writeFile = func(name string, data []byte, mode fs.FileMode) error {
				writes++
				if writes == tt.failWrite {
					return errInjected
				}
				return realWrite(name, data, mode)
			}
			// Conta os renames de primeiro nível do staging (o move); o último
			// precisa do total, então o caso -1 roda antes um init de contagem.
			failMove := tt.failMove
			if failMove == -1 {
				entries := map[string]bool{}
				for _, class := range []templates.Class{templates.Project, templates.Managed} {
					for _, f := range templates.Expected(class, nil) {
						dest, err := templates.DestPath(f, dataFor(o))
						if err != nil {
							t.Fatal(err)
						}
						top, _, _ := strings.Cut(dest, "/")
						entries[top] = true
					}
				}
				entries[manifest.FileName] = true
				failMove = len(entries)
			}
			d.rename = func(oldpath, newpath string) error {
				renames++
				if renames == failMove {
					return errInjected
				}
				return realRename(oldpath, newpath)
			}
			_, err := initWith(o, d)
			if !errors.Is(err, errInjected) {
				t.Fatalf("Init = %v, quero a falha injetada", err)
			}
			if errors.Is(err, ErrInvalid) {
				t.Errorf("falha de escrita %v marcada como ErrInvalid", err)
			}
			if tt.failMove > 0 && renames < tt.failMove {
				t.Fatalf("só %d renames; o caso não exercitou o move", renames)
			}
			assertUnchanged(t, parent, before)
			assertNoStaging(t, parent)
		})
	}
}

func TestInitGitWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git não está no PATH")
	}
	parent := t.TempDir()
	o := validOpts(parent)
	o.Git = true
	var stdout, stderr bytes.Buffer
	o.Stdout, o.Stderr = &stdout, &stderr
	res, err := Init(o)
	if err != nil {
		t.Fatalf("Init com Git: %v\n%s", err, stderr.String())
	}
	if info, err := os.Stat(filepath.Join(o.Dir, ".git")); err != nil || !info.IsDir() {
		t.Fatalf(".git não criado: %v", err)
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", o.Dir}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := git("config", "core.hooksPath"); got != ".githooks" {
		t.Errorf("core.hooksPath = %q", got)
	}
	if got := git("symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("HEAD = %q, quero refs/heads/main", got)
	}
	if out, err := exec.Command("git", "-C", o.Dir, "rev-parse", "--verify", "HEAD").Output(); err == nil {
		t.Errorf("init não deveria fazer commit: HEAD = %s", out)
	}
	// .git não entra nos arquivos gerados.
	if slices.ContainsFunc(res.Files, func(f string) bool { return strings.HasPrefix(f, ".git/") }) {
		t.Errorf("Result.Files com .git: %v", res.Files)
	}
}

func TestInitGitWithoutGit(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	o := validOpts(parent)
	o.Git = true
	res, err := Init(o)
	if err == nil {
		t.Fatal("Init com Git e sem git no PATH deveria falhar")
	}
	for _, want := range []string{"git init -b main", "git config core.hooksPath .githooks"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro %q não diz o que rodar (%q)", err, want)
		}
	}
	// Os arquivos ficam: o projeto é válido sem git.
	checkProject(t, o.Dir, o, res)
	assertNoStaging(t, parent)
}

func TestInitGitCommandFails(t *testing.T) {
	errGit := errors.New("git falhou")
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("comando %d", failAt), func(t *testing.T) {
			parent := t.TempDir()
			o := validOpts(parent)
			o.Git = true
			d := defaultDeps()
			d.lookPath = func(string) (string, error) { return "/usr/bin/git-falso", nil }
			var calls [][]string
			d.runGit = func(dir string, stdout, stderr io.Writer, git string, args ...string) error {
				calls = append(calls, args)
				if dir != o.Dir {
					t.Errorf("git rodou em %q, quero %q", dir, o.Dir)
				}
				if git != "/usr/bin/git-falso" {
					t.Errorf("git = %q, quero o do lookPath", git)
				}
				if len(calls) == failAt {
					return errGit
				}
				return nil
			}
			res, err := initWith(o, d)
			if !errors.Is(err, errGit) {
				t.Fatalf("Init = %v, quero a falha do git", err)
			}
			if !strings.Contains(err.Error(), "git init -b main") {
				t.Errorf("erro %q não diz o que rodar", err)
			}
			if len(calls) != failAt {
				t.Errorf("git rodou %d vezes depois da falha: %v", len(calls), calls)
			}
			checkProject(t, o.Dir, o, res)
		})
	}

	t.Run("argumentos", func(t *testing.T) {
		parent := t.TempDir()
		o := validOpts(parent)
		o.Git = true
		d := defaultDeps()
		d.lookPath = func(string) (string, error) { return "git", nil }
		var calls []string
		d.runGit = func(dir string, stdout, stderr io.Writer, git string, args ...string) error {
			calls = append(calls, strings.Join(args, " "))
			return nil
		}
		if _, err := initWith(o, d); err != nil {
			t.Fatal(err)
		}
		want := []string{"init -b main", "config core.hooksPath .githooks"}
		if !slices.Equal(calls, want) {
			t.Errorf("comandos git = %q, quero %q", calls, want)
		}
	})
}

func TestConfine(t *testing.T) {
	root := filepath.Join(t.TempDir(), "staging")
	tests := []struct {
		rel  string
		want string // "" = erro
	}{
		{"CLAUDE.md", filepath.Join(root, "CLAUDE.md")},
		{".claude/agents/test-writer.md", filepath.Join(root, ".claude", "agents", "test-writer.md")},
		{"cmd/biblioteca-bairro/main.go", filepath.Join(root, "cmd", "biblioteca-bairro", "main.go")},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"../x", ""},
		{"a/../../x", ""},
		{"/etc/passwd", ""},
		{"a/../b", filepath.Join(root, "b")},
	}
	for _, tt := range tests {
		got, err := confine(root, tt.rel)
		if tt.want == "" {
			if err == nil {
				t.Errorf("confine(%q) = %q, quero erro", tt.rel, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("confine(%q) = %q, %v; quero %q", tt.rel, got, err, tt.want)
		}
	}
}

func TestValidVersion(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"v0.3.0", true},
		{"v1.2.3-rc.1", true},
		{"v0.0.0-20261027120000-abcdef123456", true},
		{"v0.3.0+meta", true},
		{"", false},
		{"dev", false},
		{"0.3.0", false},
		{"v0.3", false},
		{"v0.3.0 ", false},
	}
	for _, tt := range tests {
		if got := ValidVersion(tt.v); got != tt.want {
			t.Errorf("ValidVersion(%q) = %v, quero %v", tt.v, got, tt.want)
		}
	}
}
