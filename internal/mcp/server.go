// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/mcp/server.go @ 0eac73a (MIT).
// Alterações: Serve(ctx, in, out, errOut, getwd) no lugar de Server/ServeStream;
// linha acima de 1 MiB e JSON inválido respondem -32700 com id null e o servidor
// segue; requisição malformada responde -32600; notificação nunca responde
// (TD-21); protocolVersion negociada entre as suportadas; config carregada a
// cada chamada, ausente ou inválida vira isError (TD-08); ferramentas
// pipeline_doctor, pipeline_validate, pipeline_sensors e pipeline_delivery_new
// com argumentos estritos (-32602); pipeline_sync_skills removida e
// pipeline_delivery_new recusada em caminho protegido (TD-16); caminhos
// conferidos dentro da raiz (TD-17); recursos pipeline://config,
// pipeline://invariants e pipeline://deliveries/index lidos da config;
// pipeline_diff só leitura para os revisores (TD-29).

// Package mcp implementa o servidor MCP do pipeline-claude por stdio (TD-21).
//
// Cada linha de entrada é uma mensagem JSON-RPC 2.0; cada resposta é uma linha
// em out. out só carrega protocolo; log vai para errOut.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/delivery"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/doctor"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/gitdiff"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/hookio"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/sensors"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/validate"
)

// MaxLine é o tamanho máximo de uma mensagem, sem a quebra de linha.
const MaxLine = 1 << 20

// maxResource limita o arquivo lido por um recurso.
const maxResource = 4 << 20

// maxDiff limita o patch devolvido por pipeline_diff (TD-29); o resto se lê
// com Read ou filtrando por paths.
const maxDiff = 64 << 10

// maxTimeoutSeconds limita timeout_seconds de pipeline_sensors (24 h).
const maxTimeoutSeconds = 24 * 60 * 60

// protocolVersions são as versões do MCP aceitas, da mais antiga à mais nova.
var protocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18"}

const doctorHint = "Rode `pipeline-claude doctor` para diagnosticar."

// rpcError é um erro que vira resposta JSON-RPC de erro.
type rpcError struct {
	code int
	msg  string
}

func (e *rpcError) Error() string { return e.msg }

func invalidParams(format string, args ...any) *rpcError {
	return &rpcError{CodeInvalidParams, fmt.Sprintf(format, args...)}
}

type server struct {
	ctx   context.Context
	out   io.Writer
	log   io.Writer
	getwd func() (string, error)
}

// Serve atende mensagens de in até EOF, quando devolve nil. Devolve erro se a
// leitura ou a escrita falhar, ou ctx.Err() se o contexto for cancelado entre
// mensagens.
func Serve(ctx context.Context, in io.Reader, out, errOut io.Writer, getwd func() (string, error)) error {
	s := &server{ctx: ctx, out: out, log: errOut, getwd: getwd}
	r := bufio.NewReader(in)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, tooLong, err := readLine(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("falha ao ler stdin: %w", err)
		}
		if tooLong {
			err = s.writeError(nil, CodeParseError, "mensagem acima de 1 MiB")
		} else {
			err = s.handleLine(line)
		}
		if err != nil {
			return fmt.Errorf("falha ao escrever no stdout: %w", err)
		}
	}
}

