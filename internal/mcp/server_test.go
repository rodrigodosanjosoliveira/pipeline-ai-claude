// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/mcp/server_test.go @ 0eac73a (MIT).
// Alterações: testes por transcrição (entrada em linhas, saída comparada por
// campo); casos originais mantidos onde a ferramenta existe (initialize, ping,
// tools/list, doctor, validate, resources, criação de delivery), adaptados às
// ferramentas e recursos do TD-21; pipeline_sync_skills removido (TD-16);
// tools/list com pipeline_diff (TD-29, casos em diff_test.go).

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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// helperEnv liga TestHelperProcess: os sensores do projeto de teste rodam o
// próprio binário de teste (os.Args[0] -test.run=^TestHelperProcess$ -- <modo>),
// sem depender de sh.
const helperEnv = "PIPELINE_CLAUDE_MCP_HELPER"

func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "ok":
		fmt.Println("sensor ok")
		os.Exit(0)
	default:
		fmt.Println("sensor falhou")
		os.Exit(3)
	}
}

const indexHead = "# Deliveries\n\n| ID | Título | Status | Data |\n|---|---|---|---|\n"

const invariants = "# Invariantes\n\n- INV-01 valores monetários em centavos\n"

// project cria um projeto fictício (biblioteca de bairro) e devolve a raiz.
// protected substitui protected_paths.
func project(t testing.TB, protected string) string {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sensor := func(name, mode string) string {
		argv, _ := json.Marshal([]string{exe, "-test.run=^TestHelperProcess$", "--", mode})
		return fmt.Sprintf(`{"name": %q, "cmd": %s}`, name, argv)
	}
	cfg := `{"version": 1, "project_type": "api", "database": "postgres", "git_provider": "github",
 "delivery_prefixes": ["DEL"],
 "delivery_id_pattern": "^DEL-[0-9]{3}$",
 "invariants_files": ["docs-ai/INVARIANTES.md"],
 "protected_paths": ` + protected + `,
 "sensors": [` + sensor("passa", "ok") + `, ` + sensor("falha", "fail") + `]}`
	write(t, root, config.FileName, cfg)
	write(t, root, "docs-ai/deliveries/INDEX.md", indexHead)
	write(t, root, "docs-ai/INVARIANTES.md", invariants)
	return root
}

func write(t testing.TB, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serve roda o servidor sobre as linhas de entrada e devolve as linhas de
// saída decodificadas. Toda linha de stdout tem de ser JSON.
func serve(t *testing.T, root string, lines ...string) []map[string]any {
	t.Helper()
	t.Setenv(helperEnv, "1")
	var out, errOut bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	getwd := func() (string, error) { return root, nil }
	if err := Serve(context.Background(), in, &out, &errOut, getwd); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var got []map[string]any
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("linha de stdout não é JSON: %v: %q", err, sc.Text())
		}
		got = append(got, m)
	}
	return got
}

// match confere que got contém want: objetos por campo (campos a mais em got
// são aceitos), arrays com o mesmo tamanho, escalares iguais. Uma string em
// want que começa com "~" exige só que o valor contenha o resto.
func match(want, got any) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("quer objeto, obteve %v", got)
		}
		for k, wv := range w {
			gv, present := g[k]
			if wv == absent {
				if present {
					return fmt.Errorf("%s: deveria estar ausente, obteve %v", k, gv)
				}
				continue
			}
			if !present {
				return fmt.Errorf("%s: ausente", k)
			}
			if err := match(wv, gv); err != nil {
				return fmt.Errorf("%s.%w", k, err)
			}
		}
		return nil
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Errorf("quer array de %d, obteve %v", len(w), got)
		}
		for i := range w {
			if err := match(w[i], g[i]); err != nil {
				return fmt.Errorf("[%d].%w", i, err)
			}
		}
		return nil
	case string:
		if rest, ok := strings.CutPrefix(w, "~"); ok {
			g, isStr := got.(string)
			if !isStr || !strings.Contains(g, rest) {
				return fmt.Errorf("quer trecho %q, obteve %v", rest, got)
			}
			return nil
		}
	}
	if fmt.Sprint(want) != fmt.Sprint(got) {
		return fmt.Errorf("quer %v, obteve %v", want, got)
	}
	return nil
}

// absent marca, em want, campo que não pode aparecer.
const absent = "<ausente>"

