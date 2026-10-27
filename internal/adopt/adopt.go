// Package adopt implementa o pipeline-claude adopt: instala a camada gerada
// num projeto Go existente sem alterar nada que já está lá (TD-31).
//
// Plan confere as pré-condições (TD-33), classifica cada arquivo do catálogo
// gerenciado e os arquivos do projeto que o toolkit cria (.claude/settings.json,
// .mcp.json e .pipeline-claude.json), monta o lock (TD-32) e as instruções do
// que o projeto precisa acrescentar à mão nos arquivos que já tem. Não grava
// nada. Apply só cria: cada arquivo com O_EXCL, sem seguir symlink e dentro da
// raiz (TD-17), conferindo o disco de novo antes de cada escrita; falha em
// qualquer passo remove o que esta execução criou e nunca o que já existia.
package adopt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/doctor"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/scaffold"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

// Action é o que o adopt faz com um caminho. Os valores são os que a CLI
// mostra e põe no JSON.
type Action string

// Ações do plano.
const (
	// Criar: ausente no disco; o adopt cria a partir do template.
	Criar Action = "criar"
	// Manter: existe e fica como está. Gerenciado igual ao template entra em
	// files no lock; arquivo do projeto pode gerar instrução.
	Manter Action = "manter"
	// DoProjeto: gerenciado que existe diferente do template; fica como está
	// e entra em released no lock (TD-32).
	DoProjeto Action = "do-projeto"
)

// Options são os parâmetros de Plan.
type Options struct {
	// Root é a raiz do projeto: caminho absoluto, limpo, de um diretório real.
	Root string
	// Provider é o provedor git, github ou gitlab. Vazio: deduzido do host do
	// remote origin. Só vale sem .pipeline-claude.json; com config, precisa
	// ser vazio ou igual a git_provider.
	Provider string
	// ToolkitVersion é a versão do toolkit gravada no lock (vX.Y.Z, TD-24).
	ToolkitVersion string
	// Modules são os módulos de enabled_modules. Vazio: os módulos de
	// config.Modules cujo subagente já existe. Só vale sem config; com config,
	// precisa ser vazio ou igual a enabled_modules.
	Modules []string
}

// Item é a decisão para um caminho.
type Item struct {
	// Path é o caminho relativo à raiz, com "/".
	Path string `json:"path"`
	// Action é a ação.
	Action Action `json:"action"`
	// Reason explica a ação.
	Reason string `json:"reason,omitempty"`

	// Preenchidos por Plan; Apply recusa item sem eles.
	planned bool
	managed bool        // da área gerenciada (catálogo)
	content []byte      // conteúdo a gravar, em Criar
	mode    fs.FileMode // permissão, em Criar
	hash    string      // hash do disco no plano, em gerenciado Manter/DoProjeto
}

// Instruction é um trecho que o projeto precisa acrescentar à mão num arquivo
// que já existe (ou na configuração do git). O adopt nunca o grava.
type Instruction struct {
	// Path é o arquivo (relativo à raiz, com "/") ou core.hooksPath.
	Path string `json:"path"`
	// Reason diz o que falta.
	Reason string `json:"reason"`
	// Snippet é o trecho exato a acrescentar, ou o comando a rodar.
	Snippet string `json:"snippet"`
}

// Adoption é o plano de adoção de Plan, aplicado por Apply.
type Adoption struct {
	// Root é a raiz do projeto.
	Root string
	// Items são os arquivos gerenciados do catálogo e os do projeto que o
	// toolkit cria, ordenados por Path.
	Items []Item
	// Instructions são os trechos a acrescentar à mão, nesta ordem:
	// settings.json, .mcp.json, Makefile, CLAUDE.md, .gitignore e
	// core.hooksPath.
	Instructions []Instruction
	// Lock é o lock que Apply grava por último (TD-32).
	Lock *manifest.Lock
	// Config é a configuração do projeto: a existente ou a que será criada.
	// Na que será criada, Root() é vazio.
	Config *config.Config

	root string // raiz conferida por Plan
}

// Result descreve o que Apply criou.
type Result struct {
	// Created são os arquivos criados, relativos à raiz com "/", ordenados,
	// inclusive o lock.
	Created []string `json:"created"`
}

// hooksPathKey é o Path da instrução de core.hooksPath.
const hooksPathKey = "core.hooksPath"

// settingsFile e mcpFile são arquivos do projeto que o adopt cria se faltarem.
const (
	settingsFile = ".claude/settings.json"
	mcpFile      = ".mcp.json"
)

// gitResult é a saída de um comando git. code é o exit code.
type gitResult struct {
	stdout, stderr string
	code           int
}

// deps isola o que os testes precisam trocar ou fazer falhar.
type deps struct {
	// git roda git com args em dir, sem shell; erro só quando nem rodou.
	git func(dir string, args ...string) (gitResult, error)
	// writeFile cria name em modo exclusivo com o conteúdo e a permissão.
	writeFile func(name string, data []byte, mode fs.FileMode) error
	// writeLock grava o lock na raiz.
	writeLock func(root string, l *manifest.Lock) error
}

