package goapitomcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer creates an MCP server populated with tools derived from the
// configured OpenAPI document.
func NewServer(ctx context.Context, cfg Config) (*mcp.Server, error) {
	catalog, err := LoadCatalog(ctx, cfg)
	if err != nil {
		return nil, err
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    catalog.Name,
		Version: catalog.Version,
	}, &mcp.ServerOptions{})

	for _, operation := range catalog.Operations {
		op := operation
		server.AddTool(&mcp.Tool{
			Name:         op.ToolName,
			Title:        op.Title,
			Description:  op.Description,
			InputSchema:  op.InputSchema,
			OutputSchema: op.OutputSchema,
			Annotations:  toolAnnotations(op.Method, op.Title),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return catalog.callOperation(ctx, op, req)
		})
	}

	return server, nil
}

func toolAnnotations(method string, title string) *mcp.ToolAnnotations {
	readOnly := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	openWorld := true
	destructive := !readOnly
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    readOnly,
		IdempotentHint:  readOnly || method == http.MethodPut || method == http.MethodDelete,
		OpenWorldHint:   &openWorld,
		DestructiveHint: &destructive,
	}
}
