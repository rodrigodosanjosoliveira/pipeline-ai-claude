// Command pipeline-claude é a CLI do toolkit pipeline-ai-claude: governança
// determinística para desenvolvimento assistido por agentes no Claude Code.
//
// Estado: Fase 1 (hook, validate, doctor). Os demais comandos entram por
// fatias; até lá, cada um sai com código 2 e informa a fase em que será
// entregue. O código 2 é deliberado: no hook do Claude Code ele bloqueia (falha
// fechada), então um binário incompleto nunca libera edição por engano.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/doctor"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/hook"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/validate"
)

// Códigos de saída estáveis da CLI.
const (
	exitOK      = 0
	exitFail    = 1 // reprovado (validate, doctor, sensors)
	exitBlocked = 2 // bloqueio do hook ou uso inválido
)

// planned mapeia os comandos previstos para a fase/fatia que os entrega.
var planned = map[string]string{
	"fmt-hook": "Fase 2",
	"sensors":  "Fase 2",
	"delivery": "Fase 2",
	"mcp":      "Fase 2",
	"init":     "Fase 3",
	"sync":     "Fase 3",
	"adopt":    "Fase 4",
}

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
	case "validate":
		opts, code, ok := parseValidate(args[1:], stderr)
		if !ok {
			return code
		}
		return validate.Run(opts, stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
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
  validate [--base <ref>] [--no-git] [--json] <ID|caminho>
                                                    valida o report de uma delivery
  doctor [--json]                                   diagnostica ambiente e projeto
  version                                           mostra a versão
  help                                              mostra esta ajuda

Previstos:
  fmt-hook, sensors, delivery, mcp (Fase 2)
  init, sync                      (Fase 3)
  adopt                           (Fase 4)

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