// obj decodifica um literal JSON de expectativa.
func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("expectativa inválida %s: %v", s, err)
	}
	return m
}

func call(id int, tool, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, args)
}

func read(id int, uri string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"resources/read","params":{"uri":%q}}`, id, uri)
}

type transcript struct {
	name      string
	protected string // protected_paths; vazio = ["internal/"]
	noConfig  bool
	in        []string
	want      []string // uma expectativa por linha de saída, na ordem
}

func runTranscripts(t *testing.T, cases []transcript) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			protected := tc.protected
			if protected == "" {
				protected = `["internal/"]`
			}
			root := project(t, protected)
			if tc.noConfig {
				if err := os.Remove(filepath.Join(root, config.FileName)); err != nil {
					t.Fatal(err)
				}
			}
			got := serve(t, root, tc.in...)
			if len(got) != len(tc.want) {
				t.Fatalf("respostas = %d, quer %d: %v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if err := match(obj(t, w), got[i]); err != nil {
					t.Errorf("resposta %d: %v\n%v", i, err, got[i])
				}
			}
		})
	}
}

func TestProtocolo(t *testing.T) {
	long := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", MaxLine) + `"}}`
	runTranscripts(t, []transcript{
		{
			name: "initialize e ping (caso original)",
			in: []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`,
			},
			want: []string{
				`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{},"resources":{}},"serverInfo":{"name":"pipeline-claude","version":"` + buildinfo.Version() + `"}}}`,
				`{"jsonrpc":"2.0","id":2,"result":{}}`,
			},
		},
		{
			name: "versões suportadas ecoadas",
			in: []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
				`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
			},
			want: []string{
				`{"id":1,"result":{"protocolVersion":"2025-03-26"}}`,
				`{"id":2,"result":{"protocolVersion":"2025-06-18"}}`,
			},
		},
		{
			name: "versão desconhecida devolve a mais nova",
			in:   []string{`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"1999-01-01"}}`},
			want: []string{`{"id":"a","result":{"protocolVersion":"2025-06-18"}}`},
		},
		{
			name: "id string preservado",
			in:   []string{`{"jsonrpc":"2.0","id":"x-1","method":"ping"}`},
			want: []string{`{"id":"x-1","result":{}}`},
		},
		{
			name: "notificações nunca respondem, nem método desconhecido",
			in: []string{
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
				`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"pipeline_doctor","arguments":{"x":1}}}`,
				`{"jsonrpc":"2.0","id":9,"method":"ping"}`,
			},
			want: []string{`{"id":9,"result":{}}`},
		},
		{
			name: "método desconhecido",
			in:   []string{`{"jsonrpc":"2.0","id":3,"method":"sampling/createMessage"}`},
			want: []string{`{"id":3,"error":{"code":-32601,"message":"~sampling/createMessage"},"result":"<ausente>"}`},
		},
		{
			name: "JSON inválido seguido de requisição válida",
			in:   []string{`{"jsonrpc":"2.0","id":4,"method":`, `{"jsonrpc":"2.0","id":5,"method":"ping"}`},
			want: []string{`{"jsonrpc":"2.0","id":null,"error":{"code":-32700}}`, `{"id":5,"result":{}}`},
		},
		{
			name: "linha longa",
			in:   []string{long, `{"jsonrpc":"2.0","id":6,"method":"ping"}`},
			want: []string{`{"id":null,"error":{"code":-32700,"message":"~1 MiB"}}`, `{"id":6,"result":{}}`},
		},
		{
			name: "linhas em branco ignoradas e CRLF aceito",
			in:   []string{"", "   ", "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"ping\"}\r"},
			want: []string{`{"id":7,"result":{}}`},
		},
		{
			name: "requisição malformada",
			in: []string{
				`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
				`"ping"`,
				`{"jsonrpc":"1.0","id":1,"method":"ping"}`,
				`{"jsonrpc":"2.0","id":2}`,
				`{"jsonrpc":"2.0","id":{"a":1},"method":"ping"}`,
				`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
				`{"jsonrpc":"2.0","id":3,"method":7}`,
				`{"jsonrpc":"2.0","id":4,"method":"ping","params":[1]}`,
				`{"jsonrpc":"2.0","method":"ping","params":"x"}`,
			},
			want: []string{
				`{"id":null,"error":{"code":-32600}}`,
				`{"id":null,"error":{"code":-32600}}`,
				`{"id":1,"error":{"code":-32600}}`,
				`{"id":2,"error":{"code":-32600}}`,
				`{"id":null,"error":{"code":-32600}}`,
				`{"id":null,"error":{"code":-32600}}`,
				`{"id":3,"error":{"code":-32600}}`,
				`{"id":4,"error":{"code":-32602}}`,
			},
		},
		{
			name: "chaves por nome exato",
			in:   []string{`{"jsonrpc":"2.0","id":8,"Method":"ping"}`},
			want: []string{`{"id":8,"error":{"code":-32600}}`},
		},
	})
}

