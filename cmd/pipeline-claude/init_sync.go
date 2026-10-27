package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/doctor"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/scaffold"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/syncer"
)

const initUsage = "Uso: pipeline-claude init <nome> [--dir <d>] [--module <m>] [--summary <s>] [--provider github|gitlab] [--modules a,b] [--toolkit-version <v>] [--git] [--json]"

// runInit cria um projeto novo (TD-24). Entrada ou destino inválido sai com 2;
// falha de escrita ou do git, com 1.
func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts scaffold.Options
	fs.StringVar(&opts.Dir, "dir", "", "destino, inexistente ou diretório vazio (padrão: ./<nome>)")
	fs.StringVar(&opts.Module, "module", "", "caminho do módulo Go (padrão: <nome>)")
	fs.StringVar(&opts.Summary, "summary", "", "resumo do projeto, uma linha")
	fs.StringVar(&opts.Provider, "provider", "", "provedor git: github ou gitlab (padrão: github)")
	modules := fs.String("modules", "", "módulos opcionais separados por vírgula: "+strings.Join(knownModules(), ", "))
	fs.StringVar(&opts.ToolkitVersion, "toolkit-version", "", "versão do toolkit fixada no Makefile e no lock (padrão: a do binário)")
	fs.BoolVar(&opts.Git, "git", false, "roda git init -b main e git config core.hooksPath .githooks (sem commit)")
	asJSON := fs.Bool("json", false, `saída em JSON ({"root", "files"})`)
	fs.Usage = func() {
		fmt.Fprintln(stderr, initUsage)
		fs.PrintDefaults()
	}
	names, code, ok := parseInterspersed(fs, args)
	if !ok {
		return code
	}
	switch len(names) {
	case 0:
		fmt.Fprintln(stderr, "pipeline-claude init: informe o nome do projeto.")
		fmt.Fprintln(stderr, initUsage)
		return exitBlocked
	case 1:
		opts.Name = names[0]
	default:
		fmt.Fprintf(stderr, "pipeline-claude init: informe um nome só (recebidos %d).\n", len(names))
		return exitBlocked
	}
	if *modules != "" {
		for _, m := range strings.Split(*modules, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				fmt.Fprintf(stderr, "pipeline-claude init: --modules %q tem item vazio.\n", *modules)
				return exitBlocked
			}
			opts.Modules = append(opts.Modules, m)
		}
	}
	if opts.ToolkitVersion == "" {
		v := buildinfo.Version()
		if !pinnable(v) {
			fmt.Fprintf(stderr, "pipeline-claude init: o binário não tem versão de módulo (%s) para fixar no Makefile; informe --toolkit-version vX.Y.Z.\n", v)
			return exitBlocked
		}
		opts.ToolkitVersion = v
	}
	// A saída do git vai para stderr: stdout fica só com o resultado (--json).
	opts.Stdout, opts.Stderr = stderr, stderr

	res, err := scaffold.Init(opts)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude init: %v\n", err)
		if errors.Is(err, scaffold.ErrInvalid) {
			return exitBlocked
		}
		return exitFail
	}
	if *asJSON {
		out := struct {
			Root  string   `json:"root"`
			Files []string `json:"files"`
		}{res.Root, res.Files}
		return encodeJSON("init", out, stdout, stderr)
	}
	fmt.Fprintf(stdout, "Projeto %s criado em %s (%d arquivos).\n\nPróximos passos:\n  cd %s\n", opts.Name, res.Root, len(res.Files), res.Root)
	if !opts.Git {
		fmt.Fprintln(stdout, "  git init -b main && git config core.hooksPath .githooks")
	}
	fmt.Fprintln(stdout, "  make tools")
	fmt.Fprintln(stdout, "  bin/pipeline-claude doctor")
	return exitOK
}

// pinnable diz se a versão do binário serve de padrão para --toolkit-version:
// versão de módulo que o projeto consiga instalar com go install. Build de
// desenvolvimento (dev) ou com build metadata (+dirty) não serve (TD-24).
func pinnable(v string) bool {
	return scaffold.ValidVersion(v) && !strings.Contains(v, "+")
}

