// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/scaffold/scaffold.go @ 0eac73a (MIT).
// Alterações: só Normalize/Scaffold/initGitRepo; v1 só api + PostgreSQL, sem
// tipo nem banco nas opções (TD-07); validação completa antes de tocar no disco
// (nome, módulo Go, provedor, módulos, versão, resumo) com conteúdo renderizado
// em memória pelo catálogo de internal/templates (TD-17, TD-22); destino
// inexistente ou diretório vazio, nunca sobrescrito; geração num staging irmão
// e move no fim, com remoção de tudo em falha (TD-24); caminhos conferidos
// dentro do staging após filepath.Clean (TD-17); lock com o hash de cada
// gerenciado (TD-23); git init -b main + core.hooksPath sem fallback, sem add
// nem commit, e falha do git vira erro com o comando a rodar (TD-24, TD-08);
// ErrInvalid separa entrada ou destino inválido (CLI sai com 2) de falha de
// escrita (1).

// Package scaffold implementa o init: gera um projeto novo num diretório
// temporário irmão do destino e o move para o destino no fim (TD-24).
//
// Nada é sobrescrito: o destino não pode existir, ou tem de ser um diretório
// vazio. Toda a entrada é validada e todo o conteúdo renderizado antes de
// tocar no disco; qualquer falha depois disso remove o que foi criado e deixa o
// destino como estava.
package scaffold

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

// stagingPrefix é o prefixo do diretório temporário criado ao lado do destino.
const stagingPrefix = ".pipeline-claude-init-"

// Options são os parâmetros de Init.
type Options struct {
	// Name é o nome do projeto, ^[a-z][a-z0-9-]*$ (TD-17).
	Name string
	// Dir é o destino; vazio vale ./<Name>.
	Dir string
	// Module é o caminho do módulo Go; vazio vale Name.
	Module string
	// Summary é o resumo do projeto, uma linha; vazio vale um resumo padrão.
	Summary string
	// Provider é o provedor git, github ou gitlab; vazio vale github (TD-07).
	Provider string
	// ToolkitVersion é a versão do toolkit fixada no Makefile e no lock
	// (vX.Y.Z, com pré-lançamento ou build opcionais) (TD-06).
	ToolkitVersion string
	// Modules são os módulos de enabled_modules, de config.Modules, sem
	// repetição.
	Modules []string
	// Git roda git init -b main e git config core.hooksPath .githooks no
	// projeto criado.
	Git bool
	// Stdout e Stderr recebem a saída do git; nil descarta.
	Stdout, Stderr io.Writer
}

// Result descreve o projeto criado.
type Result struct {
	// Root é o caminho absoluto do projeto.
	Root string
	// Files são os arquivos gerados, relativos a Root com "/", ordenados,
	// inclusive o lock.
	Files []string
}

// deps isola o que os testes precisam fazer falhar ou trocar.
type deps struct {
	// writeFile cria name em modo exclusivo com o conteúdo e a permissão.
	writeFile func(name string, data []byte, mode fs.FileMode) error
	// rename move um caminho (o move do staging para o destino).
	rename   func(oldpath, newpath string) error
	lookPath func(file string) (string, error)
	// runGit roda git com args em dir, sem shell.
	runGit func(dir string, stdout, stderr io.Writer, git string, args ...string) error
}

func defaultDeps() deps {
	return deps{
		writeFile: createFile,
		rename:    os.Rename,
		lookPath:  exec.LookPath,
		runGit: func(dir string, stdout, stderr io.Writer, git string, args ...string) error {
			cmd := exec.Command(git, args...)
			cmd.Dir = dir
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			return cmd.Run()
		},
	}
}

// ErrInvalid marca erro de entrada (nome, módulo, provedor, módulos, versão,
// resumo) ou de destino (com conteúdo, arquivo ou symlink): a CLI sai com 2
// nele e com 1 em falha de escrita. A mensagem continua a do erro original.
var ErrInvalid = errors.New("scaffold: entrada inválida")