func defaultDeps() deps {
	return deps{git: runGit, writeFile: createFile, writeLock: manifest.Write}
}

func runGit(dir string, args ...string) (gitResult, error) {
	full := append([]string{"-c", "core.quotepath=off"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := gitResult{stdout: stdout.String(), stderr: strings.TrimSpace(stderr.String())}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.code = exit.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("adopt: git %s: %w", strings.Join(args, " "), err)
	}
	return res, nil
}

// Plan confere as pré-condições e monta o plano de adoção do projeto em
// opts.Root. Não grava nada. Cada pré-condição falha com erro próprio (TD-33).
func Plan(opts Options) (*Adoption, error) {
	return planWith(opts, defaultDeps())
}

func planWith(opts Options, d deps) (*Adoption, error) {
	root, err := checkRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	gomod, err := inspect(root, "go.mod")
	switch {
	case err != nil:
		return nil, err
	case gomod.kind == absent:
		return nil, fmt.Errorf("adopt: %s não tem go.mod; o adopt instala só em projeto Go (TD-07)", root)
	case gomod.kind == other:
		return nil, fmt.Errorf("adopt: %s", gomod.reason)
	}
	if err := checkGit(root, d); err != nil {
		return nil, err
	}
	if err := lockAbsent(root); err != nil {
		return nil, fmt.Errorf("%w; projeto adotado usa pipeline-claude sync", err)
	}
	if !scaffold.ValidVersion(opts.ToolkitVersion) {
		return nil, fmt.Errorf("adopt: versão do toolkit %q inválida: esperado vX.Y.Z (com pré-lançamento ou build opcionais)", opts.ToolkitVersion)
	}
	cfg, cfgItem, err := resolveConfig(root, opts, gomod.content, d)
	if err != nil {
		return nil, err
	}

	modules := slices.Clone(cfg.EnabledModules)
	slices.Sort(modules)
	a := &Adoption{
		Root:   root,
		Config: cfg,
		Lock: &manifest.Lock{
			Version:        manifest.Version,
			ToolkitVersion: opts.ToolkitVersion,
			Files:          map[string]string{},
			Released:       []string{},
		},
		root: root,
	}
	data := templates.Data{Modules: modules}
	for _, f := range templates.Expected(templates.Managed, modules) {
		it, err := planManaged(root, f, data)
		if err != nil {
			return nil, err
		}
		switch it.Action {
		case Criar:
			a.Lock.Files[it.Path] = manifest.Hash(it.content)
		case Manter:
			a.Lock.Files[it.Path] = it.hash
		case DoProjeto:
			a.Lock.Released = append(a.Lock.Released, it.Path)
		}
		a.Items = append(a.Items, it)
	}

	settings, err := planProject(root, settingsFile)
	if err != nil {
		return nil, err
	}
	mcp, err := planProject(root, mcpFile)
	if err != nil {
		return nil, err
	}
	a.Items = append(a.Items, settings.item, mcp.item, cfgItem)
	slices.SortFunc(a.Items, func(x, y Item) int { return strings.Compare(x.Path, y.Path) })

	if a.Instructions, err = instructions(root, opts.ToolkitVersion, cfg, settings, mcp, d); err != nil {
		return nil, err
	}
	return a, nil
}

// checkRoot exige root absoluto, limpo e diretório real (symlink recusado).
func checkRoot(root string) (string, error) {
	switch {
	case root == "":
		return "", errors.New("adopt: raiz do projeto vazia")
	case !filepath.IsAbs(root):
		return "", fmt.Errorf("adopt: raiz %q precisa ser caminho absoluto", root)
	case filepath.Clean(root) != root:
		return "", fmt.Errorf("adopt: raiz %q não está na forma limpa (%q)", root, filepath.Clean(root))
	}
	info, err := os.Lstat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("adopt: raiz %s não existe", root)
	case err != nil:
		return "", fmt.Errorf("adopt: falha ao verificar a raiz %s: %w", root, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("adopt: raiz %s é symlink; informe o diretório real", root)
	case !info.IsDir():
		return "", fmt.Errorf("adopt: raiz %s não é diretório", root)
	}
	return root, nil
}

// checkGit exige root dentro de um working tree git com git status
// --porcelain vazio. Falha do git é erro (TD-08).
func checkGit(root string, d deps) error {
	res, err := d.git(root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if res.code != 0 || strings.TrimSpace(res.stdout) != "true" {
		return fmt.Errorf("adopt: %s não está num working tree do git (%s); a adoção precisa ser um diff revisável", root, orNone(res.stderr))
	}
	res, err = d.git(root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if res.code != 0 {
		return fmt.Errorf("adopt: git status falhou (exit %d): %s", res.code, orNone(res.stderr))
	}
	if out := strings.TrimRight(res.stdout, "\n"); out != "" {
		lines := strings.Split(out, "\n")
		if len(lines) > 10 {
			lines = append(lines[:10], "...")
		}
		return fmt.Errorf("adopt: o working tree do git não está limpo; faça commit ou stash antes de adotar, para a adoção ser um diff revisável:\n%s", strings.Join(lines, "\n"))
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "sem stderr"
	}
	return s
}

// gitConfig lê uma chave do git config: set false se a chave não existe
// (exit 1); outra falha é erro.
func gitConfig(root, key string, d deps) (value string, set bool, err error) {
	res, err := d.git(root, "config", "--get", key)
	if err != nil {
		return "", false, err
	}
	switch res.code {
	case 0:
		return strings.TrimSpace(res.stdout), true, nil
	case 1:
		return "", false, nil
	}
	return "", false, fmt.Errorf("adopt: git config --get %s falhou (exit %d): %s", key, res.code, orNone(res.stderr))
}

// resolveConfig carrega o .pipeline-claude.json existente ou renderiza um do
// template, validado com as regras do config.Load, e devolve o item dele.
func resolveConfig(root string, opts Options, gomod []byte, d deps) (*config.Config, Item, error) {
	st, err := inspect(root, config.FileName)
	if err != nil {
		return nil, Item{}, err
	}
	switch st.kind {
	case other:
		return nil, Item{}, fmt.Errorf("adopt: %s", st.reason)
	case regular:
		cfg, err := config.Load(root)
		if err != nil {
			return nil, Item{}, fmt.Errorf("adopt: %s inválido; corrija antes de adotar: %w", config.FileName, err)
		}
		if cfg.Root() != root {
			return nil, Item{}, fmt.Errorf("adopt: %s carregado de %s, fora da raiz", config.FileName, cfg.Root())
		}
		if opts.Provider != "" && opts.Provider != cfg.GitProvider {
			return nil, Item{}, fmt.Errorf("adopt: --provider %s diverge do git_provider %q de %s; a config existente vale", opts.Provider, cfg.GitProvider, config.FileName)
		}
		if len(opts.Modules) > 0 && !sameSet(opts.Modules, cfg.EnabledModules) {
			return nil, Item{}, fmt.Errorf("adopt: --modules %s diverge do enabled_modules %v de %s; a config existente vale", strings.Join(opts.Modules, ","), cfg.EnabledModules, config.FileName)
		}
		return cfg, Item{Path: config.FileName, Action: Manter, Reason: "existe e é válido; é do projeto", planned: true}, nil
	}

	module, err := moduleFromGoMod(gomod)
	if err != nil {
		return nil, Item{}, fmt.Errorf("adopt: go.mod: %w", err)
	}
	name, err := normalizeName(module)
	if err != nil {
		return nil, Item{}, fmt.Errorf("adopt: nome do projeto a partir do módulo %q: %w; crie %s à mão", module, err, config.FileName)
	}
	provider := opts.Provider
	if provider == "" {
		if provider, err = detectProvider(root, d); err != nil {
			return nil, Item{}, err
		}
	}
	if provider != "github" && provider != "gitlab" {
		return nil, Item{}, fmt.Errorf("adopt: provedor %q não suportado: use github ou gitlab (TD-07)", provider)
	}
	modules, err := resolveModules(root, opts.Modules)
	if err != nil {
		return nil, Item{}, err
	}
	f, err := catalogFile(config.FileName)
	if err != nil {
		return nil, Item{}, err
	}
	content, err := templates.Render(f, templates.Data{
		Name: name, Module: module, Provider: provider, ToolkitVersion: opts.ToolkitVersion, Modules: modules,
	})
	if err != nil {
		return nil, Item{}, fmt.Errorf("adopt: %w", err)
	}
	cfg, err := config.Parse(content)
	if err != nil {
		return nil, Item{}, fmt.Errorf("adopt: %s gerado não passa na validação: %w", config.FileName, err)
	}
	return cfg, Item{
		Path: config.FileName, Action: Criar, Reason: "ausente; criado do template (provedor " + provider + ")",
		planned: true, content: content, mode: f.Mode.Perm(),
	}, nil
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// resolveModules valida os módulos dados ou, sem eles, devolve os módulos
// cujo subagente já existe em .claude/agents/.
func resolveModules(root string, given []string) ([]string, error) {
	if len(given) > 0 {
		mods := slices.Clone(given)
		slices.Sort(mods)
		for i, m := range mods {
			if !config.Modules[m] {
				return nil, fmt.Errorf("adopt: módulo opcional %q desconhecido", m)
			}
			if i > 0 && mods[i-1] == m {
				return nil, fmt.Errorf("adopt: módulo opcional %q repetido", m)
			}
		}
		return mods, nil
	}
	var mods []string
	for m := range config.Modules {
		st, err := inspect(root, ".claude/agents/"+m+".md")
		if err != nil {
			return nil, err
		}
		if st.kind != absent {
			mods = append(mods, m)
		}
	}
	slices.Sort(mods)
	return mods, nil
}

// detectProvider deduz o provedor do host do remote origin.
func detectProvider(root string, d deps) (string, error) {
	remote, set, err := gitConfig(root, "remote.origin.url", d)
	if err != nil {
		return "", err
	}
	if !set || remote == "" {
		return "", errors.New("adopt: sem remote origin para deduzir o provedor; informe --provider github|gitlab")
	}
	p, err := providerFromURL(remote)
	if err != nil {
		return "", fmt.Errorf("adopt: %w; informe --provider github|gitlab", err)
	}
	return p, nil
}

// providerFromURL devolve o provedor pelo host da URL do remote: github.com é
// github; gitlab.com ou host com "gitlab" é gitlab. Aceita URL com esquema e a
// forma scp do git ([usuário@]host:caminho). A URL não entra no erro: pode
// carregar credencial.
func providerFromURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	var host string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", errors.New("URL do remote origin ilegível")
		}
		host = u.Hostname()
	} else if i := strings.Index(raw, ":"); i > 0 && !strings.Contains(raw[:i], "/") {
		host = raw[:i]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}
	host = strings.ToLower(host)
	switch {
	case host == "":
		return "", errors.New("remote origin sem host (caminho local?)")
	case host == "github.com":
		return "github", nil
	case host == "gitlab.com", strings.Contains(host, "gitlab"):
		return "gitlab", nil
	}
	return "", fmt.Errorf("host %q do remote origin não é github.com nem GitLab", host)
}

// moduleFromGoMod devolve o caminho da diretiva module do go.mod.
func moduleFromGoMod(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok {
			continue
		}
		if rest != "" && !strings.ContainsAny(rest[:1], " \t\"`") {
			continue // outra palavra começando com module
		}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "`") {
			s, err := strconv.Unquote(rest)
			if err != nil {
				return "", fmt.Errorf("diretiva module com aspas inválidas: %q", line)
			}
			rest = s
		}
		if rest == "" || rest == "(" || strings.ContainsAny(rest, " \t") {
			return "", fmt.Errorf("diretiva module inválida: %q", line)
		}
		return rest, nil
	}
	return "", errors.New("sem diretiva module")
}

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	majorRe = regexp.MustCompile(`^v[0-9]+$`)
)

