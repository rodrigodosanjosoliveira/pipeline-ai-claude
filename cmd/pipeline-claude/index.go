package main

import (
	"context"
	"fmt"
	"io"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/delivery"
)

const indexUsage = "Uso: pipeline-claude index sync [--base <ref>] [--dry-run] [--json]"

// runIndex atende "index sync" (TD-42): marca como merged as deliveries draft
// ou em revisão do índice com commit "[<ID>]" na base. Erro de git, índice ou
// report sai com 1 sem gravar; uso inválido, com 2.
func runIndex(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "sync" {
		if len(args) > 0 {
			fmt.Fprintf(stderr, "pipeline-claude index: subcomando desconhecido %q.\n", args[0])
		}
		fmt.Fprintln(stderr, indexUsage)
		return exitBlocked
	}
	fs := newFlagSet("index sync", stderr)
	var opts delivery.SyncOptions
	fs.StringVar(&opts.Base, "base", "", "ref onde procurar os commits [<ID>] (padrão: base_ref da configuração)")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "mostra as mudanças sem gravar")
	asJSON := fs.Bool("json", false, `saída em JSON ({"ok", "changes"})`)
	fs.Usage = func() {
		fmt.Fprintln(stderr, indexUsage)
		fs.PrintDefaults()
	}
	rest, code, ok := parseInterspersed(fs, args[1:])
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "pipeline-claude index sync: argumento inesperado %q.\n", rest[0])
		return exitBlocked
	}
	cfg, ok := loadConfig("index sync", stderr)
	if !ok {
		return exitBlocked
	}
	changes, err := delivery.SyncIndex(context.Background(), cfg, opts)
	if *asJSON {
		out := struct {
			OK      bool                    `json:"ok"`
			Changes []delivery.StatusChange `json:"changes"`
			Error   string                  `json:"error,omitempty"`
		}{err == nil, changes, ""}
		if err != nil {
			out.Changes, out.Error = []delivery.StatusChange{}, err.Error()
		}
		if code := encodeJSON("index sync", out, stdout, stderr); code != exitOK || err != nil {
			return exitFail
		}
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude index sync: %v\n", err)
		return exitFail
	}
	fmt.Fprint(stdout, delivery.FormatChanges(changes, opts.DryRun))
	return exitOK
}