// invalidError embrulha um erro de entrada: Is(ErrInvalid) sem mudar a
// mensagem.
type invalidError struct{ error }

func (e invalidError) Is(target error) bool { return target == ErrInvalid }
func (e invalidError) Unwrap() error        { return e.error }

func invalid(err error) error { return invalidError{err} }

// ValidVersion diz se v é versão de módulo aceita como versão do toolkit:
// vX.Y.Z, com pré-lançamento (inclusive pseudo-versão) ou build opcionais.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Init valida opts, gera o projeto num staging ao lado do destino e o move
// para o destino. Em erro antes do move, nada fica no disco. Com Git, uma
// falha do git devolve o Result do projeto já criado junto com o erro.
func Init(opts Options) (Result, error) {
	return initWith(opts, defaultDeps())
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// rendered é um arquivo pronto para gravar.
type rendered struct {
	rel     string // destino relativo, com "/"
	content []byte
	mode    fs.FileMode
}

// plan é o init calculado e ainda não gravado.
type plan struct {
	dest   string // absoluto
	exists bool   // destino é diretório vazio existente
	files  []rendered
	lock   *manifest.Lock
	git    bool
	stdout io.Writer
	stderr io.Writer
}

func initWith(opts Options, d deps) (Result, error) {
	p, err := prepare(opts)
	if err != nil {
		return Result{}, err
	}
	if err := p.build(d); err != nil {
		return Result{}, err
	}
	res := Result{Root: p.dest, Files: p.fileList()}
	if p.git {
		if err := initGit(p, d); err != nil {
			return res, err
		}
	}
	return res, nil
}

// prepare valida a entrada, confere o destino e renderiza tudo em memória,
// sem gravar nada.
func prepare(opts Options) (*plan, error) {
	data, err := normalize(opts)
	if err != nil {
		return nil, err
	}
	dir := opts.Dir
	if dir == "" {
		dir = "." + string(filepath.Separator) + data.Name
	}
	dest, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("scaffold: destino %s: %w", dir, err)
	}
	exists, err := checkDest(dest)
	if err != nil {
		return nil, err
	}

	p := &plan{
		dest:   dest,
		exists: exists,
		git:    opts.Git,
		stdout: writerOr(opts.Stdout),
		stderr: writerOr(opts.Stderr),
		lock: &manifest.Lock{
			Version:        manifest.Version,
			ToolkitVersion: data.ToolkitVersion,
			Files:          map[string]string{},
			Released:       []string{},
		},
	}
	seen := map[string]bool{manifest.FileName: true}
	for _, class := range []templates.Class{templates.Project, templates.Managed} {
		for _, f := range templates.Expected(class, data.Modules) {
			rel, err := templates.DestPath(f, data)
			if err != nil {
				return nil, fmt.Errorf("scaffold: %w", err)
			}
			if seen[rel] {
				return nil, fmt.Errorf("scaffold: destino %s repetido no catálogo", rel)
			}
			seen[rel] = true
			content, err := templates.Render(f, data)
			if err != nil {
				return nil, fmt.Errorf("scaffold: %w", err)
			}
			if class == templates.Managed {
				p.lock.Files[rel] = manifest.Hash(content)
			}
			p.files = append(p.files, rendered{rel: rel, content: content, mode: f.Mode.Perm()})
		}
	}
	return p, nil
}

