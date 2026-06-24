package goapitomcp

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config controls how an OpenAPI document is exposed as an MCP server.
type Config struct {
	Spec                io.Reader
	SpecBaseURI         *url.URL
	RefRoot             string
	RefFS               fs.FS
	Name                string
	Version             string
	ToolNamePrefix      string
	Filter              *OperationFilter
	BaseURL             *url.URL
	HTTPClient          *http.Client
	StaticHeaders       http.Header
	UseSecurityDefaults bool
	SkipValidation      bool
	BeforeRequest       func(context.Context, OperationContext, *http.Request) error
	Logger              *slog.Logger
}

// OperationFilter controls which OpenAPI operations become MCP tools.
//
// Include-only fields are evaluated first. When an include-only field is set,
// an operation must match it to remain visible. Exclude fields are evaluated
// afterward and can still remove matching operations.
type OperationFilter struct {
	ExcludeInternal     bool
	ExcludePaths        []string
	ExcludePathPatterns []string
	ExcludeOperationIDs []string
	ExcludeMethods      []string
	ExcludeTags         []string

	IncludeOnlyPaths        []string
	IncludeOnlyPathPatterns []string
	IncludeOnlyOperationIDs []string
	IncludeOnlyMethods      []string
	IncludeOnlyTags         []string
}

// OperationContext describes the OpenAPI operation behind an MCP tool call.
type OperationContext struct {
	ToolName    string
	OperationID string
	Method      string
	Path        string
}

// Catalog is the parsed runtime representation used to build MCP tools.
type Catalog struct {
	Name            string
	Version         string
	BaseURL         *url.URL
	OpenAPI         string
	Operations      []*Operation
	SecuritySchemes []SecurityScheme
	specServers     []string
	cfg             Config
}

// Operation is one OpenAPI operation exposed as one MCP tool.
type Operation struct {
	ToolName              string
	Title                 string
	Description           string
	Method                string
	Path                  string
	OperationID           string
	InputSchema           map[string]any
	OutputSchema          map[string]any
	ResponseDocumentation string
	Parameters            []Parameter
	RequestBody           *RequestBody
	Security              []SecurityRequirement
}

// Parameter describes one OpenAPI operation parameter after path-level and
// operation-level parameters have been merged.
type Parameter struct {
	Name        string
	In          string
	Description string
	Required    bool
	Style       string
	Explode     bool
	Schema      map[string]any
}

// RequestBody describes the JSON request body shape selected for a tool.
type RequestBody struct {
	Required    bool
	MediaType   string
	Description string
	Schema      map[string]any
}

// SecurityScheme describes an OpenAPI security scheme discovered in the spec.
type SecurityScheme struct {
	ID                string
	Type              string
	Scheme            string
	In                string
	Name              string
	Description       string
	BearerFormat      string
	OpenIDConnectURL  string
	DefaultCredential string
}

// SecurityRequirement describes one operation security requirement.
type SecurityRequirement struct {
	ID     string
	Scopes []string
}

// NewHandler creates a standard net/http handler that serves a Streamable HTTP
// MCP endpoint.
func NewHandler(ctx context.Context, cfg Config) (http.Handler, error) {
	server, err := NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       cfg.Logger,
	}), nil
}
