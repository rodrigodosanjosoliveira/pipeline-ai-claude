package templates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/mcp"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

// Fica no pacote externo porque internal/mcp depende de templates (via doctor
// e syncer); ver templates.MCPToolNames em templates_test.go.
func init() {
	templates.MCPToolNames = mcpToolNames
}

// mcpToolNames pergunta ao servidor MCP (tools/list) quais ferramentas ele
// expõe.
func mcpToolNames() ([]string, error) {
	dir, err := os.MkdirTemp("", "templates-mcp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var out, errOut bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	getwd := func() (string, error) { return dir, nil }
	if err := mcp.Serve(context.Background(), in, &out, &errOut, getwd); err != nil {
		return nil, fmt.Errorf("mcp.Serve: %w", err)
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		return nil, fmt.Errorf("resposta do tools/list ilegível: %w: %s", err, out.String())
	}
	var names []string
	for _, tl := range resp.Result.Tools {
		names = append(names, tl.Name)
	}
	return names, nil
}