// readLine lê uma linha sem a quebra ("\n" ou "\r\n"). tooLong indica linha
// acima de MaxLine, descartada até a quebra. Devolve io.EOF só sem dados.
func readLine(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	var buf []byte
	read := false
	for {
		chunk, err := r.ReadSlice('\n')
		read = read || len(chunk) > 0
		if !tooLong {
			buf = append(buf, chunk...)
			if len(buf) > MaxLine+2 {
				tooLong, buf = true, nil
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if !read {
				return nil, false, io.EOF
			}
		case err != nil:
			return nil, false, err
		}
		break
	}
	buf = bytes.TrimSuffix(buf, []byte("\n"))
	buf = bytes.TrimSuffix(buf, []byte("\r"))
	if len(buf) > MaxLine {
		return nil, true, nil
	}
	return buf, tooLong, nil
}

// handleLine decodifica e despacha uma mensagem. Só devolve erro de escrita.
func (s *server) handleLine(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	if !json.Valid(line) {
		return s.writeError(nil, CodeParseError, "JSON inválido")
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(line, &msg); err != nil || msg == nil {
		return s.writeError(nil, CodeInvalidRequest, "a mensagem precisa ser um objeto JSON-RPC 2.0")
	}

	id, hasID := msg["id"]
	if hasID && !validID(id) {
		return s.writeError(nil, CodeInvalidRequest, "id precisa ser string ou número")
	}
	// Notificação nunca recebe resposta, nem de erro, e não executa nada:
	// nenhuma notificação do cliente pede ação deste servidor.
	if !hasID {
		return nil
	}

	var version, method string
	if json.Unmarshal(msg["jsonrpc"], &version) != nil || version != "2.0" {
		return s.writeError(id, CodeInvalidRequest, `jsonrpc precisa ser "2.0"`)
	}
	if raw, ok := msg["method"]; !ok || json.Unmarshal(raw, &method) != nil || method == "" {
		return s.writeError(id, CodeInvalidRequest, "method ausente ou inválido")
	}
	params := msg["params"]
	if isNull(params) {
		params = nil
	}
	if params != nil && !isObject(params) {
		return s.writeError(id, CodeInvalidParams, "params precisa ser um objeto")
	}

	result, err := s.dispatch(method, params)
	if err != nil {
		var rerr *rpcError
		if !errors.As(err, &rerr) {
			rerr = &rpcError{CodeInternalError, err.Error()}
		}
		return s.writeError(id, rerr.code, rerr.msg)
	}
	return s.write(Response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) dispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return s.initialize(params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList()}, nil
	case "tools/call":
		return s.callTool(params)
	case "resources/list":
		return map[string]any{"resources": resourceList}, nil
	case "resources/read":
		return s.readResource(params)
	default:
		return nil, &rpcError{CodeMethodNotFound, fmt.Sprintf("método não encontrado: %s", method)}
	}
}

func (s *server) initialize(params json.RawMessage) map[string]any {
	var requested string
	if fields, err := objectFields(params); err == nil {
		_ = json.Unmarshal(fields["protocolVersion"], &requested)
	}
	version := protocolVersions[len(protocolVersions)-1]
	for _, v := range protocolVersions {
		if v == requested {
			version = v
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "pipeline-claude",
			"version": buildinfo.Version(),
		},
	}
}

// loadConfig carrega a configuração a partir do diretório atual.
func (s *server) loadConfig() (*config.Config, string, error) {
	wd, err := s.getwd()
	if err != nil {
		return nil, "", fmt.Errorf("falha ao obter o diretório atual: %w", err)
	}
	cfg, err := config.Load(wd)
	if err != nil {
		return nil, wd, fmt.Errorf("configuração ausente ou inválida: %w\n%s", err, doctorHint)
	}
	return cfg, wd, nil
}

// ---------------------------------------------------------------- ferramentas

type argKind int

const (
	kindString argKind = iota
	kindBool
	kindInteger
	kindStrings
)

type argSpec struct {
	name        string
	kind        argKind
	required    bool
	description string
}

type toolSpec struct {
	name        string
	description string
	args        []argSpec
	run         func(s *server, a args) ToolResult
}

var tools = []toolSpec{
	{
		name:        "pipeline_doctor",
		description: "Diagnostica o ambiente e o projeto: Go, git, provedor, configuração, hooks, binário e sistema de arquivos.",
		args:        []argSpec{{name: "json", kind: kindBool, description: "Saída em JSON ({\"ok\", \"checks\"})."}},
		run:         (*server).toolDoctor,
	},
	{
		name:        "pipeline_validate",
		description: "Valida o report de uma delivery (esquema, índice, ID e diff contra a base). Reprovado devolve isError.",
		args: []argSpec{
			{name: "target", kind: kindString, required: true, description: "ID da delivery ou caminho do report, relativo à raiz do projeto."},
			{name: "base", kind: kindString, description: "Ref base do diff; padrão: base_ref da configuração."},
			{name: "no_git", kind: kindBool, description: "Pula as checagens que dependem do diff, com aviso."},
		},
		run: (*server).toolValidate,
	},
	{
		name:        "pipeline_sensors",
		description: "Executa os sensores do projeto (sensors em .pipeline-claude.json). Falha devolve isError.",
		args: []argSpec{
			{name: "names", kind: kindStrings, description: "Sensores a executar; padrão: todos."},
			{name: "timeout_seconds", kind: kindInteger, description: "Prazo de cada sensor em segundos; padrão: 600."},
		},
		run: (*server).toolSensors,
	},
	{
		name:        "pipeline_delivery_new",
		description: "Cria uma delivery com ID sequencial: report a partir do esquema ou template e linha no índice. Recusada se deliveries_dir ou index_file estiver em protected_paths.",
		args: []argSpec{
			{name: "title", kind: kindString, required: true, description: "Título da delivery, uma linha."},
			{name: "prefix", kind: kindString, description: "Prefixo do ID, um de delivery_prefixes."},
		},
		run: (*server).toolDeliveryNew,
	},
	{
		name:        "pipeline_diff",
		description: "Diff de revisão, só leitura: arquivos alterados contra a base (commits da branch, índice, working tree e não rastreados) e o patch contra o merge-base, cortado em 64 KiB. Filtre com paths.",
		args: []argSpec{
			{name: "base", kind: kindString, description: "Ref base do diff; padrão: base_ref da configuração."},
			{name: "paths", kind: kindStrings, description: "Arquivos ou diretórios relativos à raiz do repositório; padrão: tudo."},
			{name: "name_only", kind: kindBool, description: "Só a lista de arquivos, sem o patch."},
		},
		run: (*server).toolDiff,
	},
}

func toolList() []Tool {
	list := make([]Tool, 0, len(tools))
	for _, t := range tools {
		props := map[string]any{}
		required := []string{}
		for _, a := range t.args {
			var schema map[string]any
			switch a.kind {
			case kindString:
				schema = map[string]any{"type": "string"}
			case kindBool:
				schema = map[string]any{"type": "boolean"}
			case kindInteger:
				schema = map[string]any{"type": "integer", "minimum": 1, "maximum": maxTimeoutSeconds}
			case kindStrings:
				schema = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			}
			schema["description"] = a.description
			props[a.name] = schema
			if a.required {
				required = append(required, a.name)
			}
		}
		input := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			input["required"] = required
		}
		list = append(list, Tool{Name: t.name, Description: t.description, InputSchema: input})
	}
	return list
}

