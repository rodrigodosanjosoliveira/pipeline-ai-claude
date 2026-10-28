package main

// Testes de contrato (TD-37): cada arquivo-referência de testdata/contract
// congela uma parte do contrato estável da v1.0. Mudança num deles aparece no
// diff da PR e exige entrada em "Não lançado" no CHANGELOG.md.
//
// Regravar, depois de conferir que a mudança é intencional:
//
//	go test ./cmd/pipeline-claude -run Contract -update
//
// Os testes ficam todos neste pacote: a flag -update só existe aqui (um
// `go test ./... -update` reprovaria nos pacotes que não a definem), e CLI,
// saídas --json e MCP são lidos do binário de teste, como o usuário os vê.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

var updateContract = flag.Bool("update", false, "regrava os arquivos-referência de testdata/contract (testes Contract)")

const contractUpdateHint = "go test ./cmd/pipeline-claude -run Contract -update"

// checkContract compara got com testdata/contract/<rel>, ou regrava com -update.
// A comparação ignora CRLF: checkout com autocrlf no Windows não é mudança.
func checkContract(t *testing.T, rel string, got string) {
	t.Helper()
	if !strings.HasSuffix(got, "\n") {
		got += "\n"
	}
	file := filepath.Join("..", "..", "testdata", "contract", filepath.FromSlash(rel))
	if *updateContract {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("arquivo-referência testdata/contract/%s ausente; confira a saída e rode `%s`:\n%s", rel, contractUpdateHint, got)
	}
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if want == got {
		return
	}
	t.Errorf("contrato mudou: testdata/contract/%s difere (- referência, + atual).\n"+
		"Mudança intencional: rode `%s` e registre a mudança em \"Não lançado\" no CHANGELOG.md (TD-37).\n%s",
		rel, contractUpdateHint, lineDiff(want, got))
}

// lineDiff lista as linhas só da referência (-) e só da atual (+).
func lineDiff(want, got string) string {
	w := strings.Split(strings.TrimSuffix(want, "\n"), "\n")
	g := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	var b strings.Builder
	for _, l := range w {
		if !slices.Contains(g, l) {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range g {
		if !slices.Contains(w, l) {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	if b.Len() == 0 {
		b.WriteString("(mesmas linhas, em outra ordem)\n")
	}
	return b.String()
}

// ---------------------------------------------------------------- config e lock

// contractField é um campo JSON descoberto por reflexão.
type contractField struct {
	path string // a.b, lista[].c, mapa.*.d
	typ  string
}

// reflectFields percorre t (struct) em ordem de declaração, pelas tags json.
func reflectFields(prefix string, t reflect.Type) []contractField {
	var out []contractField
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		p := name
		if prefix != "" {
			p = prefix + "." + name
		}
		out = append(out, contractField{p, typeName(f.Type)})
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Struct:
			out = append(out, reflectFields(p, ft)...)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			out = append(out, reflectFields(p+"[]", ft.Elem())...)
		case ft.Kind() == reflect.Map && ft.Elem().Kind() == reflect.Struct:
			out = append(out, reflectFields(p+".*", ft.Elem())...)
		}
	}
	return out
}

func typeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return typeName(t.Elem())
	case reflect.Struct:
		return "object"
	case reflect.Slice:
		return "[]" + typeName(t.Elem())
	case reflect.Map:
		return "map[" + typeName(t.Key()) + "]" + typeName(t.Elem())
	}
	return t.Kind().String()
}

// pathTokens quebra "a.b[].c.*.d" em ["a", "b", "[]", "c", "*", "d"].
func pathTokens(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, ".") {
		if name, ok := strings.CutSuffix(seg, "[]"); ok {
			out = append(out, name, "[]")
			continue
		}
		out = append(out, seg)
	}
	return out
}