// normalizeName devolve o nome do projeto a partir do último elemento do
// módulo (sem o sufixo de versão maior /vN): minúsculas, "_" e "." viram "-",
// hífens repetidos ou nas pontas somem. Outro caractere, ou resultado fora de
// ^[a-z][a-z0-9-]*$, é erro.
func normalizeName(module string) (string, error) {
	elems := strings.Split(module, "/")
	last := elems[len(elems)-1]
	if n := len(elems); n >= 2 && majorRe.MatchString(last) && !strings.Contains(elems[n-2], ".") {
		last = elems[n-2]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(last) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteByte('-')
		default:
			return "", fmt.Errorf("caractere %q fora de [a-z0-9-]", r)
		}
	}
	name := b.String()
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("nome %q fora de ^[a-z][a-z0-9-]*$", name)
	}
	return name, nil
}

// catalogFile devolve a entrada do catálogo com destino rel.
func catalogFile(rel string) (templates.File, error) {
	for _, f := range templates.Catalog() {
		if f.Path == rel {
			return f, nil
		}
	}
	return templates.File{}, fmt.Errorf("adopt: %s fora do catálogo", rel)
}

// planManaged classifica um arquivo gerenciado pelo TD-32.
func planManaged(root string, f templates.File, data templates.Data) (Item, error) {
	rel, err := templates.DestPath(f, data)
	if err != nil {
		return Item{}, fmt.Errorf("adopt: %w", err)
	}
	if !manifest.ManagedArea(rel) {
		return Item{}, fmt.Errorf("adopt: catálogo: %s fora da área gerenciada", rel)
	}
	content, err := templates.Render(f, data)
	if err != nil {
		return Item{}, fmt.Errorf("adopt: %w", err)
	}
	st, err := inspect(root, rel)
	if err != nil {
		return Item{}, err
	}
	it := Item{Path: rel, planned: true, managed: true}
	switch st.kind {
	case absent:
		it.Action, it.Reason = Criar, "ausente; criado do template"
		it.content, it.mode = content, f.Mode.Perm()
	case other:
		return Item{}, fmt.Errorf("adopt: %s: %s; resolva antes de adotar", rel, st.reason)
	default:
		it.hash = manifest.Hash(st.content)
		if it.hash == manifest.Hash(content) {
			it.Action, it.Reason = Manter, "igual ao template; gerenciado (files)"
		} else {
			it.Action, it.Reason = DoProjeto, "diferente do template; fica do projeto (released), o sync não o toca"
		}
	}
	return it, nil
}

