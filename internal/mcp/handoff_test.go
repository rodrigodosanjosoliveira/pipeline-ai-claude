package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// handoffRoot cria o projeto fictício com a delivery DEL-001 e dois subagentes.
func handoffRoot(t *testing.T, protected string) string {
	t.Helper()
	root := project(t, protected)
	write(t, root, "docs-ai/deliveries/DEL-001/report.md", "---\ndelivery_id: DEL-001\nstatus: draft\n---\n")
	write(t, root, ".claude/agents/pm-triage.md", "---\nname: pm-triage\n---\n")
	write(t, root, ".claude/agents/code-reviewer.md", "---\nname: code-reviewer\n---\n")
	return root
}

// TD-41: put grava em sequência e get devolve na ordem, com e sem agent.
func TestHandoffPutEGet(t *testing.T) {
	root := handoffRoot(t, `["internal/"]`)
	got := serve(t, root,
		call(1, "pipeline_handoff_get", `{"delivery":"DEL-001"}`),
		call(2, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"pm-triage","content":"STATUS: ok\nPROXIMO: code-reviewer\n"}`),
		call(3, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"code-reviewer","content":"STATUS: bloqueado\n"}`),
		call(4, "pipeline_handoff_get", `{"delivery":"DEL-001"}`),
		call(5, "pipeline_handoff_get", `{"delivery":"DEL-001","agent":"code-reviewer"}`),
	)
	want := []string{
		`{"id":1,"result":{"isError":false,"content":[{"type":"text","text":"~Nenhum handoff"}]}}`,
		`{"id":2,"result":{"isError":false,"content":[{"type":"text","text":"~docs-ai/deliveries/DEL-001/handoffs/01-pm-triage.md"}]}}`,
		`{"id":3,"result":{"isError":false,"content":[{"type":"text","text":"~docs-ai/deliveries/DEL-001/handoffs/02-code-reviewer.md"}]}}`,
		`{"id":4,"result":{"isError":false,"content":[{"type":"text","text":"~PROXIMO: code-reviewer"}]}}`,
		`{"id":5,"result":{"isError":false,"content":[{"type":"text","text":"~STATUS: bloqueado"}]}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("respostas = %d: %v", len(got), got)
	}
	for i, w := range want {
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("resposta %d: %v\n%v", i, err, got[i])
		}
	}
	all := got[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if i, j := strings.Index(all, "01-pm-triage.md"), strings.Index(all, "02-code-reviewer.md"); i < 0 || j < i {
		t.Errorf("get fora de ordem:\n%s", all)
	}
	only := got[4]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(only, "pm-triage") {
		t.Errorf("get com agent trouxe outro subagente:\n%s", only)
	}
}

// Erro de conteúdo é isError sem gravar; tipo errado é -32602.
func TestHandoffRecusas(t *testing.T) {
	root := handoffRoot(t, `["internal/"]`)
	before := snapshot(t, root)
	got := serve(t, root,
		call(1, "pipeline_handoff_put", `{"delivery":"DEL-009","agent":"pm-triage","content":"x"}`),
		call(2, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"release-ops","content":"x"}`),
		call(3, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"pm-triage","content":""}`),
		call(4, "pipeline_handoff_put", `{"delivery":"../DEL-001","agent":"pm-triage","content":"x"}`),
		call(5, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"pm-triage"}`),
		call(6, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"pm-triage","content":7}`),
		call(7, "pipeline_handoff_get", `{"delivery":"DEL-001","agent":"Code Reviewer"}`),
		call(8, "pipeline_handoff_get", `{}`),
	)
	want := []string{
		`{"id":1,"result":{"isError":true,"content":[{"type":"text","text":"~não existe"}]}}`,
		`{"id":2,"result":{"isError":true,"content":[{"type":"text","text":"~release-ops"}]}}`,
		`{"id":3,"result":{"isError":true,"content":[{"type":"text","text":"~vazio"}]}}`,
		`{"id":4,"result":{"isError":true,"content":[{"type":"text","text":"~ID"}]}}`,
		`{"id":5,"error":{"code":-32602}}`,
		`{"id":6,"error":{"code":-32602}}`,
		`{"id":7,"result":{"isError":true,"content":[{"type":"text","text":"~agent"}]}}`,
		`{"id":8,"error":{"code":-32602}}`,
	}
	if len(got) != len(want) {
		t.Fatalf("respostas = %d: %v", len(got), got)
	}
	for i, w := range want {
		if err := match(obj(t, w), got[i]); err != nil {
			t.Errorf("resposta %d: %v\n%v", i, err, got[i])
		}
	}
	if after := snapshot(t, root); after != before {
		t.Errorf("recusa gravou no disco:\nantes:\n%s\ndepois:\n%s", before, after)
	}
}

// TD-16: o MCP não grava em caminho protegido; get continua só leitura.
func TestHandoffPutRecusadoEmCaminhoProtegido(t *testing.T) {
	for _, protected := range []string{`["docs-ai/"]`, `["docs-ai/deliveries/DEL-001/handoffs/"]`} {
		t.Run(protected, func(t *testing.T) {
			root := handoffRoot(t, protected)
			before := snapshot(t, root)
			got := serve(t, root,
				call(1, "pipeline_handoff_put", `{"delivery":"DEL-001","agent":"pm-triage","content":"x"}`),
				call(2, "pipeline_handoff_get", `{"delivery":"DEL-001"}`),
			)
			for i, w := range []string{
				`{"id":1,"result":{"isError":true,"content":[{"type":"text","text":"~protegido"}]}}`,
				`{"id":2,"result":{"isError":false}}`,
			} {
				if err := match(obj(t, w), got[i]); err != nil {
					t.Errorf("resposta %d: %v\n%v", i, err, got[i])
				}
			}
			if after := snapshot(t, root); after != before {
				t.Errorf("disco mudou")
			}
			if _, err := os.Stat(filepath.Join(root, "docs-ai", "deliveries", "DEL-001", "handoffs")); !os.IsNotExist(err) {
				t.Errorf("pasta de handoffs criada: %v", err)
			}
		})
	}
}