func TestFerramentas(t *testing.T) {
	runTranscripts(t, []transcript{
		{
			name: "tools/list (caso original)",
			in:   []string{`{"jsonrpc":"2.0","id":10,"method":"tools/list","params":{}}`},
			want: []string{`{"id":10,"result":{"tools":[
				{"name":"pipeline_doctor","inputSchema":{"type":"object","additionalProperties":false}},
				{"name":"pipeline_validate","inputSchema":{"type":"object","additionalProperties":false,"required":["target"]}},
				{"name":"pipeline_sensors","inputSchema":{"type":"object","additionalProperties":false}},
				{"name":"pipeline_delivery_new","inputSchema":{"type":"object","additionalProperties":false,"required":["title"]}},
				{"name":"pipeline_diff","inputSchema":{"type":"object","additionalProperties":false}}]}}`},
		},
		{
			name: "doctor (caso original)",
			in:   []string{call(20, "pipeline_doctor", `{}`), call(21, "pipeline_doctor", `{"json":true}`)},
			want: []string{
				`{"id":20,"result":{"content":[{"type":"text","text":"~pipeline-claude doctor"}]}}`,
				`{"id":21,"result":{"content":[{"type":"text","text":"~\"checks\""}]}}`,
			},
		},
		{
			name: "validate sem target (caso original: agora parâmetro inválido)",
			in:   []string{call(30, "pipeline_validate", `{}`)},
			want: []string{`{"id":30,"error":{"code":-32602,"message":"~target"}}`},
		},
		{
			name: "validate de delivery inexistente",
			in:   []string{call(31, "pipeline_validate", `{"target":"DEL-404","no_git":true}`)},
			want: []string{`{"id":31,"result":{"isError":true,"content":[{"type":"text","text":"~DEL-404"}]}}`},
		},
		{
			name: "validate de caminho fora da raiz",
			in:   []string{call(32, "pipeline_validate", `{"target":"../fora/report.md","no_git":true}`)},
			want: []string{`{"id":32,"result":{"isError":true,"content":[{"type":"text","text":"~fora da raiz"}]}}`},
		},
		{
			name: "sensors: todos, com falha",
			in:   []string{call(40, "pipeline_sensors", `{}`)},
			want: []string{`{"id":40,"result":{"isError":true,"content":[{"type":"text","text":"~\"ok\": false"}]}}`},
		},
		{
			name: "sensors: só o que passa",
			in:   []string{call(41, "pipeline_sensors", `{"names":["passa"],"timeout_seconds":60}`)},
			want: []string{`{"id":41,"result":{"isError":false,"content":[{"type":"text","text":"~sensor ok"}]}}`},
		},
		{
			name: "sensors: nome desconhecido",
			in:   []string{call(42, "pipeline_sensors", `{"names":["nenhum"]}`)},
			want: []string{`{"id":42,"result":{"isError":true,"content":[{"type":"text","text":"~sensor desconhecido"}]}}`},
		},
		{
			name: "argumentos inválidos",
			in: []string{
				call(50, "pipeline_doctor", `{"dir":"."}`),
				call(51, "pipeline_doctor", `{"json":"sim"}`),
				call(52, "pipeline_validate", `{"target":7}`),
				call(53, "pipeline_validate", `{"target":"DEL-001","no_git":1}`),
				call(54, "pipeline_sensors", `{"names":"passa"}`),
				call(55, "pipeline_sensors", `{"names":[1]}`),
				call(56, "pipeline_sensors", `{"timeout_seconds":1.5}`),
				call(57, "pipeline_sensors", `{"timeout_seconds":0}`),
				call(58, "pipeline_delivery_new", `{"title":"x","type":"feat"}`),
				call(59, "pipeline_delivery_new", `{"prefix":"DEL"}`),
				call(60, "pipeline_delivery_new", `[]`),
				`{"jsonrpc":"2.0","id":61,"method":"tools/call","params":{"arguments":{}}}`,
				`{"jsonrpc":"2.0","id":62,"method":"tools/call","params":{"name":"pipeline_doctor","arguments":{},"extra":1}}`,
				call(63, "pipeline_sync_skills", `{}`),
				call(64, "pipeline_validate", `{"Target":"DEL-001"}`),
				call(65, "pipeline_sensors", `{"names":[null]}`),
				call(66, "pipeline_validate", `{"target":null}`),
			},
			want: []string{
				`{"id":50,"error":{"code":-32602,"message":"~dir"}}`,
				`{"id":51,"error":{"code":-32602,"message":"~json"}}`,
				`{"id":52,"error":{"code":-32602}}`,
				`{"id":53,"error":{"code":-32602}}`,
				`{"id":54,"error":{"code":-32602}}`,
				`{"id":55,"error":{"code":-32602}}`,
				`{"id":56,"error":{"code":-32602}}`,
				`{"id":57,"error":{"code":-32602}}`,
				`{"id":58,"error":{"code":-32602,"message":"~type"}}`,
				`{"id":59,"error":{"code":-32602,"message":"~title"}}`,
				`{"id":60,"error":{"code":-32602}}`,
				`{"id":61,"error":{"code":-32602}}`,
				`{"id":62,"error":{"code":-32602}}`,
				`{"id":63,"error":{"code":-32602,"message":"~pipeline_sync_skills"}}`,
				`{"id":64,"error":{"code":-32602}}`,
				`{"id":65,"error":{"code":-32602}}`,
				`{"id":66,"error":{"code":-32602}}`,
			},
		},
		{
			name:     "config ausente: toda ferramenta é isError com a dica do doctor",
			noConfig: true,
			in: []string{
				call(70, "pipeline_doctor", `{}`),
				call(71, "pipeline_validate", `{"target":"DEL-001"}`),
				call(72, "pipeline_sensors", `{}`),
				call(73, "pipeline_delivery_new", `{"title":"Multa"}`),
			},
			want: []string{
				`{"id":70,"result":{"isError":true,"content":[{"type":"text","text":"~configuração"}]}}`,
				`{"id":71,"result":{"isError":true,"content":[{"type":"text","text":"~pipeline-claude doctor"}]}}`,
				`{"id":72,"result":{"isError":true,"content":[{"type":"text","text":"~pipeline-claude doctor"}]}}`,
				`{"id":73,"result":{"isError":true,"content":[{"type":"text","text":"~pipeline-claude doctor"}]}}`,
			},
		},
	})
}