// projectFile é um arquivo do projeto que o adopt cria se faltar.
type projectFile struct {
	item  Item
	state state
	tmpl  []byte // template renderizado
}

// planProject classifica settings.json ou .mcp.json: ausente é criado;
// existente é do projeto e nunca é alterado.
func planProject(root, rel string) (projectFile, error) {
	f, err := catalogFile(rel)
	if err != nil {
		return projectFile{}, err
	}
	content, err := templates.Render(f, templates.Data{})
	if err != nil {
		return projectFile{}, fmt.Errorf("adopt: %w", err)
	}
	st, err := inspect(root, rel)
	if err != nil {
		return projectFile{}, err
	}
	pf := projectFile{state: st, tmpl: content, item: Item{Path: rel, planned: true}}
	if st.kind == absent {
		pf.item.Action, pf.item.Reason = Criar, "ausente; criado do template"
		pf.item.content, pf.item.mode = content, f.Mode.Perm()
		return pf, nil
	}
	pf.item.Action, pf.item.Reason = Manter, "existe; é do projeto e não é alterado"
	return pf, nil
}

// settingsJSON é a parte do settings.json que o adopt confere.
type settingsJSON struct {
	Permissions struct {
		Deny []string `json:"deny"`
	} `json:"permissions"`
	Hooks map[string][]hookEntry `json:"hooks"`
}