// normalize valida opts e devolve os valores de renderização, com os padrões
// preenchidos.
func normalize(o Options) (templates.Data, error) {
	if !nameRe.MatchString(o.Name) {
		return templates.Data{}, invalid(fmt.Errorf("scaffold: nome do projeto %q inválido: use letras minúsculas, dígitos e hífen, começando por letra (^[a-z][a-z0-9-]*$)", o.Name))
	}
	module := o.Module
	if module == "" {
		module = o.Name
	}
	if strings.ContainsFunc(module, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) ||
		strings.Contains(module, "..") || strings.HasPrefix(module, "/") || strings.HasSuffix(module, "/") {
		return templates.Data{}, invalid(fmt.Errorf("scaffold: módulo Go %q inválido: sem espaço, sem \"..\" e sem \"/\" no início ou no fim", module))
	}
	provider := o.Provider
	if provider == "" {
		provider = "github"
	}
	if provider != "github" && provider != "gitlab" {
		return templates.Data{}, invalid(fmt.Errorf("scaffold: provedor %q não suportado: use github ou gitlab", provider))
	}
	mods := slices.Clone(o.Modules)
	slices.Sort(mods)
	for i, m := range mods {
		if !config.Modules[m] {
			return templates.Data{}, invalid(fmt.Errorf("scaffold: módulo opcional %q desconhecido: use um de %s", m, strings.Join(knownModules(), ", ")))
		}
		if i > 0 && mods[i-1] == m {
			return templates.Data{}, invalid(fmt.Errorf("scaffold: módulo opcional %q repetido", m))
		}
	}
	if !ValidVersion(o.ToolkitVersion) {
		return templates.Data{}, invalid(fmt.Errorf("scaffold: versão do toolkit %q inválida: esperado vX.Y.Z (com pré-lançamento ou build opcionais)", o.ToolkitVersion))
	}
	summary := strings.TrimSpace(o.Summary)
	if strings.ContainsFunc(o.Summary, unicode.IsControl) {
		return templates.Data{}, invalid(errors.New("scaffold: o resumo precisa ter uma linha só, sem caractere de controle"))
	}
	if summary == "" {
		summary = fmt.Sprintf("API %s em Go com PostgreSQL.", o.Name)
	}
	return templates.Data{
		Name:           o.Name,
		Module:         module,
		Summary:        summary,
		Provider:       provider,
		ToolkitVersion: o.ToolkitVersion,
		Modules:        mods,
	}, nil
}