// args são os argumentos de uma chamada, já conferidos contra o toolSpec.
type args map[string]any

func (a args) str(k string) string    { v, _ := a[k].(string); return v }
func (a args) boolean(k string) bool  { v, _ := a[k].(bool); return v }
func (a args) integer(k string) int64 { v, _ := a[k].(int64); return v }
func (a args) strs(k string) []string { v, _ := a[k].([]string); return v }

// parseArgs confere raw contra spec por chave exata: argumento desconhecido,
// ausente quando obrigatório, null ou de tipo errado é -32602.
func parseArgs(spec toolSpec, raw json.RawMessage) (args, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return nil, invalidParams("arguments de %s: %v", spec.name, err)
	}
	known := map[string]argSpec{}
	for _, a := range spec.args {
		known[a.name] = a
	}
	for k := range fields {
		if _, ok := known[k]; !ok {
			return nil, invalidParams("argumento desconhecido em %s: %q", spec.name, k)
		}
	}
	out := args{}
	for _, a := range spec.args {
		v, ok := fields[a.name]
		if !ok {
			if a.required {
				return nil, invalidParams("argumento obrigatório ausente em %s: %q", spec.name, a.name)
			}
			continue
		}
		bad := invalidParams("argumento %q de %s com tipo inválido", a.name, spec.name)
		if isNull(v) {
			return nil, bad
		}
		switch a.kind {
		case kindString:
			var x string
			if json.Unmarshal(v, &x) != nil {
				return nil, bad
			}
			out[a.name] = x
		case kindBool:
			var x bool
			if json.Unmarshal(v, &x) != nil {
				return nil, bad
			}
			out[a.name] = x
		case kindInteger:
			var x int64
			if json.Unmarshal(v, &x) != nil {
				return nil, bad
			}
			if x < 1 || x > maxTimeoutSeconds {
				return nil, invalidParams("argumento %q de %s fora do intervalo 1..%d", a.name, spec.name, maxTimeoutSeconds)
			}
			out[a.name] = x
		case kindStrings:
			var x []string
			if json.Unmarshal(v, &x) != nil || hasNullItem(v) {
				return nil, bad
			}
			out[a.name] = x
		}
	}
	return out, nil
}

