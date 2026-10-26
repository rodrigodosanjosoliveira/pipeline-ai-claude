// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/mcp/protocol.go @ 0eac73a (MIT).
// Alterações: id como json.RawMessage, ecoado sem conversão e null em erro de
// parse (TD-21); Request removido: a requisição é lida por chave exata, como
// o payload do hook; isError sempre presente no resultado de ferramenta.

package mcp

import "encoding/json"

// Response representa a resposta JSON-RPC 2.0 emitida pelo servidor MCP.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError representa um erro segundo o padrão JSON-RPC 2.0.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Códigos de erro do JSON-RPC 2.0.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Tool define uma ferramenta exposta pelo servidor MCP.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolContent representa um bloco de conteúdo na resposta da ferramenta.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult é a estrutura de resultado de execução de uma ferramenta no MCP.
type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError"`
}

// Resource representa um recurso de contexto exposto no MCP.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType"`
}

// ResourceContent representa o conteúdo retornado por um recurso lido.
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

// ReadResourceResult representa a resposta estruturada para resources/read.
type ReadResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}
