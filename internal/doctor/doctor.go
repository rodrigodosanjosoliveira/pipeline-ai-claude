// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/doctor/doctor.go @ 0eac73a (MIT).
// Alterações: checagens do Claude Code (hook no .claude/settings.json, TD-02) no
// lugar de .agents e pre-push; versão mínima do Go lida do go.mod do projeto, com
// GOTOOLCHAIN=local; gh ou glab conforme git_provider (TD-07); binário com a
// versão fixada no Makefile (TD-06); aviso de filesystem case-insensitive (TD-09);
// status ok/aviso/erro com ação corretiva; comandos externos injetáveis.

// Package doctor diagnostica se o ambiente e o projeto estão prontos para o
// toolkit: ferramentas no PATH, configuração, registro do hook e binário.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// Status é o resultado de uma checagem.
type Status string

// Valores de Status. Só StatusError reprova o doctor.
const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "aviso"
	StatusError Status = "erro"
)

// Check é o resultado de uma checagem, com a ação corretiva quando não está ok.
type Check struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
}

// Nomes das checagens, na ordem em que rodam.
const (
	checkGo       = "go"
	checkGit      = "git"
	checkProvider = "provedor git"
	checkConfig   = "configuração"
	checkHook     = "hook"
	checkBinary   = "binário"
	checkCaseFS   = "filesystem"
)

// editTools são as ferramentas que o matcher do hook precisa cobrir.
var editTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit"}

// cmdTimeout limita cada comando externo.
const cmdTimeout = 10 * time.Second

// env isola o acesso a PATH e a comandos externos.
type env struct {
	lookPath func(name string) (string, error)
	output   func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func defaultEnv() env {
	return env{
		lookPath: exec.LookPath,
		output: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return newCmd(ctx, name, args...).Output()
		},
	}
}

// newCmd monta o comando com GOTOOLCHAIN=local: sem isso, rodar o go dentro de
// um módulo com versão maior baixaria outro toolchain e a checagem mentiria.
func newCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	return cmd
}

// Collect roda todas as checagens a partir de root (ou da raiz do projeto
// acima dele, onde está a configuração).
func Collect(root string) []Check {
	return collect(root, defaultEnv())
}

// Run imprime o diagnóstico em stdout, legível ou em JSON
// ({"ok": bool, "checks": [...]}). Devolve 1 se alguma checagem der erro.
func Run(root string, asJSON bool, stdout io.Writer) int {
	return run(root, asJSON, stdout, defaultEnv())
}

func run(root string, asJSON bool, stdout io.Writer, e env) int {
	checks := collect(root, e)
	var errs, warns int
	for _, c := range checks {
		switch c.Status {
		case StatusError:
			errs++
		case StatusWarn:
			warns++
		}
	}
	code := 0
	if errs > 0 {
		code = 1
	}

	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		rep := struct {
			OK     bool    `json:"ok"`
			Checks []Check `json:"checks"`
		}{errs == 0, checks}
		if err := enc.Encode(rep); err != nil {
			return 1
		}
		return code
	}

	fmt.Fprintln(stdout, "pipeline-claude doctor")
	fmt.Fprintln(stdout)
	for _, c := range checks {
		fmt.Fprintf(stdout, "  %-7s %-14s %s\n", "["+string(c.Status)+"]", c.Name, c.Message)
		if c.Action != "" {
			fmt.Fprintf(stdout, "  %-7s %-14s → %s\n", "", "", c.Action)
		}
	}
	fmt.Fprintf(stdout, "\nResultado: %d erro(s), %d aviso(s).\n", errs, warns)
	return code
}

func collect(start string, e env) []Check {
	cfg, cfgErr := config.Load(start)
	root := start
	if cfgErr == nil {
		root = cfg.Root()
	} else if abs, err := filepath.Abs(start); err == nil {
		root = abs
	}

	return []Check{
		goCheck(root, e),
		toolCheck(checkGit, "git", StatusError, "instale o git e deixe-o no PATH", e),
		providerCheck(cfg, cfgErr, e),
		configCheck(cfgErr),
		hookCheck(root),
		binaryCheck(root, e),
		caseFSCheck(root),
	}
}

func ok(name, msg string) Check { return Check{Name: name, Status: StatusOK, Message: msg} }

func bad(name string, st Status, msg, action string) Check {
	return Check{Name: name, Status: st, Message: msg, Action: action}
}

var (
	goVersionRe = regexp.MustCompile(`go version go(\d+(?:\.\d+){1,2})`)
	goModRe     = regexp.MustCompile(`(?m)^go[ \t]+(\S+)[ \t]*(?://.*)?$`)
	semverRe    = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)
)

// parseGoVersion lê a versão (major, minor, patch) da saída de "go version".
// Pré-lançamentos (go1.25rc1) contam como a versão base.
func parseGoVersion(out string) ([3]int, bool) {
	m := goVersionRe.FindStringSubmatch(out)
	if m == nil {
		return [3]int{}, false
	}
	return parseVersion(m[1])
}

