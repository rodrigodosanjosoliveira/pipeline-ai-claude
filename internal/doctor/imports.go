package doctor

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// importsCheck avisa quando um pacote Go do projeto importa algo sensível
// (config.SensitiveImports, por prefixo de caminho de import) num arquivo fora
// de security_surface_paths (TD-44). Lê só os imports (parser.ImportsOnly) e
// ignora _test.go, vendor/, testdata/ e diretórios que começam com "." ou
// "_". A cobertura é a do validador (config.IsSecuritySurface), arquivo a
// arquivo. Arquivo ilegível vira aviso com o caminho. Nunca é erro.
func importsCheck(root string, cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return bad(checkImports, StatusWarn, "não verificado: configuração ausente ou inválida", "corrija a configuração (ver checagem configuração)")
	}
	found := map[string]map[string]bool{} // diretório → imports sensíveis
	rootFiles := map[string]bool{}        // arquivos na raiz com import sensível
	var unreadable []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			if p == root {
				return err
			}
			unreadable = append(unreadable, rel)
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if perr != nil {
			unreadable = append(unreadable, rel)
			return nil
		}
		if cfg.IsSecuritySurface(rel) {
			return nil
		}
		for _, spec := range f.Imports {
			imp, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil || cfg.SensitiveImport(imp) == "" {
				continue
			}
			dir := path.Dir(rel)
			if found[dir] == nil {
				found[dir] = map[string]bool{}
			}
			found[dir][imp] = true
			if dir == "." {
				rootFiles[rel] = true
			}
		}
		return nil
	})
	if err != nil {
		return bad(checkImports, StatusWarn, fmt.Sprintf("não verificado: falha ao ler o projeto: %v", err), "confira as permissões da raiz do projeto")
	}
	if len(found) == 0 && len(unreadable) == 0 {
		return ok(checkImports, "nenhum import sensível fora de security_surface_paths")
	}

	var msgs, actions []string
	if len(found) > 0 {
		dirs := sortedKeys(found)
		var items, suggest []string
		for _, dir := range dirs {
			items = append(items, fmt.Sprintf("%s (%s)", dir, strings.Join(sortedKeys(found[dir]), ", ")))
			if dir == "." {
				for _, f := range sortedKeys(rootFiles) {
					suggest = append(suggest, strconv.Quote(f))
				}
				continue
			}
			suggest = append(suggest, strconv.Quote(dir+"/"))
		}
		msgs = append(msgs, "imports sensíveis fora de security_surface_paths: "+strings.Join(items, ", "))
		actions = append(actions, "se o pacote acessa banco ou executa comando, acrescente em security_surface_paths: "+
			strings.Join(suggest, ", ")+"; se o import não é sensível no projeto, ajuste security_surface_imports")
	} else {
		msgs = append(msgs, "nenhum import sensível fora de security_surface_paths")
	}
	if len(unreadable) > 0 {
		slices.Sort(unreadable)
		msgs = append(msgs, "arquivo(s) Go ilegível(is), imports não verificados: "+strings.Join(unreadable, ", "))
		actions = append(actions, "corrija a sintaxe ou as permissões desses arquivos")
	}
	return bad(checkImports, StatusWarn, strings.Join(msgs, "; "), strings.Join(actions, "; "))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
