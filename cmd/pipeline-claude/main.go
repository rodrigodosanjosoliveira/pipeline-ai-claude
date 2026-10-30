// Command pipeline-claude é a CLI do toolkit pipeline-ai-claude: governança
// determinística para desenvolvimento assistido por agentes no Claude Code.
//
// Estado: Fase 4 (hook, fmt-hook, validate, sensors, delivery new, doctor,
// mcp, init, sync, adopt). Comando previsto e ainda não entregue sai com
// código 2 e informa a fase em que será entregue. O código 2 é deliberado: no hook do Claude Code ele bloqueia (falha
// fechada), então um binário incompleto nunca libera edição por engano.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/delivery"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/doctor"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/fmthook"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/hook"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/mcp"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/sensors"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/validate"
)

// Códigos de saída estáveis da CLI.
const (
	exitOK      = 0
	exitFail    = 1 // reprovado (validate, doctor, sensors)
	exitBlocked = 2 // bloqueio do hook ou uso inválido
)

// planned mapeia os comandos previstos para a fase/fatia que os entrega.
var planned = map[string]string{}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitBlocked
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, buildinfo.String())
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	case "hook":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "pipeline-claude hook: não aceita argumentos; o payload vem pelo stdin.")
			return exitBlocked
		}
		return hook.Run(stdin, stderr, os.Getwd)
	case "fmt-hook":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "pipeline-claude fmt-hook: não aceita argumentos; o payload vem pelo stdin.")
			return exitBlocked
		}
		return fmthook.Run(stdin, stderr, os.Getwd)
	case "validate":
		opts, code, ok := parseValidate(args[1:], stderr)
		if !ok {
			return code
		}
		return validate.Run(opts, stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "sensors":
		return runSensors(args[1:], stdout, stderr)
	case "delivery":
		return runDelivery(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdin, stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "sync":
		return runSync(args[1:], stdout, stderr)
	case "adopt":
		return runAdopt(args[1:], stdout, stderr)
	}
	if phase, ok := planned[args[0]]; ok {
		fmt.Fprintf(stderr, "pipeline-claude: comando %q ainda não implementado (previsto para a %s).\n", args[0], phase)
		return exitBlocked
	}
	fmt.Fprintf(stderr, "pipeline-claude: comando desconhecido %q\n\n", args[0])
	usage(stderr)
	return exitBlocked
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Uso: pipeline-claude <comando> [opções]

Comandos:
  hook                                              hook PreToolUse do Claude Code (payload no stdin)
  fmt-hook                                          hook PostToolUse: formata o .go editado (payload no stdin)
  validate [--base <ref>] [--no-git] [--json] <ID|caminho>
                                                    valida o report de uma delivery
  sensors [--json] [--timeout <duração>] [nome...]  executa os sensores do projeto
  delivery new --title <título> [--prefix <p>] [--json]
                                                    cria a próxima delivery e registra no índice
  doctor [--json]                                   diagnostica ambiente e projeto
  init <nome> [--dir <d>] [--module <m>] [--summary <s>] [--provider github|gitlab]
       [--modules a,b] [--toolkit-version <v>] [--git] [--json]
                                                    cria um projeto novo (destino vazio)
  sync [--dry-run] [--json] [--release <caminho>]...
                                                    atualiza os arquivos gerenciados pelo lock
  adopt [--dir <d>] [--provider github|gitlab] [--modules a,b] [--toolkit-version <v>]
        [--dry-run] [--json]
                                                    instala a pipeline num projeto Go existente
  mcp                                               servidor MCP por stdio (até EOF)
  version                                           mostra a versão
  help                                              mostra esta ajuda

Códigos de saída: 0 ok · 1 reprovado · 2 bloqueio do hook ou uso inválido.
`)
}

// parseValidate lê as opções do validate, antes ou depois do alvo. ok=false
// devolve o código de saída: 0 para -h, 2 para uso inválido.
func parseValidate(args []string, stderr io.Writer) (opts validate.Options, code int, ok bool) {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.BaseRef, "base", "", "referência git do diff (padrão: base_ref da configuração)")
	fs.BoolVar(&opts.NoGit, "no-git", false, "pula as checagens do diff, com aviso")
	fs.BoolVar(&opts.JSON, "json", false, "saída em JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: pipeline-claude validate [--base <ref>] [--no-git] [--json] <ID|caminho>")
		fs.PrintDefaults()
	}

	var targets []string
	rest := args
	for len(rest) > 0 {
		if rest[0] == "--" {
			targets = append(targets, rest[1:]...)
			break
		}
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return opts, exitOK, false
			}
			return opts, exitBlocked, false
		}
		rest = fs.Args()
		if len(rest) > 0 {
			targets = append(targets, rest[0])
			rest = rest[1:]
		}
	}
	switch len(targets) {
	case 0:
		fmt.Fprintln(stderr, "pipeline-claude validate: informe um alvo (ID da delivery ou caminho do report).")
		return opts, exitBlocked, false
	case 1:
		opts.Target = targets[0]
		return opts, exitOK, true
	}
	fmt.Fprintf(stderr, "pipeline-claude validate: informe um alvo só (recebidos %d).\n", len(targets))
	return opts, exitBlocked, false
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "saída em JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: pipeline-claude doctor [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitBlocked
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "pipeline-claude doctor: não aceita argumentos além de --json.")
		return exitBlocked
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude doctor: falha ao obter o diretório atual: %v\n", err)
		return exitBlocked
	}
	return doctor.Run(wd, *asJSON, stdout)
}

// loadConfig carrega a configuração a partir do diretório atual. Falha é uso
// inválido (2), como no validate, com a dica do doctor.
func loadConfig(cmd string, stderr io.Writer) (*config.Config, bool) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude %s: falha ao obter o diretório atual: %v\n", cmd, err)
		return nil, false
	}
	cfg, err := config.Load(wd)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude %s: configuração ausente ou inválida: %v\nRode `pipeline-claude doctor` para diagnosticar.\n", cmd, err)
		return nil, false
	}
	return cfg, true
}

func runSensors(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sensors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "saída em JSON")
	timeout := fs.Duration("timeout", sensors.DefaultTimeout, "prazo de cada sensor (ex.: 90s, 15m)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: pipeline-claude sensors [--json] [--timeout <duração>] [nome...]")
		fs.PrintDefaults()
	}
	names, code, ok := parseInterspersed(fs, args)
	if !ok {
		return code
	}
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "pipeline-claude sensors: --timeout precisa ser positivo (recebido %s).\n", *timeout)
		return exitBlocked
	}
	cfg, ok := loadConfig("sensors", stderr)
	if !ok {
		return exitBlocked
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return sensors.Print(stdout, sensors.Run(ctx, cfg, sensors.Options{Names: names, Timeout: *timeout}), *asJSON)
}

func runDelivery(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "new" {
		if len(args) > 0 {
			fmt.Fprintf(stderr, "pipeline-claude delivery: subcomando desconhecido %q.\n", args[0])
		}
		fmt.Fprintln(stderr, "Uso: pipeline-claude delivery new --title <título> [--prefix <p>] [--scope <escopo>] [--flags <f1,f2>] [--json]")
		return exitBlocked
	}
	fs := flag.NewFlagSet("delivery new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	title := fs.String("title", "", "título da delivery (obrigatório, uma linha)")
	prefix := fs.String("prefix", "", "prefixo do ID, um de delivery_prefixes")
	scope := fs.String("scope", "", "escopo fechado da triagem, gravado no lugar de <escopo> do report_template")
	flags := fs.String("flags", "", "flags do report a ligar, separadas por vírgula (ex.: require_security)")
	asJSON := fs.Bool("json", false, `saída em JSON ({"id", "report", "index"})`)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: pipeline-claude delivery new --title <título> [--prefix <p>] [--scope <escopo>] [--flags <f1,f2>] [--json]")
		fs.PrintDefaults()
	}
	rest, code, ok := parseInterspersed(fs, args[1:])
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "pipeline-claude delivery new: argumento inesperado %q; use --title.\n", rest[0])
		return exitBlocked
	}
	if strings.TrimSpace(*title) == "" {
		fmt.Fprintln(stderr, "pipeline-claude delivery new: informe --title.")
		return exitBlocked
	}
	var flagList []string
	if *flags != "" {
		for _, f := range strings.Split(*flags, ",") {
			if strings.TrimSpace(f) == "" {
				fmt.Fprintln(stderr, "pipeline-claude delivery new: --flags com item vazio; use nomes separados por vírgula (ex.: require_security,require_release_ops).")
				return exitBlocked
			}
			flagList = append(flagList, f)
		}
	}
	cfg, ok := loadConfig("delivery new", stderr)
	if !ok {
		return exitBlocked
	}
	created, err := delivery.New(cfg, delivery.NewOptions{Title: *title, Prefix: *prefix, Scope: *scope, Flags: flagList}, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude delivery new: %v\n", err)
		return exitFail
	}
	if *asJSON {
		out := struct {
			ID     string `json:"id"`
			Report string `json:"report"`
			Index  string `json:"index"`
		}{created.ID, created.ReportPath, created.IndexPath}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "pipeline-claude delivery new: falha ao escrever a saída: %v\n", err)
			return exitFail
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "%s criada: %s\n", created.ID, created.ReportPath)
	return exitOK
}

func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "pipeline-claude mcp: não aceita argumentos; as mensagens vêm pelo stdin.")
		return exitBlocked
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mcp.Serve(ctx, stdin, stdout, stderr, os.Getwd); err != nil {
		fmt.Fprintf(stderr, "pipeline-claude mcp: %v\n", err)
		return exitFail
	}
	return exitOK
}

// parseInterspersed lê as opções de fs antes ou depois dos argumentos
// posicionais, que devolve em ordem; "--" encerra as opções. ok=false devolve
// o código de saída: 0 para -h, 2 para uso inválido.
func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, code int, ok bool) {
	rest := args
	for len(rest) > 0 {
		if rest[0] == "--" {
			return append(positional, rest[1:]...), exitOK, true
		}
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, exitOK, false
			}
			return nil, exitBlocked, false
		}
		rest = fs.Args()
		if len(rest) > 0 && rest[0] != "--" {
			positional = append(positional, rest[0])
			rest = rest[1:]
		}
	}
	return positional, exitOK, true
}
