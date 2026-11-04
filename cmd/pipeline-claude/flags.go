package main

import (
	"flag"
	"fmt"
	"io"
	"regexp"
)

// newFlagSet cria o conjunto de opções de um comando. Os erros do pacote flag
// saem em português e com o prefixo do comando, como as demais mensagens da
// CLI; a ajuda (Usage e PrintDefaults) passa sem mudança.
func newFlagSet(cmd string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(flagOutput{cmd: cmd, w: stderr})
	return fs
}

// flagErrors são as mensagens de erro do pacote flag (uma por Write) e a
// tradução, que usa os grupos da expressão na ordem. A causa que o pacote
// acrescenta ao valor inválido é sempre "parse error" nos tipos usados aqui
// e fica de fora.
var flagErrors = []struct {
	re     *regexp.Regexp
	format string
}{
	{regexp.MustCompile(`^flag provided but not defined: -(\S+)\n$`), "opção desconhecida --%[1]s"},
	{regexp.MustCompile(`^flag needs an argument: -(\S+)\n$`), "a opção --%[1]s precisa de um valor"},
	{regexp.MustCompile(`^invalid value (".*") for flag -(\S+): .*\n$`), "valor %[1]s inválido para --%[2]s"},
	{regexp.MustCompile(`^invalid boolean value (".*") for -(\S+): .*\n$`), "valor %[1]s inválido para --%[2]s"},
	{regexp.MustCompile(`^bad flag syntax: (.*)\n$`), "sintaxe de opção inválida: %[1]s"},
}

type flagOutput struct {
	cmd string
	w   io.Writer
}

func (o flagOutput) Write(p []byte) (int, error) {
	for _, e := range flagErrors {
		m := e.re.FindStringSubmatch(string(p))
		if m == nil {
			continue
		}
		args := make([]any, len(m)-1)
		for i, g := range m[1:] {
			args[i] = g
		}
		if _, err := fmt.Fprintf(o.w, "pipeline-claude %s: %s.\n", o.cmd, fmt.Sprintf(e.format, args...)); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return o.w.Write(p)
}