// parseVersion lê "1.24" ou "1.24.3".
func parseVersion(s string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i, part := range m[1:] {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func less(a, b [3]int) bool { return slices.Compare(a[:], b[:]) < 0 }

func goCheck(root string, e env) Check {
	if _, err := e.lookPath("go"); err != nil {
		return bad(checkGo, StatusError, "go não encontrado no PATH", "instale o Go (https://go.dev/dl) e deixe-o no PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := e.output(ctx, "go", "version")
	if err != nil {
		return bad(checkGo, StatusError, fmt.Sprintf("go version falhou: %v", err), "confira a instalação do Go")
	}
	raw := strings.TrimSpace(string(out))
	have, okHave := parseGoVersion(raw)
	if !okHave {
		return bad(checkGo, StatusError, fmt.Sprintf("versão do Go não reconhecida: %q", raw), "confira a instalação do Go")
	}

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return bad(checkGo, StatusWarn, raw+"; projeto sem go.mod, versão mínima não verificada", "rode o doctor na raiz do projeto Go")
	}
	if err != nil {
		return bad(checkGo, StatusError, fmt.Sprintf("falha ao ler go.mod: %v", err), "confira as permissões do go.mod")
	}
	m := goModRe.FindSubmatch(data)
	if m == nil {
		return bad(checkGo, StatusWarn, raw+"; go.mod sem diretiva go, versão mínima não verificada", "declare a versão com `go mod edit -go=<versão>`")
	}
	want, okWant := parseVersion(string(m[1]))
	if !okWant {
		return bad(checkGo, StatusError, fmt.Sprintf("diretiva go ilegível no go.mod: %q", m[1]), "corrija a diretiva go do go.mod")
	}
	if less(have, want) {
		return bad(checkGo, StatusError, fmt.Sprintf("%s é anterior ao go %s do go.mod", raw, m[1]), "atualize o Go para a versão do go.mod ou mais nova")
	}
	return ok(checkGo, fmt.Sprintf("%s (go.mod pede %s)", raw, m[1]))
}

func toolCheck(name, bin string, missing Status, action string, e env) Check {
	p, err := e.lookPath(bin)
	if err != nil {
		return bad(name, missing, bin+" não encontrado no PATH", action)
	}
	return ok(name, p)
}

func providerCheck(cfg *config.Config, cfgErr error, e env) Check {
	if cfgErr != nil {
		return bad(checkProvider, StatusWarn, "não verificado: configuração ausente ou inválida", "corrija a configuração (ver checagem abaixo)")
	}
	bin := map[string]string{"github": "gh", "gitlab": "glab"}[cfg.GitProvider]
	c := toolCheck(checkProvider, bin, StatusWarn, "instale o "+bin+" para abrir PR/MR pela linha de comando", e)
	c.Message = cfg.GitProvider + ": " + c.Message
	return c
}

func configCheck(err error) Check {
	switch {
	case errors.Is(err, config.ErrNotFound):
		return bad(checkConfig, StatusError, config.FileName+" não encontrado", "crie "+config.FileName+" na raiz do projeto")
	case err != nil:
		return bad(checkConfig, StatusError, err.Error(), "corrija "+config.FileName+" conforme a mensagem")
	}
	return ok(checkConfig, config.FileName+" válido")
}

// settingsFile é o arquivo de configuração do Claude Code versionado no projeto.
const settingsFile = ".claude/settings.json"

type settings struct {
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

const hookSnippet = `registre em ` + settingsFile + `: {"hooks": {"PreToolUse": [{"matcher": "Edit|Write|MultiEdit|NotebookEdit", "hooks": [{"type": "command", "command": "\"$CLAUDE_PROJECT_DIR\"/bin/pipeline-claude hook"}]}]}}`

func hookCheck(root string) Check {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(settingsFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return bad(checkHook, StatusError, settingsFile+" não encontrado", hookSnippet)
	}
	if err != nil {
		return bad(checkHook, StatusError, fmt.Sprintf("falha ao ler %s: %v", settingsFile, err), "confira as permissões de "+settingsFile)
	}
	var s settings
	if err := json.Unmarshal(data, &s); err != nil {
		return bad(checkHook, StatusError, fmt.Sprintf("%s ilegível: %v", settingsFile, err), "corrija o JSON de "+settingsFile)
	}

	covered := map[string]bool{}
	var wrongEvents []string
	for event, entries := range s.Hooks {
		for _, en := range entries {
			if !slices.ContainsFunc(en.Hooks, func(h hookCommand) bool {
				return h.Type == "command" && isHookCommand(h.Command)
			}) {
				continue
			}
			if event != "PreToolUse" {
				wrongEvents = append(wrongEvents, event)
				continue
			}
			for _, tool := range editTools {
				if matcherCovers(en.Matcher, tool) {
					covered[tool] = true
				}
			}
		}
	}
	slices.Sort(wrongEvents)
	wrongEvents = slices.Compact(wrongEvents)
	if len(wrongEvents) > 0 {
		return bad(checkHook, StatusError,
			"pipeline-claude hook registrado em "+strings.Join(wrongEvents, ", ")+"; lá ele bloqueia toda edição",
			"mova o registro para PreToolUse em "+settingsFile)
	}
	if len(covered) == 0 {
		return bad(checkHook, StatusError, "pipeline-claude hook não registrado em PreToolUse", hookSnippet)
	}
	var missing []string
	for _, tool := range editTools {
		if !covered[tool] {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return bad(checkHook, StatusError, "matcher do hook não cobre "+strings.Join(missing, ", "),
			`use "matcher": "Edit|Write|MultiEdit|NotebookEdit" em `+settingsFile)
	}
	return ok(checkHook, "registrado em PreToolUse para "+strings.Join(editTools, "|"))
}

// isHookCommand reconhece "pipeline-claude hook", com o binário por caminho
// e aspas opcionais, e nada além.
func isHookCommand(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) != 2 || f[1] != "hook" {
		return false
	}
	bin := strings.NewReplacer(`"`, "", `'`, "").Replace(f[0])
	base := filepath.Base(filepath.FromSlash(bin))
	return base == "pipeline-claude" || base == "pipeline-claude.exe"
}

// matcherCovers diz se o matcher do Claude Code casa a ferramenta: vazio ou
// "*" casam tudo; o resto é expressão regular sobre o nome inteiro.
func matcherCovers(matcher, tool string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	re, err := regexp.Compile(`^(?:` + matcher + `)$`)
	if err != nil {
		return false
	}
	return re.MatchString(tool)
}

var (
	makeVarRe     = regexp.MustCompile(`(?m)^[ \t]*PIPELINE_CLAUDE_VERSION[ \t]*(?::{1,3}=|\?=|=)[ \t]*(\S+)[ \t]*$`)
	makeInstallRe = regexp.MustCompile(`pipeline-claude@([^\s$()"']+)`)
	binVersionRe  = regexp.MustCompile(`^pipeline-claude (\S+)$`)
)

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "pipeline-claude.exe"
	}
	return "pipeline-claude"
}

// declaredVersion lê a versão do toolkit fixada no Makefile do projeto
// (TD-06). Vazio se não houver Makefile ou versão declarada.
func declaredVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("falha ao ler Makefile: %w", err)
	}
	if m := makeVarRe.FindSubmatch(data); m != nil {
		return string(m[1]), nil
	}
	if m := makeInstallRe.FindSubmatch(data); m != nil {
		return string(m[1]), nil
	}
	return "", nil
}