// removePath apaga o campo de v (JSON genérico) em todas as ocorrências e
// devolve quantas apagou.
func removePath(v any, toks []string) int {
	switch toks[0] {
	case "[]":
		arr, _ := v.([]any)
		n := 0
		for _, e := range arr {
			n += removePath(e, toks[1:])
		}
		return n
	case "*":
		m, _ := v.(map[string]any)
		n := 0
		for _, e := range m {
			n += removePath(e, toks[1:])
		}
		return n
	}
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	if len(toks) == 1 {
		if _, ok := m[toks[0]]; !ok {
			return 0
		}
		delete(m, toks[0])
		return 1
	}
	return removePath(m[toks[0]], toks[1:])
}

// without devolve full sem os campos paths; falha se algum não estiver lá.
func without(t *testing.T, full string, paths ...string) []byte {
	t.Helper()
	var tree any
	if err := json.Unmarshal([]byte(full), &tree); err != nil {
		t.Fatalf("exemplo completo não é JSON: %v", err)
	}
	for _, p := range paths {
		if removePath(tree, pathTokens(p)) == 0 {
			t.Fatalf("campo %s ausente do exemplo completo do teste: acrescente-o para o contrato saber se é obrigatório", p)
		}
	}
	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fieldsContract descreve cada campo de typ: caminho, tipo e se é obrigatório.
// Obrigatório é o campo cuja remoção do exemplo completo (válido) faz parse
// reprovar: o critério é o comportamento, não um comentário.
func fieldsContract(t *testing.T, typ reflect.Type, full string, parse func([]byte) error) ([]contractField, []bool) {
	t.Helper()
	if err := parse([]byte(full)); err != nil {
		t.Fatalf("exemplo completo do teste é inválido: %v", err)
	}
	fields := reflectFields("", typ)
	required := make([]bool, len(fields))
	for i, f := range fields {
		required[i] = parse(without(t, full, f.path)) != nil
	}
	return fields, required
}

func requiredWord(req bool) string {
	if req {
		return "obrigatório"
	}
	return "opcional"
}

// contractConfigFull usa todos os campos de .pipeline-claude.json, com valores
// válidos e independentes entre si (remover um não invalida outro por tabela).
const contractConfigFull = `{
  "version": 1,
  "project_type": "api",
  "database": "postgres",
  "git_provider": "github",
  "base_ref": "origin/main",
  "deliveries_dir": "docs-ai/entregas",
  "index_file": "docs-ai/entregas/INDEX.md",
  "report_template": "docs-ai/modelos/report.md",
  "delivery_id_pattern": "^DEL-[0-9]{3}$",
  "delivery_prefixes": ["DEL"],
  "invariants_files": ["docs-ai/invariantes.md"],
  "protected_paths": ["internal/", "docs-ai/regras/"],
  "no_agent_paths": ["docs-ai/regras/restrito/"],
  "editor_agents": ["dev-implementation"],
  "report_writers": ["qa-review"],
  "enabled_modules": ["data-model"],
  "security_surface_paths": ["internal/acesso/"],
  "auto_flags": {"require_security": ["internal/acesso/"]},
  "report_schema": {
    "section_level": 2,
    "front_required": ["delivery_id"],
    "sections_required": ["Dev"],
    "fields_required": {"Dev": ["Resumo"]},
    "flag_sections": {"require_security": {"section": "Security", "fields": ["Resultado"]}},
    "flag_front": {"arquitetura_change": ["aprovacao"]},
    "checklist_section": "Final Checklist",
    "placeholder_values": ["tbd"],
    "forbid_markers": ["TODO"]
  },
  "sensors": [{"name": "testes", "cmd": ["go", "test", "./..."]}]
}`

func TestContractConfig(t *testing.T) {
	parse := func(data []byte) error { _, err := config.Parse(data); return err }
	fields, required := fieldsContract(t, reflect.TypeFor[config.Config](), contractConfigFull, parse)

	// Padrões: config só com os obrigatórios de primeiro nível, lida de volta.
	var optionalTop []string
	for i, f := range fields {
		if !required[i] && !strings.ContainsAny(f.path, ".[") {
			optionalTop = append(optionalTop, f.path)
		}
	}
	minimal, err := config.Parse(without(t, contractConfigFull, optionalTop...))
	if err != nil {
		t.Fatalf("config só com os campos obrigatórios é inválida: %v", err)
	}
	defaults := scalarValues(reflect.ValueOf(minimal).Elem())

	var b strings.Builder
	b.WriteString("# Contrato de .pipeline-claude.json version 1 (TD-37): caminho JSON, tipo,\n")
	b.WriteString("# obrigatório/opcional e padrão de campo escalar de primeiro nível.\n")
	b.WriteString("# Gerado por TestContractConfig; regrave com `" + contractUpdateHint + "`.\n")
	for i, f := range fields {
		fmt.Fprintf(&b, "%s %s %s", f.path, f.typ, requiredWord(required[i]))
		if d, ok := defaults[f.path]; ok && !required[i] {
			fmt.Fprintf(&b, " padrão=%s", d)
		}
		b.WriteString("\n")
	}
	checkContract(t, "config.txt", b.String())
}

// scalarValues devolve, por tag json, os campos escalares não zero de v em JSON.
func scalarValues(v reflect.Value) map[string]string {
	out := map[string]string{}
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		switch f.Type.Kind() {
		case reflect.String, reflect.Int, reflect.Bool:
		default:
			continue
		}
		if v.Field(i).IsZero() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		data, err := json.Marshal(v.Field(i).Interface())
		if err != nil {
			continue
		}
		out[name] = string(data)
	}
	return out
}