func knownModules() []string {
	out := make([]string, 0, len(config.Modules))
	for m := range config.Modules {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

func writerOr(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// checkDest confere o destino: inexistente (false) ou diretório vazio (true).
// Symlink, arquivo ou diretório com conteúdo é erro.
func checkDest(dest string) (bool, error) {
	info, err := os.Lstat(dest)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("scaffold: falha ao verificar o destino %s: %w", dest, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return false, invalid(fmt.Errorf("scaffold: o destino %s é um symlink; informe o diretório real", dest))
	case !info.IsDir():
		return false, invalid(fmt.Errorf("scaffold: o destino %s existe e não é um diretório", dest))
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return false, fmt.Errorf("scaffold: falha ao ler o destino %s: %w", dest, err)
	}
	if len(entries) > 0 {
		return false, invalid(fmt.Errorf("scaffold: o destino %s não está vazio; nada foi criado", dest))
	}
	return true, nil
}

// build grava o projeto no staging e o move para o destino. Em falha, remove
// o staging e o que já tiver sido movido.
func (p *plan) build(d deps) (err error) {
	staging, err := os.MkdirTemp(filepath.Dir(p.dest), stagingPrefix+"*")
	if err != nil {
		return fmt.Errorf("scaffold: falha ao criar o diretório temporário ao lado de %s (nada foi criado): %w", p.dest, err)
	}
	var moved []string // caminhos já movidos para o destino existente
	defer func() {
		if err == nil {
			return
		}
		var errs []error
		for _, m := range slices.Backward(moved) {
			if rerr := os.RemoveAll(m); rerr != nil {
				errs = append(errs, rerr)
			}
		}
		if rerr := os.RemoveAll(staging); rerr != nil {
			errs = append(errs, rerr)
		}
		if len(errs) > 0 {
			err = errors.Join(append([]error{err, errors.New("scaffold: limpeza incompleta")}, errs...)...)
		}
	}()

	// MkdirTemp cria com 0700; a raiz do projeto fica como um diretório comum.
	if err := os.Chmod(staging, 0o755); err != nil {
		return fmt.Errorf("scaffold: falha ao ajustar a permissão de %s: %w", staging, err)
	}
	for _, f := range p.files {
		target, err := confine(staging, f.rel)
		if err != nil {
			return fmt.Errorf("scaffold: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("scaffold: falha ao criar o diretório de %s: %w", f.rel, err)
		}
		if err := d.writeFile(target, f.content, f.mode); err != nil {
			return fmt.Errorf("scaffold: falha ao gravar %s: %w", f.rel, err)
		}
	}
	if err := manifest.Write(staging, p.lock); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	if !p.exists {
		if err := d.rename(staging, p.dest); err != nil {
			return fmt.Errorf("scaffold: falha ao mover o projeto para %s: %w", p.dest, err)
		}
		return nil
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return fmt.Errorf("scaffold: falha ao ler %s: %w", staging, err)
	}
	for _, e := range entries {
		src, err := confine(staging, e.Name())
		if err != nil {
			return fmt.Errorf("scaffold: %w", err)
		}
		dst, err := confine(p.dest, e.Name())
		if err != nil {
			return fmt.Errorf("scaffold: %w", err)
		}
		// rename substitui arquivo existente: confere antes para não
		// sobrescrever o que apareceu no destino depois da checagem.
		if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("scaffold: %s apareceu no destino durante o init; nada foi sobrescrito", e.Name())
		}
		if err := d.rename(src, dst); err != nil {
			return fmt.Errorf("scaffold: falha ao mover %s para %s: %w", e.Name(), p.dest, err)
		}
		moved = append(moved, dst)
	}
	if err := os.Remove(staging); err != nil {
		return fmt.Errorf("scaffold: falha ao remover o diretório temporário %s: %w", staging, err)
	}
	return nil
}

// fileList devolve os arquivos gerados, ordenados, com o lock.
func (p *plan) fileList() []string {
	out := []string{manifest.FileName}
	for _, f := range p.files {
		out = append(out, f.rel)
	}
	slices.Sort(out)
	return out
}

// createFile cria name em modo exclusivo (nunca sobrescreve) e fixa a
// permissão em mode, independente da umask.
func createFile(name string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(mode)
	}
	return errors.Join(werr, f.Close())
}

// confine junta rel (relativo, com "/") a root e confere, depois de
// filepath.Clean, que o resultado fica estritamente dentro de root (TD-17).
func confine(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("caminho vazio")
	}
	if filepath.IsAbs(rel) || path.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("%q é absoluto: fora de %s", rel, root)
	}
	root = filepath.Clean(root)
	p := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	r, err := filepath.Rel(root, p)
	if err != nil || r == "." || r == ".." || filepath.IsAbs(r) || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q fica fora de %s", rel, root)
	}
	return p, nil
}

// gitHint é o que rodar à mão quando o git falha.
func gitHint(dest string) string {
	return fmt.Sprintf("o projeto foi criado em %s; para versionar, rode nele: git init -b main && git config core.hooksPath .githooks", dest)
}

// initGit roda git init -b main e git config core.hooksPath .githooks no
// projeto, sem shell e sem commit (TD-24).
func initGit(p *plan, d deps) error {
	git, err := d.lookPath("git")
	if err != nil {
		return fmt.Errorf("scaffold: git não encontrado no PATH (%w); %s", err, gitHint(p.dest))
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "core.hooksPath", ".githooks"}} {
		if err := d.runGit(p.dest, p.stdout, p.stderr, git, args...); err != nil {
			return fmt.Errorf("scaffold: git %s falhou (%w); %s", strings.Join(args, " "), err, gitHint(p.dest))
		}
	}
	return nil
}