func binaryCheck(root string, e env) Check {
	rel := "bin/" + binaryName()
	path := filepath.Join(root, "bin", binaryName())
	want, err := declaredVersion(root)
	if err != nil {
		return bad(checkBinary, StatusError, err.Error(), "confira as permissões do Makefile")
	}
	install := "instale com `GOBIN=$(pwd)/bin go install github.com/rodrigodosanjosoliveira/pipeline-ai-claude/cmd/pipeline-claude@<versão>`"
	if want != "" {
		install = strings.Replace(install, "<versão>", want, 1)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return bad(checkBinary, StatusError, rel+" não encontrado", install)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := e.output(ctx, path, "version")
	if err != nil {
		return bad(checkBinary, StatusError, fmt.Sprintf("%s version falhou: %v", rel, err), install)
	}
	m := binVersionRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return bad(checkBinary, StatusError, fmt.Sprintf("%s version com saída irreconhecível: %q", rel, strings.TrimSpace(string(out))), install)
	}
	have := m[1]
	switch {
	case want == "":
		return ok(checkBinary, rel+" "+have+" (versão não fixada no Makefile)")
	case have != want:
		return bad(checkBinary, StatusError, fmt.Sprintf("%s é %s; o Makefile fixa %s", rel, have, want), install)
	}
	return ok(checkBinary, rel+" "+have)
}

// caseFSCheck cria, num diretório temporário dentro da raiz, dois arquivos que
// diferem só na caixa. Se colidirem, o filesystem não diferencia caixa: o hook
// já compara sem caixa (TD-09), mas o aviso ajuda a entender diffs e renames.
func caseFSCheck(root string) Check {
	dir, err := os.MkdirTemp(root, ".pipeline-claude-doctor-")
	if err != nil {
		return bad(checkCaseFS, StatusWarn, fmt.Sprintf("não foi possível verificar a caixa do filesystem: %v", err), "confira a permissão de escrita na raiz do projeto")
	}
	defer os.RemoveAll(dir)

	create := func(name string) error {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	}
	if err := create("caixa"); err != nil {
		return bad(checkCaseFS, StatusWarn, fmt.Sprintf("não foi possível verificar a caixa do filesystem: %v", err), "confira a permissão de escrita na raiz do projeto")
	}
	err = create("CAIXA")
	switch {
	case errors.Is(err, fs.ErrExist):
		return bad(checkCaseFS, StatusWarn, "filesystem não diferencia caixa: Internal/ e internal/ são o mesmo diretório",
			"nada a corrigir: o hook já compara caminhos sem caixa; evite arquivos que diferem só na caixa")
	case err != nil:
		return bad(checkCaseFS, StatusWarn, fmt.Sprintf("não foi possível verificar a caixa do filesystem: %v", err), "confira a permissão de escrita na raiz do projeto")
	}
	return ok(checkCaseFS, "diferencia caixa")
}