const contractLockFull = `{
  "version": 1,
  "toolkit_version": "v1.0.0",
  "files": {".claude/agents/dev-implementation.md": "0000000000000000000000000000000000000000000000000000000000000000"},
  "released": [".githooks/pre-push"]
}`

func TestContractLock(t *testing.T) {
	parse := func(data []byte) error {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, manifest.FileName), data, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := manifest.Read(root)
		return err
	}
	fields, required := fieldsContract(t, reflect.TypeFor[manifest.Lock](), contractLockFull, parse)
	var b strings.Builder
	b.WriteString("# Contrato de .pipeline-claude.lock version 1 (TD-23, TD-37): caminho JSON,\n")
	b.WriteString("# tipo e obrigatório/opcional.\n")
	b.WriteString("# Gerado por TestContractLock; regrave com `" + contractUpdateHint + "`.\n")
	for i, f := range fields {
		fmt.Fprintf(&b, "%s %s %s\n", f.path, f.typ, requiredWord(required[i]))
	}
	checkContract(t, "lock.txt", b.String())
}

// ---------------------------------------------------------------- CLI

// helpCommands lê os comandos da seção "Comandos:" da ajuda: o que vem antes
// da primeira opção ou argumento ("delivery new --title ..." → "delivery new").
func helpCommands(t *testing.T, help string) []string {
	t.Helper()
	_, section, ok := strings.Cut(help, "Comandos:\n")
	if !ok {
		t.Fatalf("ajuda sem seção Comandos:\n%s", help)
	}
	section, _, _ = strings.Cut(section, "\n\n")
	word := regexp.MustCompile(`^[a-z][a-z-]*$`)
	var cmds []string
	for _, line := range strings.Split(section, "\n") {
		rest, ok := strings.CutPrefix(line, "  ")
		if !ok || rest == "" || rest[0] == ' ' {
			continue // continuação de linha ou descrição
		}
		head, _, _ := strings.Cut(rest, "  ")
		var words []string
		for _, w := range strings.Fields(head) {
			if !word.MatchString(w) {
				break
			}
			words = append(words, w)
		}
		cmds = append(cmds, strings.Join(words, " "))
	}
	slices.Sort(cmds)
	return cmds
}

// helpExitCodes lê a linha "Códigos de saída: 0 ok · 1 ... ." da ajuda.
func helpExitCodes(t *testing.T, help string) []string {
	t.Helper()
	for _, line := range strings.Split(help, "\n") {
		rest, ok := strings.CutPrefix(line, "Códigos de saída: ")
		if !ok {
			continue
		}
		var out []string
		for _, part := range strings.Split(strings.TrimSuffix(rest, "."), " · ") {
			out = append(out, "saída "+strings.TrimSpace(part))
		}
		return out
	}
	t.Fatalf("ajuda sem a linha de códigos de saída:\n%s", help)
	return nil
}

