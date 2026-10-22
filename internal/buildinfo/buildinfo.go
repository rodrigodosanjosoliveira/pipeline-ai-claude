// Package buildinfo expõe a versão do binário pipeline-claude.
//
// A versão vem, em ordem de preferência:
//  1. de -ldflags "-X github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo.version=<v>"
//     (builds locais pelo Makefile);
//  2. da versão do módulo principal registrada pelo `go install ...@<v>`;
//  3. "dev".
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// version é preenchida por -ldflags. Vazia significa "não informada".
var version string

// Version devolve a versão efetiva do binário.
func Version() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

// String devolve a linha impressa por `pipeline-claude version`.
func String() string {
	return fmt.Sprintf("pipeline-claude %s", Version())
}