func TestDeliveryNewEValidate(t *testing.T) {
	root := project(t, `["internal/"]`)
	got := serve(t, root,
		call(1, "pipeline_delivery_new", `{"title":"Multa por atraso"}`),
		call(2, "pipeline_delivery_new", `{"title":"Reserva de livros","prefix":"DEL"}`),
		call(3, "pipeline_delivery_new", `{"title":"a|b"}`),
		call(4, "pipeline_validate", `{"target":"DEL-001","no_git":true}`),
	)
	want := []string{
		`{"id":1,"result":{"isError":false,"content":[{"type":"text","text":"~DEL-001"}]}}`,
		`{"id":2,"result":{"isError":false,"content":[{"type":"text","text":"~DEL-002"}]}}`,
		`{"id":3,"result":{"isError":true}}`,
		`{"id":4,"result":{"isError":true,"content":[{"type":"text","text":"~placeholder"}]}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("respostas = %d: %v", len(got), got)
	}
	for i, w := range want {
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("resposta %d: %v\n%v", i, err, got[i])
		}
	}
	index, err := os.ReadFile(filepath.Join(root, "docs-ai", "deliveries", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "DEL-001") || !strings.Contains(string(index), "DEL-002") {
		t.Errorf("índice sem as deliveries:\n%s", index)
	}

	// Preenchido, o report aprova (caso original "relatório válido").
	reportPath := filepath.Join(root, "docs-ai", "deliveries", "DEL-001", "report.md")
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.ReplaceAll(string(data), "<preencher>", "conferido na revisão")
	filled = strings.ReplaceAll(filled, "- [ ]", "- [x]")
	write(t, root, "docs-ai/deliveries/DEL-001/report.md", filled)
	got = serve(t, root,
		call(5, "pipeline_validate", `{"target":"DEL-001","no_git":true}`),
		call(6, "pipeline_validate", `{"target":"docs-ai/deliveries/DEL-001/report.md","no_git":true}`),
	)
	for i, id := range []int{5, 6} {
		w := fmt.Sprintf(`{"id":%d,"result":{"isError":false,"content":[{"type":"text","text":"~\"ok\": true"}]}}`, id)
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("validate preenchido: %v\n%v", err, got[i])
		}
	}
}

// TASK-053: scope e flags chegam ao report; erro de conteúdo é isError, não -32602.
func TestDeliveryNewEscopoEFlags(t *testing.T) {
	root := project(t, `["internal/"]`)
	cfg, err := os.ReadFile(filepath.Join(root, ".pipeline-claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, ".pipeline-claude.json", strings.Replace(string(cfg), `"delivery_prefixes"`, `"report_template": "docs-ai/modelo.md", "delivery_prefixes"`, 1))
	write(t, root, "docs-ai/modelo.md", "---\ndelivery_id: <delivery_id>\nstatus: draft\nrequire_security: false\n---\n\n## PM/Triage\n- Escopo fechado: <escopo>\n")
	got := serve(t, root,
		call(1, "pipeline_delivery_new", `{"title":"Login","scope":"Login por senha","flags":["require_security"]}`),
		call(2, "pipeline_delivery_new", `{"title":"Outra","flags":["require_magia"]}`),
		call(3, "pipeline_delivery_new", `{"title":"Outra","scope":"a\nb"}`),
		call(4, "pipeline_delivery_new", `{"title":"Outra","scope":7}`),
		call(5, "pipeline_delivery_new", `{"title":"Outra","flags":"require_security"}`),
		call(6, "pipeline_delivery_new", `{"title":"Outra","flags":[null]}`),
	)
	want := []string{
		`{"id":1,"result":{"isError":false,"content":[{"type":"text","text":"~DEL-001"}]}}`,
		`{"id":2,"result":{"isError":true,"content":[{"type":"text","text":"~require_magia"}]}}`,
		`{"id":3,"result":{"isError":true,"content":[{"type":"text","text":"~uma linha"}]}}`,
		`{"id":4,"error":{"code":-32602}}`,
		`{"id":5,"error":{"code":-32602}}`,
		`{"id":6,"error":{"code":-32602}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("respostas = %d: %v", len(got), got)
	}
	for i, w := range want {
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("resposta %d: %v\n%v", i, err, got[i])
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "docs-ai", "deliveries", "DEL-001", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"- Escopo fechado: Login por senha\n", "require_security: true\n", "\n## Security\n- Resultado: <preencher>\n"} {
		if !strings.Contains(string(data), w) {
			t.Errorf("report sem %q:\n%s", w, data)
		}
	}
	index, err := os.ReadFile(filepath.Join(root, "docs-ai", "deliveries", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "DEL-002") {
		t.Errorf("entrada recusada criou delivery:\n%s", index)
	}
}

// TD-16: delivery_new nunca grava em caminho protegido.
func TestDeliveryNewRecusadaEmCaminhoProtegido(t *testing.T) {
	tests := []struct {
		name      string
		protected string
		setup     func(t *testing.T, root string)
	}{
		{name: "deliveries_dir protegido", protected: `["docs-ai/"]`},
		{name: "deliveries_dir protegido, caixa diferente", protected: `["Docs-AI/Deliveries/"]`},
		{name: "index_file protegido", protected: `["docs-ai/deliveries/INDEX.md"]`},
		{name: "caminho protegido dentro de deliveries_dir", protected: `["docs-ai/deliveries/DEL-001/"]`},
		{
			// O hook enxerga o caminho declarado: protegido, mesmo que o link
			// leve a um lugar livre.
			name:      "deliveries_dir protegido é link para caminho livre",
			protected: `["docs-ai/"]`,
			setup: func(t *testing.T, root string) {
				dd := filepath.Join(root, "docs-ai", "deliveries")
				target := filepath.Join(root, "registros")
				if err := os.Rename(dd, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dd); err != nil {
					t.Skipf("symlink indisponível: %v", err)
				}
			},
		},
		{
			name:      "deliveries_dir é link para caminho protegido",
			protected: `["internal/"]`,
			setup: func(t *testing.T, root string) {
				dd := filepath.Join(root, "docs-ai", "deliveries")
				target := filepath.Join(root, "internal", "deliveries")
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(dd, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dd); err != nil {
					t.Skipf("symlink indisponível: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := project(t, tt.protected)
			if tt.setup != nil {
				tt.setup(t, root)
			}
			before := snapshot(t, root)
			got := serve(t, root, call(1, "pipeline_delivery_new", `{"title":"Multa por atraso"}`))
			if len(got) != 1 {
				t.Fatalf("respostas: %v", got)
			}
			if err := match(obj(t, `{"id":1,"result":{"isError":true,"content":[{"type":"text","text":"~protegido"}]}}`), got[0]); err != nil {
				t.Errorf("%v\n%v", err, got[0])
			}
			if after := snapshot(t, root); after != before {
				t.Errorf("disco mudou:\nantes:\n%s\ndepois:\n%s", before, after)
			}
		})
	}
}

// snapshot descreve a árvore em root (caminho, tipo e conteúdo).
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s %v", rel, d.Type())
		if d.Type().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " %q", data)
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRecursos(t *testing.T) {
	runTranscripts(t, []transcript{
		{
			name: "resources/list (caso original)",
			in:   []string{`{"jsonrpc":"2.0","id":40,"method":"resources/list","params":{}}`},
			want: []string{`{"id":40,"result":{"resources":[
				{"uri":"pipeline://config","mimeType":"application/json"},
				{"uri":"pipeline://invariants","mimeType":"text/markdown"},
				{"uri":"pipeline://deliveries/index","mimeType":"text/markdown"}]}}`},
		},
		{
			name: "resources/read",
			in: []string{
				read(41, "pipeline://invariants"),
				read(42, "pipeline://deliveries/index"),
				read(43, "pipeline://config"),
			},
			want: []string{
				`{"id":41,"result":{"contents":[{"uri":"pipeline://invariants","mimeType":"text/markdown","text":"~## docs-ai/INVARIANTES.md\n\n# Invariantes\n\n- INV-01"}]}}`,
				`{"id":42,"result":{"contents":[{"uri":"pipeline://deliveries/index","mimeType":"text/markdown","text":"~| ID | Título | Status | Data |"}]}}`,
				`{"id":43,"result":{"contents":[{"uri":"pipeline://config","mimeType":"application/json","text":"~\"base_ref\": \"origin/main\""}]}}`,
			},
		},
		{
			name: "URI desconhecida e parâmetros inválidos",
			in: []string{
				read(44, "pipeline://catalog"),
				`{"jsonrpc":"2.0","id":45,"method":"resources/read","params":{}}`,
				`{"jsonrpc":"2.0","id":46,"method":"resources/read","params":{"uri":"pipeline://config","x":1}}`,
			},
			want: []string{
				`{"id":44,"error":{"code":-32602,"message":"~pipeline://catalog"}}`,
				`{"id":45,"error":{"code":-32602}}`,
				`{"id":46,"error":{"code":-32602}}`,
			},
		},
		{
			name:     "config ausente",
			noConfig: true,
			in:       []string{read(47, "pipeline://config")},
			want:     []string{`{"id":47,"error":{"code":-32603,"message":"~pipeline-claude doctor"}}`},
		},
	})

	t.Run("invariante ausente é erro do recurso", func(t *testing.T) {
		root := project(t, `["internal/"]`)
		if err := os.Remove(filepath.Join(root, "docs-ai", "INVARIANTES.md")); err != nil {
			t.Fatal(err)
		}
		got := serve(t, root, read(1, "pipeline://invariants"))
		if len(got) != 1 {
			t.Fatalf("respostas: %v", got)
		}
		if err := match(obj(t, `{"id":1,"error":{"code":-32603,"message":"~docs-ai/INVARIANTES.md"}}`), got[0]); err != nil {
			t.Error(err)
		}
	})
	t.Run("invariante por link para fora da raiz", func(t *testing.T) {
		root, outside := project(t, `["internal/"]`), t.TempDir()
		write(t, outside, "segredo.md", "segredo")
		inv := filepath.Join(root, "docs-ai", "INVARIANTES.md")
		if err := os.Remove(inv); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "segredo.md"), inv); err != nil {
			t.Skipf("symlink indisponível: %v", err)
		}
		got := serve(t, root, read(1, "pipeline://invariants"))
		if len(got) != 1 {
			t.Fatalf("respostas: %v", got)
		}
		if err := match(obj(t, `{"id":1,"error":{"code":-32603,"message":"~fora da raiz"}}`), got[0]); err != nil {
			t.Error(err)
		}
	})
}

func TestLimiteDeLinha(t *testing.T) {
	root := project(t, `["internal/"]`)
	sized := func(id, n int) string {
		head := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping","params":{"x":"`, id)
		const tail = `"}}`
		return head + strings.Repeat("a", n-len(head)-len(tail)) + tail
	}
	got := serve(t, root, sized(1, MaxLine), sized(2, MaxLine+1), sized(3, MaxLine)+"\r")
	want := []string{`{"id":1,"result":{}}`, `{"id":null,"error":{"code":-32700}}`, `{"id":3,"result":{}}`}
	if len(got) != len(want) {
		t.Fatalf("respostas = %d", len(got))
	}
	for i, w := range want {
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("resposta %d: %v", i, err)
		}
	}
}