var (
	flagHeaderRe  = regexp.MustCompile(`^  -(\S+)(?: (\S+))?(?:\t(.*))?$`)
	flagDefaultRe = regexp.MustCompile(`\(default (.*)\)$`)
)

// parseFlagDefaults lê a saída de flag.PrintDefaults: "-nome tipo padrão=...".
func parseFlagDefaults(out string) []string {
	type opt struct{ name, typ, def string }
	var opts []opt
	for _, line := range strings.Split(out, "\n") {
		if m := flagHeaderRe.FindStringSubmatch(line); m != nil {
			typ := m[2]
			if typ == "" {
				typ = "bool"
			}
			opts = append(opts, opt{name: m[1], typ: typ})
			line = m[3]
		} else if len(opts) == 0 || !strings.HasPrefix(line, "    \t") {
			continue
		}
		if d := flagDefaultRe.FindStringSubmatch(line); d != nil {
			opts[len(opts)-1].def = d[1]
		}
	}
	zero := map[string]string{"bool": "false", "string": `""`, "duration": "0s", "int": "0", "value": `""`}
	var lines []string
	for _, o := range opts {
		def := o.def
		if def == "" {
			def = zero[o.typ]
		}
		lines = append(lines, fmt.Sprintf("  --%s %s padrão=%s", o.name, o.typ, def))
	}
	return lines
}

func TestContractCLI(t *testing.T) {
	dir := t.TempDir()
	code, help, stderr := runBin(t, dir, "", "help")
	if code != exitOK {
		t.Fatalf("help: código %d\n%s", code, stderr)
	}
	var b strings.Builder
	b.WriteString("# Contrato da CLI (TD-37): comandos da ajuda, código de saída de -h e de opção\n")
	b.WriteString("# desconhecida, opções lidas do -h de cada comando (nome, tipo, padrão) e a\n")
	b.WriteString("# tabela de códigos de saída.\n")
	b.WriteString("# Gerado por TestContractCLI; regrave com `" + contractUpdateHint + "`.\n")
	for _, cmd := range helpCommands(t, help) {
		args := strings.Fields(cmd)
		fmt.Fprintf(&b, "comando %s\n", cmd)
		code, _, stderr := runBin(t, dir, "", append(slices.Clone(args), "-h")...)
		fmt.Fprintf(&b, "  -h: saída %d\n", code)
		if !strings.HasPrefix(stderr, "Uso: pipeline-claude "+cmd) {
			b.WriteString("  sem opções\n")
			continue
		}
		bad, _, _ := runBin(t, dir, "", append(slices.Clone(args), "--opcao-inexistente")...)
		fmt.Fprintf(&b, "  opção desconhecida: saída %d\n", bad)
		opts := parseFlagDefaults(stderr)
		if len(opts) == 0 {
			t.Fatalf("%s -h sem opções legíveis:\n%s", cmd, stderr)
		}
		for _, o := range opts {
			b.WriteString(o + "\n")
		}
	}
	for _, l := range helpExitCodes(t, help) {
		b.WriteString(l + "\n")
	}
	checkContract(t, "cli.txt", b.String())
}

// ---------------------------------------------------------------- saídas --json