func knownModules() []string {
	out := make([]string, 0, len(config.Modules))
	for m := range config.Modules {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// listFlag é uma opção repetível.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

const syncUsage = "Uso: pipeline-claude sync [--dry-run] [--json] [--release <caminho>]..."

// runSync atualiza os arquivos gerenciados (TD-25): 0 sem conflito, 1 com
// editado ou não gerenciado (ou falha de escrita), 2 com configuração, lock ou
// Makefile ausente ou inválido, versão do binário diferente da do Makefile ou
// uso inválido.
func runSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "mostra o plano sem gravar nada")
	asJSON := fs.Bool("json", false, `saída em JSON ({"ok", "items"})`)
	var release listFlag
	fs.Var(&release, "release", "passa o caminho para released no lock: o sync não o toca mais (repetível)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, syncUsage)
		fs.PrintDefaults()
	}
	rest, code, ok := parseInterspersed(fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "pipeline-claude sync: argumento inesperado %q; use --release <caminho>.\n", rest[0])
		return exitBlocked
	}
	if *dryRun && len(release) > 0 {
		fmt.Fprintln(stderr, "pipeline-claude sync: --release grava o lock e não combina com --dry-run.")
		return exitBlocked
	}

	cfg, ok := loadConfig("sync", stderr)
	if !ok {
		return exitBlocked
	}
	root := cfg.Root()
	// Templates de outra versão desfariam o pin do Makefile (TD-06).
	want, err := doctor.DeclaredVersion(root)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude sync: %v\n", err)
		return exitBlocked
	}
	have := buildinfo.Version()
	switch {
	case want == "":
		fmt.Fprintln(stderr, "pipeline-claude sync: o Makefile não fixa a versão do toolkit (PIPELINE_CLAUDE_VERSION); o sync só roda com a versão fixada.")
		return exitBlocked
	case want != have:
		fmt.Fprintf(stderr, "pipeline-claude sync: o binário é %s e o Makefile fixa %s; rode `make tools` (ou ajuste PIPELINE_CLAUDE_VERSION) e tente de novo.\n", have, want)
		return exitBlocked
	}
	lock, err := manifest.Read(root)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude sync: %v\nRode `pipeline-claude doctor` para diagnosticar.\n", err)
		return exitBlocked
	}
	if len(release) > 0 {
		if err := syncer.Release(root, release); err != nil {
			fmt.Fprintf(stderr, "pipeline-claude sync: %v\n", err)
			return exitBlocked
		}
		if lock, err = manifest.Read(root); err != nil {
			fmt.Fprintf(stderr, "pipeline-claude sync: %v\n", err)
			return exitFail
		}
	}

	items, err := syncer.Plan(root, cfg, lock)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude sync: %v\n", err)
		return exitFail
	}
	if !*dryRun {
		if items, err = syncer.Apply(root, items, lock, have); err != nil {
			fmt.Fprintf(stderr, "pipeline-claude sync: %v\n", err)
			return exitFail
		}
	}

	counts := map[syncer.Action]int{}
	for _, it := range items {
		counts[it.Action]++
	}
	conflicts := counts[syncer.Editado] + counts[syncer.NaoGerenciado]
	code = exitOK
	if conflicts > 0 {
		code = exitFail
	}
	if *asJSON {
		if items == nil {
			items = []syncer.Item{}
		}
		out := struct {
			OK    bool          `json:"ok"`
			Items []syncer.Item `json:"items"`
		}{conflicts == 0, items}
		if c := encodeJSON("sync", out, stdout, stderr); c != exitOK {
			return c
		}
		return code
	}

	if *dryRun {
		fmt.Fprintln(stdout, "Plano do sync (--dry-run: nada foi gravado):")
	}
	listed := false
	for _, it := range items {
		if it.Action == syncer.Nada {
			continue
		}
		listed = true
		fmt.Fprintf(stdout, "  %-16s %s", "["+string(it.Action)+"]", it.Path)
		if it.Reason != "" {
			fmt.Fprintf(stdout, " — %s", it.Reason)
		}
		fmt.Fprintln(stdout)
	}
	writes := counts[syncer.Criar] + counts[syncer.Atualizar] + counts[syncer.Recriar] + counts[syncer.Apagar]
	verb := "aplicada(s)"
	if *dryRun {
		verb = "a aplicar"
	}
	if listed {
		fmt.Fprintln(stdout)
	}
	fmt.Fprintf(stdout, "Resumo: %d mudança(s) %s, %d em dia, %d liberado(s), %d editado(s) ou não gerenciado(s).\n",
		writes, verb, counts[syncer.Nada], counts[syncer.Liberado], conflicts)
	if conflicts > 0 {
		fmt.Fprintln(stdout, "Editados e não gerenciados não foram tocados: assuma o arquivo com `pipeline-claude sync --release <caminho>` ou apague-o e rode o sync para restaurar o conteúdo gerado.")
	}
	return code
}

// encodeJSON escreve v indentado em stdout.
func encodeJSON(cmd string, v any, stdout, stderr io.Writer) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "pipeline-claude %s: falha ao escrever a saída: %v\n", cmd, err)
		return exitFail
	}
	return exitOK
}