func (s *server) callTool(params json.RawMessage) (any, error) {
	fields, err := objectFields(params)
	if err != nil {
		return nil, invalidParams("params de tools/call: %v", err)
	}
	for k := range fields {
		if k != "name" && k != "arguments" && k != "_meta" {
			return nil, invalidParams("campo desconhecido em tools/call: %q", k)
		}
	}
	var name string
	if json.Unmarshal(fields["name"], &name) != nil || name == "" {
		return nil, invalidParams("tools/call sem name")
	}
	for _, t := range tools {
		if t.name != name {
			continue
		}
		a, err := parseArgs(t, fields["arguments"])
		if err != nil {
			return nil, err
		}
		start := time.Now()
		res := t.run(s, a)
		fmt.Fprintf(s.log, "pipeline-claude mcp: %s isError=%v (%s)\n", name, res.IsError, time.Since(start).Round(time.Millisecond))
		return res, nil
	}
	return nil, invalidParams("ferramenta desconhecida: %s", name)
}

func text(msg string, isError bool) ToolResult {
	return ToolResult{Content: []ToolContent{{Type: "text", Text: msg}}, IsError: isError}
}

func (s *server) toolDoctor(a args) ToolResult {
	cfg, wd, err := s.loadConfig()
	root := wd
	if cfg != nil {
		root = cfg.Root()
	}
	var buf bytes.Buffer
	code := 1
	if root != "" {
		code = doctor.Run(root, a.boolean("json"), &buf)
	}
	if err != nil {
		// Sem configuração o diagnóstico ainda ajuda, mas a resposta é erro.
		return text(err.Error()+"\n\n"+buf.String(), true)
	}
	return text(buf.String(), code != 0)
}