// jsonShape descreve data como "caminho: tipo", um por linha e ordenado; itens
// de lista entram como caminho[] e a forma deles é a união dos itens.
func jsonShape(t *testing.T, data string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("saída não é JSON: %v\n%s", err, data)
	}
	if _, err := dec.Token(); err == nil {
		t.Fatalf("saída com mais de um valor JSON:\n%s", data)
	}
	types := map[string]map[string]bool{}
	var walk func(p string, v any)
	walk = func(p string, v any) {
		var typ string
		switch x := v.(type) {
		case nil:
			typ = "null"
		case bool:
			typ = "bool"
		case json.Number:
			typ = "number"
		case string:
			typ = "string"
		case []any:
			typ = "array"
			for _, e := range x {
				walk(p+"[]", e)
			}
		case map[string]any:
			typ = "object"
			for k, e := range x {
				if p == "" {
					walk(k, e)
				} else {
					walk(p+"."+k, e)
				}
			}
		}
		if p == "" {
			return
		}
		if types[p] == nil {
			types[p] = map[string]bool{}
		}
		types[p][typ] = true
	}
	walk("", v)
	var out []string
	for p, set := range types {
		ts := make([]string, 0, len(set))
		for typ := range set {
			ts = append(ts, typ)
		}
		slices.Sort(ts)
		out = append(out, p+": "+strings.Join(ts, "|"))
	}
	slices.Sort(out)
	return out
}

func jsonContract(t *testing.T, rel, cmd string, code int, stdout string, extra ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "# Contrato de `pipeline-claude %s --json` (TD-37): caminho das chaves e tipo\n", cmd)
	b.WriteString("# de cada valor no cenário fictício do teste, não os valores. Chave nova é\n")
	b.WriteString("# compatível; chave removida ou com tipo diferente, não.\n")
	b.WriteString("# Gerado por TestContractJSON; regrave com `" + contractUpdateHint + "`.\n")
	fmt.Fprintf(&b, "saída %d\n", code)
	for _, l := range jsonShape(t, stdout) {
		b.WriteString(l + "\n")
	}
	for _, l := range extra {
		b.WriteString(l + "\n")
	}
	checkContract(t, rel, b.String())
}

