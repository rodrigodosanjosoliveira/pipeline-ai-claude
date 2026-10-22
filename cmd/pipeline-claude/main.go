// Command pipeline-claude é a CLI do toolkit pipeline-ai-claude: governança
// determinística para desenvolvimento assistido por agentes no Claude Code.
//
// Estado: fundação (Fase 0). Os comandos de governança entram por fatias; até
// lá, cada um sai com código 2 e informa a fase em que será entregue. O código 2
// é deliberado: no hook do Claude Code ele bloqueia (falha fechada), então um
// binário incompleto nunca libera edição por engano.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
)

// Códigos de saída estáveis da CLI.
const (
	exitOK      = 0
	exitFail    = 1 // reprovado (validate, doctor, sensors)
	exitBlocked = 2 // bloqueio do hook ou uso inválido
)

// planned mapeia os comandos previstos para a fase/fatia que os entrega.
var planned = map[string]string{
	"hook":     "Fase 1",
	"validate": "Fase 1",
	"doctor":   "Fase 1",
	"fmt-hook": "Fase 2",
	"sensors":  "Fase 2",
	"delivery": "Fase 2",
	"mcp":      "Fase 2",
	"init":     "Fase 3",
	"sync":     "Fase 3",
	"adopt":    "Fase 4",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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
  version    mostra a versão
  help       mostra esta ajuda

Previstos:
  hook, validate, doctor          (Fase 1)
  fmt-hook, sensors, delivery, mcp (Fase 2)
  init, sync                      (Fase 3)
  adopt                           (Fase 4)

Códigos de saída: 0 ok · 1 reprovado · 2 bloqueio do hook ou uso inválido.
`)
}