func TestStdoutSoProtocolo(t *testing.T) {
	root := project(t, `["internal/"]`)
	t.Setenv(helperEnv, "1")
	var out, errOut bytes.Buffer
	in := strings.NewReader(strings.Join([]string{
		call(1, "pipeline_sensors", `{}`),
		call(2, "pipeline_doctor", `{}`),
		`lixo`,
	}, "\n"))
	if err := Serve(context.Background(), in, &out, &errOut, func() (string, error) { return root, nil }); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("linhas = %d: %q", len(lines), out.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil || m["jsonrpc"] != "2.0" {
			t.Errorf("linha fora do protocolo: %q", l)
		}
	}
}

func TestEOFSemQuebraDeLinha(t *testing.T) {
	root := project(t, `["internal/"]`)
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if err := Serve(context.Background(), in, &out, io.Discard, func() (string, error) { return root, nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":1`) {
		t.Errorf("sem resposta à última linha: %q", out.String())
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin fechado") }

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("stdout fechado") }

func TestErrosDeES(t *testing.T) {
	getwd := func() (string, error) { return t.TempDir(), nil }
	if err := Serve(context.Background(), errReader{}, io.Discard, io.Discard, getwd); err == nil {
		t.Error("erro de leitura engolido")
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if err := Serve(context.Background(), in, errWriter{}, io.Discard, getwd); err == nil {
		t.Error("erro de escrita engolido")
	}
}

func TestContextoCancelado(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, pr, io.Discard, io.Discard, func() (string, error) { return t.TempDir(), nil })
	}()
	go pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("erro = %v, quer context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve não terminou com o contexto cancelado")
	}
}