func TestContractJSON(t *testing.T) {
	t.Run("validate", func(t *testing.T) {
		// DEL-002 tem checklist aberto: reprova com findings.
		code, stdout, _ := runBin(t, e2eProject(t), "", "validate", "--no-git", "--json", "DEL-002")
		jsonContract(t, "json/validate.txt", "validate", code, stdout)
	})
	t.Run("doctor", func(t *testing.T) {
		// Sem .claude/settings.json: reprova em qualquer ambiente, e as
		// checagens com erro trazem action.
		code, stdout, _ := runBin(t, e2eProject(t), "", "doctor", "--json")
		var out struct {
			Checks []struct {
				Name string `json:"name"`
			} `json:"checks"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("doctor --json: %v\n%s", err, stdout)
		}
		names := make([]string, len(out.Checks))
		for i, c := range out.Checks {
			names[i] = c.Name
		}
		jsonContract(t, "json/doctor.txt", "doctor", code, stdout, "checks[].name, em ordem: "+strings.Join(names, ", "))
	})
	t.Run("sensors", func(t *testing.T) {
		t.Setenv(helperEnv, "1")
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		sensor := func(name, mode string) string {
			argv, _ := json.Marshal([]string{exe, "-test.run=^TestHelperProcess$", "--", mode})
			return fmt.Sprintf(`{"name": %q, "cmd": %s}`, name, argv)
		}
		root := e2eProject(t)
		cfg := strings.Replace(e2eConfig, `"version": 1,`, `"version": 1, "sensors": [`+sensor("passa", "ok")+`, `+sensor("falha", "fail")+`],`, 1)
		writeFile(t, root, ".pipeline-claude.json", cfg)
		code, stdout, _ := runBin(t, root, "", "sensors", "--json")
		jsonContract(t, "json/sensors.txt", "sensors", code, stdout)
	})
	t.Run("delivery new", func(t *testing.T) {
		code, stdout, _ := runBin(t, e2eProject(t), "", "delivery", "new", "--json", "--title", "Reserva de livros")
		jsonContract(t, "json/delivery-new.txt", "delivery new", code, stdout)
	})
	t.Run("init", func(t *testing.T) {
		parent := t.TempDir()
		code, stdout, _ := runBin(t, parent, "", "init", "biblioteca-bairro", "--dir", filepath.Join(parent, "biblioteca-bairro"),
			"--module", "example.com/biblioteca-bairro", "--json")
		jsonContract(t, "json/init.txt", "init", code, stdout)
	})
	t.Run("sync", func(t *testing.T) {
		// Um subagente editado: o item traz reason e o sync reprova.
		root := initProject(t)
		writeFile(t, root, e2eAgent, readFile(t, root, e2eAgent)+"\nRegra local da biblioteca.\n")
		code, stdout, _ := runBin(t, root, "", "sync", "--json")
		jsonContract(t, "json/sync.txt", "sync", code, stdout)
	})
	t.Run("adopt", func(t *testing.T) {
		code, stdout, _ := runBin(t, adoptProject(t), "", "adopt", "--json")
		jsonContract(t, "json/adopt.txt", "adopt", code, stdout)
	})
}

// ---------------------------------------------------------------- MCP

// stripDescriptions remove "description" dos objetos de esquema; dentro de
// "properties" as chaves são nomes de argumento e ficam.
func stripDescriptions(v any, parentKey string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k == "description" && parentKey != "properties" {
				continue
			}
			out[k] = stripDescriptions(e, k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripDescriptions(e, parentKey)
		}
		return out
	}
	return v
}

func TestContractMCP(t *testing.T) {
	session := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"contrato","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
	}, "\n") + "\n"
	code, stdout, stderr := runBin(t, e2eProject(t), session, "mcp")
	if code != exitOK {
		t.Fatalf("mcp: código %d\n%s", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("respostas = %d:\n%s", len(lines), stdout)
	}
	var tools struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	var resources struct {
		Result struct {
			Resources []struct {
				URI  string `json:"uri"`
				Name string `json:"name"`
			} `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &tools); err != nil || len(tools.Result.Tools) == 0 {
		t.Fatalf("tools/list: %v\n%s", err, lines[1])
	}
	if err := json.Unmarshal([]byte(lines[2]), &resources); err != nil || len(resources.Result.Resources) == 0 {
		t.Fatalf("resources/list: %v\n%s", err, lines[2])
	}

	type tool struct {
		Name        string `json:"name"`
		InputSchema any    `json:"inputSchema"`
	}
	type resource struct {
		URI  string `json:"uri"`
		Name string `json:"name"`
	}
	out := struct {
		Tools     []tool     `json:"tools"`
		Resources []resource `json:"resources"`
	}{}
	for _, tl := range tools.Result.Tools {
		out.Tools = append(out.Tools, tool{tl.Name, stripDescriptions(tl.InputSchema, "")})
	}
	for _, r := range resources.Result.Resources {
		out.Resources = append(out.Resources, resource(r))
	}
	slices.SortFunc(out.Tools, func(a, b tool) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(out.Resources, func(a, b resource) int { return strings.Compare(a.URI, b.URI) })
	// encoding/json ordena as chaves de mapa: a saída é determinística.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	checkContract(t, "mcp.json", buf.String())
}

// ---------------------------------------------------------------- área gerenciada

func TestContractManaged(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Contrato da área gerenciada (TD-15, TD-23, TD-37): prefixos e destinos dos\n")
	b.WriteString("# arquivos gerenciados do catálogo (o conteúdo não é contrato).\n")
	b.WriteString("# Gerado por TestContractManaged; regrave com `" + contractUpdateHint + "`.\n")
	prefixes := manifest.ManagedPrefixes()
	slices.Sort(prefixes)
	for _, p := range prefixes {
		fmt.Fprintf(&b, "prefixo %s\n", p)
	}
	var files []string
	for _, f := range templates.Catalog() {
		if f.Class != templates.Managed {
			continue
		}
		if !manifest.ManagedArea(f.Path) {
			t.Errorf("arquivo gerenciado %s fora da área gerenciada", f.Path)
		}
		line := "arquivo " + f.Path
		if f.Module != "" {
			line += " módulo=" + f.Module
		}
		files = append(files, line)
	}
	if len(files) == 0 {
		t.Fatal("catálogo sem arquivo gerenciado")
	}
	slices.Sort(files)
	for _, l := range files {
		b.WriteString(l + "\n")
	}
	checkContract(t, "managed.txt", b.String())
}
