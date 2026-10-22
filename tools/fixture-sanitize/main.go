// Command fixture-sanitize converte payloads de hook capturados num projeto
// real em fixtures publicáveis.
//
// Uso:
//
//	go run ./tools/fixture-sanitize -root /caminho/absoluto/do/projeto \
//	    -in ./captures -out testdata/hooks/claude/<versao-do-claude-code>
//
// Para cada *.json em -in: troca caminhos, IDs e conteúdo por marcadores fixos
// (ver internal/fixtures), confere o resultado com fixtures.Check e grava em
// -out com nome derivado de evento, ferramenta e agente. Nunca sobrescreve um
// arquivo existente. Qualquer falha aborta sem gravar o arquivo em questão.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/fixtures"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fixture-sanitize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "raiz absoluta do projeto onde a captura foi feita (obrigatório)")
	home := fs.String("home", "", "diretório home de quem capturou (default: $HOME)")
	in := fs.String("in", "", "diretório com os payloads brutos (obrigatório)")
	out := fs.String("out", "", "diretório de destino das fixtures (obrigatório)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" || *in == "" || *out == "" {
		fmt.Fprintln(stderr, "fixture-sanitize: -root, -in e -out são obrigatórios")
		return 2
	}
	if *home == "" {
		*home, _ = os.UserHomeDir()
	}

	entries, err := filepath.Glob(filepath.Join(*in, "*.json"))
	if err != nil {
		fmt.Fprintf(stderr, "fixture-sanitize: %v\n", err)
		return 1
	}
	sort.Strings(entries)
	if len(entries) == 0 {
		fmt.Fprintf(stderr, "fixture-sanitize: nenhum *.json em %s\n", *in)
		return 1
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(stderr, "fixture-sanitize: %v\n", err)
		return 1
	}

	failed := 0
	for _, src := range entries {
		raw, err := os.ReadFile(src)
		if err != nil {
			fmt.Fprintf(stderr, "ERRO %s: %v\n", src, err)
			failed++
			continue
		}
		clean, name, err := fixtures.Sanitize(raw, fixtures.Options{Root: *root, Home: *home})
		if err != nil {
			fmt.Fprintf(stderr, "ERRO %s: %v\n", src, err)
			failed++
			continue
		}
		if probs := fixtures.Check(clean, nil); len(probs) > 0 {
			fmt.Fprintf(stderr, "ERRO %s: saída reprovada: %v\n", src, probs)
			failed++
			continue
		}
		dst, err := uniquePath(*out, name)
		if err != nil {
			fmt.Fprintf(stderr, "ERRO %s: %v\n", src, err)
			failed++
			continue
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "ERRO %s: %v\n", src, err)
			failed++
			continue
		}
		_, werr := f.Write(clean)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			fmt.Fprintf(stderr, "ERRO %s: falha ao gravar %s\n", src, dst)
			_ = os.Remove(dst)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "ok  %s -> %s\n", filepath.Base(src), dst)
	}
	if failed > 0 {
		fmt.Fprintf(stderr, "%d payload(s) não convertido(s)\n", failed)
		return 1
	}
	return 0
}

func uniquePath(dir, name string) (string, error) {
	base := strings.TrimSuffix(name, ".json")
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, name)
		if i > 1 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s-%d.json", base, i))
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("nomes esgotados para %s", name)
}
