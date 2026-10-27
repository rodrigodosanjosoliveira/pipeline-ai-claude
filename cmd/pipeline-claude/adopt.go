package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/adopt"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
)

const adoptUsage = "Uso: pipeline-claude adopt [--dir <d>] [--provider github|gitlab] [--modules a,b] [--toolkit-version <v>] [--dry-run] [--json]"

// runAdopt instala a pipeline num projeto Go existente (TD-31 a TD-33): cria
// o que falta e o lock, nunca altera arquivo existente e imprime as
// instruções do que o projeto acrescenta à mão. Pré-condição ou entrada
// inválida sai com 2; falha de escrita, com 1.
func runAdopt(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "raiz do projeto (padrão: diretório atual)")
	var opts adopt.Options
	fs.StringVar(&opts.Provider, "provider", "", "provedor git: github ou gitlab (padrão: o do remote origin)")
	modules := fs.String("modules", "", "módulos opcionais separados por vírgula (padrão: os que já têm subagente): "+strings.Join(knownModules(), ", "))
	fs.StringVar(&opts.ToolkitVersion, "toolkit-version", "", "versão do toolkit gravada no lock e nas instruções (padrão: a do binário)")
	dryRun := fs.Bool("dry-run", false, "mostra o plano e as instruções sem gravar nada")
	asJSON := fs.Bool("json", false, `saída em JSON ({"ok", "items", "instructions", "created"})`)
	fs.Usage = func() {
		fmt.Fprintln(stderr, adoptUsage)
		fs.PrintDefaults()
	}
	rest, code, ok := parseInterspersed(fs, args)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "pipeline-claude adopt: argumento inesperado %q; use --dir <d>.\n", rest[0])
		return exitBlocked
	}
	if *modules != "" {
		for _, m := range strings.Split(*modules, ",") {
			m = strings.TrimSpace(m)
			if m == "" {
				fmt.Fprintf(stderr, "pipeline-claude adopt: --modules %q tem item vazio.\n", *modules)
				return exitBlocked
			}
			opts.Modules = append(opts.Modules, m)
		}
	}
	if opts.ToolkitVersion == "" {
		v := buildinfo.Version()
		if !pinnable(v) {
			fmt.Fprintf(stderr, "pipeline-claude adopt: o binário não tem versão de módulo (%s) para fixar no projeto; informe --toolkit-version vX.Y.Z.\n", v)
			return exitBlocked
		}
		opts.ToolkitVersion = v
	}
	root := *dir
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "pipeline-claude adopt: falha ao obter o diretório atual: %v\n", err)
			return exitBlocked
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude adopt: raiz %s: %v\n", root, err)
		return exitBlocked
	}
	opts.Root = abs

	plan, err := adopt.Plan(opts)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline-claude adopt: %v\n", err)
		return exitBlocked
	}
	created := []string{}
	if !*dryRun {
		res, err := adopt.Apply(plan)
		if err != nil {
			fmt.Fprintf(stderr, "pipeline-claude adopt: %v\n", err)
			return exitFail
		}
		created = res.Created
	}

	if *asJSON {
		out := struct {
			OK           bool                `json:"ok"`
			Items        []adopt.Item        `json:"items"`
			Instructions []adopt.Instruction `json:"instructions"`
			Created      []string            `json:"created"`
		}{true, plan.Items, plan.Instructions, created}
		if out.Instructions == nil {
			out.Instructions = []adopt.Instruction{}
		}
		return encodeJSON("adopt", out, stdout, stderr)
	}

	if *dryRun {
		fmt.Fprintf(stdout, "Plano de adoção de %s (--dry-run: nada foi gravado):\n", plan.Root)
	} else {
		fmt.Fprintf(stdout, "Adoção de %s (%d arquivo(s) criado(s), inclusive o lock):\n", plan.Root, len(created))
	}
	for _, it := range plan.Items {
		fmt.Fprintf(stdout, "  %-14s %s", "["+string(it.Action)+"]", it.Path)
		if it.Reason != "" {
			fmt.Fprintf(stdout, " — %s", it.Reason)
		}
		fmt.Fprintln(stdout)
	}
	if len(plan.Instructions) > 0 {
		fmt.Fprintln(stdout, "\nInstruções (o adopt não altera arquivo existente; acrescente à mão):")
		for i, in := range plan.Instructions {
			fmt.Fprintf(stdout, "\n  %d. %s: %s\n", i+1, in.Path, in.Reason)
			for _, l := range strings.Split(strings.TrimRight(in.Snippet, "\n"), "\n") {
				fmt.Fprintf(stdout, "     %s\n", l)
			}
		}
	}
	fmt.Fprintln(stdout, "\nPróximos passos:")
	if len(plan.Instructions) > 0 {
		fmt.Fprintln(stdout, "  aplique as instruções acima")
	}
	if *dryRun {
		fmt.Fprintln(stdout, "  rode `pipeline-claude adopt` sem --dry-run")
	}
	fmt.Fprintln(stdout, "  make tools")
	fmt.Fprintln(stdout, "  bin/pipeline-claude doctor")
	fmt.Fprintln(stdout, "  revise o diff e faça o commit da adoção")
	return exitOK
}