func (s *server) toolValidate(a args) ToolResult {
	cfg, _, err := s.loadConfig()
	if err != nil {
		return text(err.Error(), true)
	}
	target := a.str("target")
	if strings.ContainsAny(target, `/\`) || strings.HasSuffix(strings.ToLower(target), ".md") {
		t, err := hookio.ResolveInRoot(cfg.Root(), target)
		if err != nil {
			return text(fmt.Sprintf("não foi possível resolver %q: %v", target, err), true)
		}
		if !t.Inside {
			return text(fmt.Sprintf("report %q fora da raiz do projeto", target), true)
		}
	}
	var stdout, stderr bytes.Buffer
	code := validate.Run(validate.Options{
		Target:  target,
		BaseRef: a.str("base"),
		NoGit:   a.boolean("no_git"),
		JSON:    true,
		Getwd:   func() (string, error) { return cfg.Root(), nil },
	}, &stdout, &stderr)
	return text(joinOutput(stdout.String(), stderr.String()), code != validate.ExitOK)
}

func (s *server) toolSensors(a args) ToolResult {
	cfg, _, err := s.loadConfig()
	if err != nil {
		return text(err.Error(), true)
	}
	opts := sensors.Options{Names: a.strs("names")}
	if n := a.integer("timeout_seconds"); n > 0 {
		opts.Timeout = time.Duration(n) * time.Second
	}
	var buf bytes.Buffer
	code := sensors.Print(&buf, sensors.Run(s.ctx, cfg, opts), true)
	return text(buf.String(), code != 0)
}

func (s *server) toolDeliveryNew(a args) ToolResult {
	cfg, _, err := s.loadConfig()
	if err != nil {
		return text(err.Error(), true)
	}
	if err := checkDeliveryWritable(cfg); err != nil {
		return text(err.Error(), true)
	}
	created, err := delivery.New(cfg, delivery.NewOptions{Title: a.str("title"), Prefix: a.str("prefix")}, time.Now())
	if err != nil {
		return text(fmt.Sprintf("delivery não criada: %v", err), true)
	}
	return text(fmt.Sprintf("Delivery %s criada.\nReport: %s\nÍndice: %s\n", created.ID, created.ReportPath, created.IndexPath), false)
}

func (s *server) toolDiff(a args) ToolResult {
	cfg, _, err := s.loadConfig()
	if err != nil {
		return text(err.Error(), true)
	}
	base := a.str("base")
	if base == "" {
		base = cfg.BaseRef
	}
	d, err := gitdiff.Review(s.ctx, cfg.Root(), base, a.strs("paths"), maxDiff)
	if err != nil {
		return text(fmt.Sprintf("diff não calculado: %v", err), true)
	}
	if len(d.Files) == 0 {
		return text(fmt.Sprintf("Nenhum arquivo alterado contra %s (merge-base %s).\n", base, d.MergeBase), false)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Base: %s (merge-base %s)\nArquivos alterados (%d):\n", base, d.MergeBase, len(d.Files))
	for _, f := range d.Files {
		if slices.Contains(d.Untracked, f) {
			fmt.Fprintf(&b, "  %s (não rastreado: leia com Read)\n", f)
			continue
		}
		fmt.Fprintf(&b, "  %s\n", f)
	}
	if !a.boolean("name_only") {
		b.WriteString("\n")
		b.WriteString(d.Patch)
		if d.Truncated {
			fmt.Fprintf(&b, "\n[diff cortado: %d de %d bytes. Filtre com paths ou leia os arquivos com Read.]\n", len(d.Patch), d.Size)
		}
	}
	return text(b.String(), false)
}

// checkDeliveryWritable recusa pipeline_delivery_new quando deliveries_dir ou
// index_file está em protected_paths, inclusive por symlink, ou quando há
// caminho protegido dentro de deliveries_dir (TD-16). Roda antes de qualquer
// escrita.
func checkDeliveryWritable(cfg *config.Config) error {
	refuse := func(rel string) error {
		return fmt.Errorf("pipeline_delivery_new recusada: %s está em caminho protegido (protected_paths); o MCP não grava em caminho protegido (TD-16). Crie a delivery fora da sessão com `pipeline-claude delivery new`", rel)
	}
	for _, rel := range []string{cfg.DeliveriesDir, cfg.IndexFile} {
		if cfg.IsProtected(rel) {
			return refuse(rel)
		}
		t, err := hookio.ResolveInRoot(cfg.Root(), rel)
		if err != nil {
			return fmt.Errorf("pipeline_delivery_new recusada: não foi possível resolver %s: %v", rel, err)
		}
		if t.Inside && cfg.IsProtected(t.Rel) {
			return refuse(rel + " (" + t.Rel + ")")
		}
	}
	dd := strings.ToLower(cfg.DeliveriesDir) + "/"
	for _, p := range cfg.ProtectedPaths {
		if strings.HasPrefix(strings.ToLower(path.Clean(p))+"/", dd) {
			return refuse(p)
		}
	}
	return nil
}

func joinOutput(stdout, stderr string) string {
	if stdout != "" && stderr != "" {
		return stdout + "\n" + stderr
	}
	return stdout + stderr
}

// ------------------------------------------------------------------- recursos

const (
	uriConfig     = "pipeline://config"
	uriInvariants = "pipeline://invariants"
	uriIndex      = "pipeline://deliveries/index"
)

var resourceList = []Resource{
	{URI: uriConfig, Name: "Configuração efetiva", Description: "Conteúdo de .pipeline-claude.json com os padrões aplicados.", MimeType: "application/json"},
	{URI: uriInvariants, Name: "Invariantes do projeto", Description: "Arquivos de invariants_files, com título por arquivo.", MimeType: "text/markdown"},
	{URI: uriIndex, Name: "Índice de deliveries", Description: "Conteúdo de index_file.", MimeType: "text/markdown"},
}

func (s *server) readResource(params json.RawMessage) (any, error) {
	fields, err := objectFields(params)
	if err != nil {
		return nil, invalidParams("params de resources/read: %v", err)
	}
	for k := range fields {
		if k != "uri" && k != "_meta" {
			return nil, invalidParams("campo desconhecido em resources/read: %q", k)
		}
	}
	var uri string
	if json.Unmarshal(fields["uri"], &uri) != nil || uri == "" {
		return nil, invalidParams("resources/read sem uri")
	}
	var mime string
	for _, r := range resourceList {
		if r.URI == uri {
			mime = r.MimeType
		}
	}
	if mime == "" {
		return nil, invalidParams("recurso não encontrado: %s", uri)
	}

	cfg, _, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	var body string
	switch uri {
	case uriConfig:
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("falha ao serializar a configuração: %w", err)
		}
		body = string(data) + "\n"
	case uriInvariants:
		if len(cfg.InvariantsFiles) == 0 {
			body = "Nenhum arquivo em invariants_files.\n"
		}
		parts := make([]string, 0, len(cfg.InvariantsFiles))
		for _, rel := range cfg.InvariantsFiles {
			data, err := readInRoot(cfg.Root(), rel)
			if err != nil {
				return nil, err
			}
			parts = append(parts, "## "+rel+"\n\n"+strings.TrimRight(data, "\n")+"\n")
		}
		if len(parts) > 0 {
			body = strings.Join(parts, "\n")
		}
	case uriIndex:
		if body, err = readInRoot(cfg.Root(), cfg.IndexFile); err != nil {
			return nil, err
		}
	}
	return ReadResourceResult{Contents: []ResourceContent{{URI: uri, MimeType: mime, Text: body}}}, nil
}

// readInRoot lê rel, relativo à raiz, só se o arquivo resolvido (symlinks
// seguidos) for regular, ficar dentro da raiz e não passar de maxResource.
func readInRoot(root, rel string) (string, error) {
	t, err := hookio.ResolveInRoot(root, rel)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	if !t.Inside {
		return "", fmt.Errorf("%s: aponta para fora da raiz do projeto", rel)
	}
	info, err := os.Stat(t.Real)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: não é arquivo regular", rel)
	}
	if info.Size() > maxResource {
		return "", fmt.Errorf("%s: acima de %d MiB", rel, maxResource>>20)
	}
	data, err := os.ReadFile(t.Real)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	return string(data), nil
}

// ---------------------------------------------------------------- utilitários

func (s *server) write(resp Response) error {
	if resp.ID == nil {
		resp.ID = json.RawMessage("null")
	}
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(s.log, "pipeline-claude mcp: falha ao serializar resposta: %v\n", err)
		data, _ = json.Marshal(Response{JSONRPC: "2.0", ID: resp.ID, Error: &RPCError{CodeInternalError, "falha ao serializar resposta"}})
	}
	_, err = s.out.Write(append(data, '\n'))
	return err
}

func (s *server) writeError(id json.RawMessage, code int, msg string) error {
	return s.write(Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}})
}

// validID diz se id é string ou número JSON.
func validID(id json.RawMessage) bool {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

func isNull(raw json.RawMessage) bool {
	return raw != nil && string(bytes.TrimSpace(raw)) == "null"
}

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// objectFields decodifica raw como objeto por chave exata. Ausente ou null
// vale objeto vazio.
func objectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if raw == nil || isNull(raw) {
		return map[string]json.RawMessage{}, nil
	}
	if !isObject(raw) {
		return nil, errors.New("precisa ser um objeto")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errors.New("precisa ser um objeto")
	}
	return fields, nil
}

// hasNullItem diz se o array JSON raw tem algum item null.
func hasNullItem(raw json.RawMessage) bool {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return true
	}
	for _, it := range items {
		if isNull(it) {
			return true
		}
	}
	return false
}