func FuzzServe(f *testing.F) {
	root := project(f, `["internal/"]`)
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"pipeline://config"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"pipeline_validate","arguments":{"target":"DEL-001","no_git":true}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		"{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"ping\"}\n{",
		`[]`, `null`, ``, "\n\n",
	} {
		f.Add([]byte(s))
	}
	getwd := func() (string, error) { return root, nil }
	f.Fuzz(func(t *testing.T, data []byte) {
		// Ferramentas que executam processos ou gravam ficam fora do fuzz: o
		// alvo é o parser e o despacho.
		if bytes.Contains(data, []byte("pipeline_sensors")) || bytes.Contains(data, []byte("pipeline_delivery_new")) || bytes.Contains(data, []byte("pipeline_doctor")) {
			return
		}
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- Serve(context.Background(), bytes.NewReader(data), &out, io.Discard, getwd) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Serve não terminou para %q", data)
		}
		if out.Len() == 0 {
			return
		}
		if !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
			t.Fatalf("saída sem quebra de linha final: %q", out.Bytes())
		}
		for _, line := range bytes.Split(bytes.TrimSuffix(out.Bytes(), []byte("\n")), []byte("\n")) {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatalf("linha não é JSON: %q", line)
			}
			if string(m["jsonrpc"]) != `"2.0"` {
				t.Fatalf("linha sem jsonrpc 2.0: %q", line)
			}
			if _, ok := m["id"]; !ok {
				t.Fatalf("resposta sem id: %q", line)
			}
			_, hasResult := m["result"]
			_, hasError := m["error"]
			if hasResult == hasError {
				t.Fatalf("resposta precisa de exatamente um de result/error: %q", line)
			}
		}
	})
}