type hookEntry struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookCommand `json:"hooks"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// instructions monta os trechos que o projeto precisa acrescentar à mão.
func instructions(root, version string, cfg *config.Config, settings, mcp projectFile, d deps) ([]Instruction, error) {
	steps := []func() ([]Instruction, error){
		func() ([]Instruction, error) { return settingsInstructions(root, settings) },
		func() ([]Instruction, error) { return mcpInstructions(mcp) },
		func() ([]Instruction, error) { return makefileInstructions(root, version) },
		func() ([]Instruction, error) { return claudeInstructions(root, cfg.GitProvider) },
		func() ([]Instruction, error) { return gitignoreInstructions(root) },
		func() ([]Instruction, error) { return hooksPathInstructions(root, d) },
	}
	var out []Instruction
	for _, step := range steps {
		in, err := step()
		if err != nil {
			return nil, err
		}
		out = append(out, in...)
	}
	return out, nil
}

// notRegular é a instrução para arquivo do projeto que não é arquivo regular
// (symlink, diretório): o adopt não lê através dele.
func notRegular(rel string, st state, what, snippet string) Instruction {
	return Instruction{
		Path:    rel,
		Reason:  st.reason + "; o adopt não lê através dele: confira à mão se tem " + what,
		Snippet: snippet,
	}
}

func marshalSnippet(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("adopt: falha ao montar o trecho: %w", err)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// settingsInstructions confere o hook e o fmt-hook com as regras do doctor e
// as regras de permissions.deny do template.
func settingsInstructions(root string, pf projectFile) ([]Instruction, error) {
	switch pf.state.kind {
	case absent:
		return nil, nil
	case other:
		return []Instruction{notRegular(settingsFile, pf.state, "o hook, o fmt-hook e o permissions.deny", string(pf.tmpl))}, nil
	}
	var tmpl, cur settingsJSON
	if err := json.Unmarshal(pf.tmpl, &tmpl); err != nil {
		return nil, fmt.Errorf("adopt: template de %s ilegível: %w", settingsFile, err)
	}
	if err := json.Unmarshal(pf.state.content, &cur); err != nil {
		return nil, fmt.Errorf("adopt: %s ilegível: %w; corrija o JSON antes de adotar", settingsFile, err)
	}

	var out []Instruction
	hook, fmtHook := doctor.SettingsHooks(root)
	for _, h := range []struct {
		check doctor.Check
		event string
		sub   string
	}{{hook, "PreToolUse", "hook"}, {fmtHook, "PostToolUse", "fmt-hook"}} {
		if h.check.Status == doctor.StatusOK {
			continue
		}
		entries := tmpl.Hooks[h.event]
		if len(entries) == 0 {
			return nil, fmt.Errorf("adopt: template de %s sem %s", settingsFile, h.event)
		}
		snippet, err := marshalSnippet(map[string]any{"hooks": map[string]any{h.event: entries}})
		if err != nil {
			return nil, err
		}
		reason := h.check.Message + "; acrescente o registro em hooks." + h.event
		if others := otherCommands(cur, h.event, h.sub); len(others) > 0 {
			reason += "; " + h.event + " já tem outro hook (" + strings.Join(others, ", ") + "): o projeto decide se o remove"
		}
		out = append(out, Instruction{Path: settingsFile, Reason: reason, Snippet: snippet})
	}

	var missing []string
	for _, rule := range tmpl.Permissions.Deny {
		if !slices.Contains(cur.Permissions.Deny, rule) {
			missing = append(missing, rule)
		}
	}
	if len(missing) > 0 {
		snippet, err := marshalSnippet(map[string]any{"permissions": map[string]any{"deny": missing}})
		if err != nil {
			return nil, err
		}
		out = append(out, Instruction{
			Path:    settingsFile,
			Reason:  "permissions.deny sem " + strings.Join(missing, ", ") + "; acrescente às regras existentes (TD-26)",
			Snippet: snippet,
		})
	}
	return out, nil
}

// otherCommands lista, ordenados e sem repetição, os comandos registrados em
// event que não são pipeline-claude <sub>.
func otherCommands(s settingsJSON, event, sub string) []string {
	var out []string
	for _, en := range s.Hooks[event] {
		for _, h := range en.Hooks {
			if !doctor.IsToolkitCommand(h.Command, sub) {
				out = append(out, h.Command)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// mcpInstructions confere se .mcp.json registra pipeline-claude mcp.
func mcpInstructions(pf projectFile) ([]Instruction, error) {
	snippet := strings.TrimRight(string(pf.tmpl), "\n")
	switch pf.state.kind {
	case absent:
		return nil, nil
	case other:
		return []Instruction{notRegular(mcpFile, pf.state, "o servidor pipeline-claude", snippet)}, nil
	}
	var cur struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(pf.state.content, &cur); err != nil {
		return nil, fmt.Errorf("adopt: %s ilegível: %w; corrija o JSON antes de adotar", mcpFile, err)
	}
	for _, s := range cur.Servers {
		if doctor.IsToolkitCommand(strings.Join(append([]string{s.Command}, s.Args...), " "), "mcp") {
			return nil, nil
		}
	}
	return []Instruction{{
		Path:    mcpFile,
		Reason:  "não registra o servidor pipeline-claude mcp; acrescente a entrada em mcpServers",
		Snippet: snippet,
	}}, nil
}

// makefileInstructions confere a versão fixada no Makefile (TD-06), lida como
// o doctor lê.
func makefileInstructions(root, version string) ([]Instruction, error) {
	st, err := inspect(root, "Makefile")
	if err != nil {
		return nil, err
	}
	f, err := catalogFile("Makefile")
	if err != nil {
		return nil, err
	}
	out, err := templates.Render(f, templates.Data{Name: "projeto", ToolkitVersion: version})
	if err != nil {
		return nil, fmt.Errorf("adopt: %w", err)
	}
	snippet, err := makefileBlock(string(out))
	if err != nil {
		return nil, err
	}
	switch st.kind {
	case absent:
		return []Instruction{{Path: "Makefile", Reason: "ausente; crie com a versão do toolkit e o alvo tools (make tools instala bin/pipeline-claude)", Snippet: snippet}}, nil
	case other:
		return []Instruction{notRegular("Makefile", st, "PIPELINE_CLAUDE_VERSION e o alvo tools", snippet)}, nil
	}
	declared, err := doctor.DeclaredVersion(root)
	if err != nil {
		return nil, fmt.Errorf("adopt: %w", err)
	}
	switch declared {
	case version:
		return nil, nil
	case "":
		return []Instruction{{Path: "Makefile", Reason: "sem PIPELINE_CLAUDE_VERSION; acrescente a versão do toolkit e o alvo tools", Snippet: snippet}}, nil
	}
	return []Instruction{{
		Path:    "Makefile",
		Reason:  fmt.Sprintf("fixa a versão %s do toolkit e a adoção grava %s no lock; ajuste PIPELINE_CLAUDE_VERSION", declared, version),
		Snippet: snippet,
	}}, nil
}

// makefileBlock recorta do Makefile do template o bloco da versão do toolkit
// até a receita do alvo tools.
func makefileBlock(text string) (string, error) {
	start := strings.Index(text, "# Versão do pipeline-claude")
	if start < 0 {
		return "", errors.New("adopt: template do Makefile sem o bloco PIPELINE_CLAUDE_VERSION")
	}
	rest := text[start:]
	i := strings.Index(rest, "\ntools:\n")
	if i < 0 {
		return "", errors.New("adopt: template do Makefile sem o alvo tools")
	}
	i += len("\ntools:\n")
	j := strings.Index(rest[i:], "\n")
	if j < 0 {
		return "", errors.New("adopt: template do Makefile sem a receita do alvo tools")
	}
	return rest[:i+j], nil
}

// claudeHeading abre a seção da pipeline no CLAUDE.md do template.
const claudeHeading = "## Como o trabalho acontece aqui"

// claudeInstructions confere a seção da pipeline no CLAUDE.md.
func claudeInstructions(root, provider string) ([]Instruction, error) {
	st, err := inspect(root, "CLAUDE.md")
	if err != nil {
		return nil, err
	}
	f, err := catalogFile("CLAUDE.md")
	if err != nil {
		return nil, err
	}
	out, err := templates.Render(f, templates.Data{Name: "projeto", Summary: "Projeto.", Provider: provider})
	if err != nil {
		return nil, fmt.Errorf("adopt: %w", err)
	}
	i := strings.Index(string(out), claudeHeading)
	if i < 0 {
		return nil, errors.New("adopt: template do CLAUDE.md sem a seção da pipeline")
	}
	snippet := strings.TrimRight(string(out[i:]), "\n")
	switch st.kind {
	case absent:
		return []Instruction{{Path: "CLAUDE.md", Reason: "ausente; crie com a seção da pipeline (título e resumo são do projeto)", Snippet: snippet}}, nil
	case other:
		return []Instruction{notRegular("CLAUDE.md", st, "a seção "+claudeHeading, snippet)}, nil
	}
	for _, line := range strings.Split(string(st.content), "\n") {
		if strings.TrimSpace(line) == claudeHeading {
			return nil, nil
		}
	}
	return []Instruction{{
		Path:    "CLAUDE.md",
		Reason:  "sem a seção da pipeline; acrescente o trecho, o resto do arquivo continua do projeto",
		Snippet: snippet,
	}}, nil
}

// gitignoreInstructions confere bin/ (onde make tools instala o binário) no
// .gitignore.
func gitignoreInstructions(root string) ([]Instruction, error) {
	st, err := inspect(root, ".gitignore")
	if err != nil {
		return nil, err
	}
	const snippet = "bin/"
	switch st.kind {
	case absent:
		return []Instruction{{Path: ".gitignore", Reason: "ausente; crie ignorando bin/ (make tools instala bin/pipeline-claude)", Snippet: snippet}}, nil
	case other:
		return []Instruction{notRegular(".gitignore", st, "bin/", snippet)}, nil
	}
	for _, line := range strings.Split(string(st.content), "\n") {
		switch strings.TrimSpace(line) {
		case "bin/", "/bin/", "bin", "/bin":
			return nil, nil
		}
	}
	return []Instruction{{Path: ".gitignore", Reason: "sem bin/; acrescente a linha (make tools instala bin/pipeline-claude)", Snippet: snippet}}, nil
}

// hooksPathInstructions confere core.hooksPath = .githooks (pre-push
// gerenciado, TD-26).
func hooksPathInstructions(root string, d deps) ([]Instruction, error) {
	value, set, err := gitConfig(root, "core.hooksPath", d)
	if err != nil {
		return nil, err
	}
	if set && path.Clean(filepath.ToSlash(value)) == ".githooks" {
		return nil, nil
	}
	reason := "core.hooksPath não configurado; o pre-push gerenciado (.githooks/pre-push) não roda"
	if set {
		reason = fmt.Sprintf("core.hooksPath é %q; o pre-push gerenciado (.githooks/pre-push) não roda", value)
	}
	return []Instruction{{Path: hooksPathKey, Reason: reason, Snippet: "git config core.hooksPath .githooks"}}, nil
}

type kind int

const (
	absent kind = iota
	regular
	other
)

// state é o que há no disco num caminho.
type state struct {
	kind    kind
	content []byte // se regular
	reason  string // se other
}

// inspect olha rel (relativo, com "/") sob root sem seguir symlink: Lstat em
// cada componente. Symlink em qualquer componente, componente intermediário
// que não é diretório ou destino que não é arquivo regular dão other. Erro de
// acesso é erro (falha fechada).
func inspect(root, rel string) (state, error) {
	if _, err := confine(root, rel); err != nil {
		return state{}, err
	}
	parts := strings.Split(rel, "/")
	cur := root
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		shown := strings.Join(parts[:i+1], "/")
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return state{kind: absent}, nil
		case err != nil:
			return state{}, fmt.Errorf("adopt: falha ao verificar %s: %w", shown, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return state{kind: other, reason: shown + " é symlink"}, nil
		case i < len(parts)-1 && !info.IsDir():
			return state{kind: other, reason: shown + " não é diretório"}, nil
		case i == len(parts)-1 && !info.Mode().IsRegular():
			return state{kind: other, reason: shown + " não é arquivo regular"}, nil
		}
	}
	data, err := os.ReadFile(cur)
	if err != nil {
		return state{}, fmt.Errorf("adopt: falha ao ler %s: %w", rel, err)
	}
	return state{kind: regular, content: data}, nil
}

// confine devolve o caminho de rel sob root, conferido dentro de root depois
// de filepath.Clean (TD-17).
func confine(root, rel string) (string, error) {
	if rel == "" || path.IsAbs(rel) || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || path.Clean(rel) != rel {
		return "", fmt.Errorf("adopt: caminho %q inválido; nada foi gravado", rel)
	}
	p := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	r, err := filepath.Rel(root, p)
	if err != nil || r == "." || !filepath.IsLocal(r) {
		return "", fmt.Errorf("adopt: %s sai da raiz do projeto; nada foi gravado", rel)
	}
	return p, nil
}

// Apply grava o plano: os arquivos criar, depois a config (se for criada) e
// por último o lock. Antes da primeira escrita confere o plano inteiro contra
// o disco (criar ainda ausente, gerenciado mantido ou do projeto com o mesmo
// hash, lock ausente), e de novo cada caminho logo antes de gravá-lo.
// Qualquer falha remove o que esta execução criou, arquivos e diretórios,
// nunca o que já existia, e devolve erro com a lista do que foi removido.
func Apply(a *Adoption) (Result, error) {
	return applyWith(a, defaultDeps())
}

func applyWith(a *Adoption, d deps) (Result, error) {
	if a == nil {
		return Result{}, errors.New("adopt: plano nil; nada foi criado")
	}
	if a.Lock == nil {
		return Result{}, errors.New("adopt: plano sem lock; nada foi criado")
	}
	if a.root == "" || a.Root != a.root {
		return Result{}, errors.New("adopt: plano não veio de Plan para esta raiz; nada foi criado")
	}
	root, err := checkRoot(a.Root)
	if err != nil {
		return Result{}, err
	}
	seen := map[string]bool{}
	var files []Item
	var cfgItem *Item
	for i, it := range a.Items {
		if !it.planned {
			return Result{}, fmt.Errorf("adopt: item %s (%s) não veio de Plan; nada foi criado", it.Path, it.Action)
		}
		if _, err := confine(root, it.Path); err != nil {
			return Result{}, err
		}
		if seen[it.Path] {
			return Result{}, fmt.Errorf("adopt: %s repetido no plano; nada foi criado", it.Path)
		}
		seen[it.Path] = true
		if err := recheck(root, it); err != nil {
			return Result{}, fmt.Errorf("%w; o disco mudou desde o plano, nada foi criado", err)
		}
		switch {
		case it.Action != Criar:
		case it.Path == config.FileName:
			cfgItem = &a.Items[i]
		default:
			files = append(files, it)
		}
	}
	if err := lockAbsent(root); err != nil {
		return Result{}, fmt.Errorf("%w; nada foi criado", err)
	}
	if cfgItem != nil {
		files = append(files, *cfgItem)
	}

	var created []string // absolutos, na ordem de criação (arquivos e diretórios)
	rollback := func(cause error) error {
		var removed []string
		var errs []error
		for _, p := range slices.Backward(created) {
			if err := os.Remove(p); err != nil {
				errs = append(errs, err)
				continue
			}
			rel, _ := filepath.Rel(root, p)
			removed = append(removed, filepath.ToSlash(rel))
		}
		msg := "nada foi criado"
		if len(removed) > 0 {
			msg = "removido o que esta execução criou: " + strings.Join(removed, ", ")
		}
		err := fmt.Errorf("%w; %s", cause, msg)
		if len(errs) > 0 {
			err = errors.Join(append([]error{err, errors.New("adopt: limpeza incompleta")}, errs...)...)
		}
		return err
	}

	var res Result
	for _, it := range files {
		// Conferência de novo, logo antes de gravar.
		if err := recheck(root, it); err != nil {
			return Result{}, rollback(err)
		}
		target, err := confine(root, it.Path)
		if err != nil {
			return Result{}, rollback(err)
		}
		dirs, err := mkdirs(root, path.Dir(it.Path))
		created = append(created, dirs...)
		if err != nil {
			return Result{}, rollback(err)
		}
		if err := d.writeFile(target, it.content, it.mode); err != nil {
			return Result{}, rollback(fmt.Errorf("adopt: falha ao criar %s: %w", it.Path, err))
		}
		created = append(created, target)
		res.Created = append(res.Created, it.Path)
	}
	if err := lockAbsent(root); err != nil {
		return Result{}, rollback(err)
	}
	if err := d.writeLock(root, a.Lock); err != nil {
		return Result{}, rollback(fmt.Errorf("adopt: %w", err))
	}
	res.Created = append(res.Created, manifest.FileName)
	slices.Sort(res.Created)
	return res, nil
}

// recheck confere it contra o disco: criar ainda ausente; gerenciado mantido
// ou do projeto com o mesmo hash do plano.
func recheck(root string, it Item) error {
	if it.Action != Criar && !it.managed {
		return nil
	}
	st, err := inspect(root, it.Path)
	if err != nil {
		return err
	}
	switch {
	case it.Action == Criar && st.kind == other:
		return fmt.Errorf("adopt: %s: %s", it.Path, st.reason)
	case it.Action == Criar && st.kind != absent:
		return fmt.Errorf("adopt: %s apareceu no disco; nada foi sobrescrito", it.Path)
	case it.Action == Criar:
		return nil
	case st.kind != regular || manifest.Hash(st.content) != it.hash:
		return fmt.Errorf("adopt: %s mudou desde o plano", it.Path)
	}
	return nil
}

// lockAbsent exige que o lock não exista, em qualquer forma.
func lockAbsent(root string) error {
	_, err := os.Lstat(filepath.Join(root, manifest.FileName))
	switch {
	case err == nil:
		return fmt.Errorf("adopt: %s já existe", manifest.FileName)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("adopt: falha ao verificar %s: %w", manifest.FileName, err)
	}
	return nil
}

// mkdirs garante os diretórios de relDir sob root, componente a componente,
// sem seguir symlink, e devolve os que criou, em ordem. Em erro, devolve os
// que criou até ali.
func mkdirs(root, relDir string) ([]string, error) {
	if relDir == "." {
		return nil, nil
	}
	var created []string
	cur := root
	for _, part := range strings.Split(relDir, "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return created, fmt.Errorf("adopt: falha ao criar %s: %w", cur, err)
			}
			created = append(created, cur)
		case err != nil:
			return created, fmt.Errorf("adopt: falha ao verificar %s: %w", cur, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return created, fmt.Errorf("adopt: %s é symlink; nada foi gravado através dele", cur)
		case !info.IsDir():
			return created, fmt.Errorf("adopt: %s não é diretório", cur)
		}
	}
	return created, nil
}

// createFile cria name em modo exclusivo (nunca sobrescreve nem segue
// symlink) e fixa a permissão em mode, independente da umask. Falha depois de
// criar remove o arquivo.
func createFile(name string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(mode)
	}
	if werr = errors.Join(werr, f.Close()); werr != nil {
		if rerr := os.Remove(name); rerr != nil {
			werr = errors.Join(werr, rerr)
		}
	}
	return werr
}
