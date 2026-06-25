package goapitomcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPHandlerListsAndCallsOpenAPITools(t *testing.T) {
	ctx := context.Background()
	var sawAuth bool
	var sawPathQuery bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/override/pets/abc" && r.URL.Query().Get("verbose") == "true" {
			sawPathQuery = true
		}
		if r.Header.Get("Authorization") == "Bearer test-token" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "abc",
			"verbose": r.URL.Query().Get("verbose"),
		})
	}))
	defer backend.Close()

	spec := `
openapi: 3.1.0
info:
  title: Runtime API
  version: 2.0.0
servers:
  - url: https://spec-server.example.invalid/base
paths:
  /pets/{id}:
    get:
      operationId: getPet
      summary: Get pet
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
        - name: verbose
          in: query
          schema:
            type: boolean
      responses:
        '200':
          description: ok
`
	baseURL := mustParseURL(t, backend.URL+"/override")
	handler, err := NewHandler(ctx, Config{
		Spec:    strings.NewReader(spec),
		BaseURL: baseURL,
		BeforeRequest: func(_ context.Context, op OperationContext, r *http.Request) error {
			if op.ToolName != "getPet" {
				t.Fatalf("OperationContext.ToolName = %q, want getPet", op.ToolName)
			}
			r.Header.Set("Authorization", "Bearer test-token")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	mcpServer := httptest.NewServer(handler)
	defer mcpServer.Close()

	session := connectMCP(t, ctx, mcpServer.URL)
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "getPet" {
		t.Fatalf("tools = %#v, want getPet", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "getPet",
		Arguments: map[string]any{
			"path": map[string]any{"id": "abc"},
			"query": map[string]any{
				"verbose": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() IsError = true, content = %#v", result.Content)
	}
	payload := decodePayload(t, result)
	if payload.Status != 200 {
		t.Fatalf("payload.Status = %d, want 200", payload.Status)
	}
	body := payload.Body.(map[string]any)
	if body["id"] != "abc" || body["verbose"] != "true" {
		t.Fatalf("payload.Body = %#v", payload.Body)
	}
	if !sawPathQuery {
		t.Fatalf("backend did not see BaseURL override path and serialized query")
	}
	if !sawAuth {
		t.Fatalf("backend did not see BeforeRequest auth header")
	}
}

func TestMCPHandlerForwardsConfiguredHeaders(t *testing.T) {
	ctx := context.Background()
	var sawAuth bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer forwarded-token" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer backend.Close()

	handler, err := NewHandler(ctx, Config{
		Spec:           strings.NewReader(simpleGetSpec()),
		BaseURL:        mustParseURL(t, backend.URL),
		ForwardHeaders: []string{"Authorization"},
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	mcpServer := httptest.NewServer(handler)
	defer mcpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: mcpServer.URL,
		HTTPClient: &http.Client{
			Transport: headerTransport{
				headers: http.Header{"Authorization": []string{"Bearer forwarded-token"}},
				base:    http.DefaultTransport,
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "getStatus"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() IsError = true")
	}
	if !sawAuth {
		t.Fatalf("backend did not see forwarded Authorization header")
	}
}

func TestForwardedHeadersPrecedeBeforeRequestAndIgnoreUnlistedHeaders(t *testing.T) {
	ctx := context.Background()
	var sawExpectedHeaders bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") == "hook" && r.Header.Get("X-Ignored") == "" {
			sawExpectedHeaders = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer backend.Close()

	handler, err := NewHandler(ctx, Config{
		Spec:           strings.NewReader(simpleGetSpec()),
		BaseURL:        mustParseURL(t, backend.URL),
		StaticHeaders:  http.Header{"X-Test": []string{"static"}},
		ForwardHeaders: []string{"X-Test"},
		BeforeRequest: func(_ context.Context, _ OperationContext, r *http.Request) error {
			if got := r.Header.Get("X-Test"); got != "forwarded" {
				t.Fatalf("BeforeRequest saw X-Test = %q, want forwarded", got)
			}
			if got := r.Header.Get("X-Ignored"); got != "" {
				t.Fatalf("BeforeRequest saw unlisted X-Ignored = %q, want empty", got)
			}
			r.Header.Set("X-Test", "hook")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	mcpServer := httptest.NewServer(handler)
	defer mcpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: mcpServer.URL,
		HTTPClient: &http.Client{
			Transport: headerTransport{
				headers: http.Header{
					"X-Test":    []string{"forwarded"},
					"X-Ignored": []string{"ignored"},
				},
				base: http.DefaultTransport,
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "getStatus"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() IsError = true")
	}
	if !sawExpectedHeaders {
		t.Fatalf("backend did not see BeforeRequest override and ignored-header filtering")
	}
}

func TestCallOperationPostsJSONBody(t *testing.T) {
	var gotBody map[string]any
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pets" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer backend.Close()

	spec := `
openapi: 3.0.3
info:
  title: Runtime API
  version: 1.0.0
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
      responses:
        '201':
          description: created
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(spec),
		BaseURL: mustParseURL(t, backend.URL),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err := catalog.callOperation(context.Background(), findOperation(t, catalog, "POST", "/pets"), callRequest(map[string]any{
		"body": map[string]any{"name": "Milo"},
	}))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	if result.IsError {
		t.Fatalf("callOperation() IsError = true")
	}
	if gotBody["name"] != "Milo" {
		t.Fatalf("gotBody = %#v", gotBody)
	}
	if got := decodePayload(t, result).Status; got != http.StatusCreated {
		t.Fatalf("status = %d, want 201", got)
	}
}

func TestRequestBodyEdgeCases(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Body API
  version: 1.0.0
paths:
  /required:
    post:
      operationId: requiredBody
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
      responses:
        '200':
          description: ok
  /optional:
    post:
      operationId: optionalBody
      requestBody:
        required: false
        content:
          application/json:
            schema:
              type: object
      responses:
        '200':
          description: ok
  /vendor:
    post:
      operationId: vendorJSON
      requestBody:
        required: true
        content:
          application/vnd.example+json:
            schema:
              type: object
      responses:
        '200':
          description: ok
  /form:
    post:
      operationId: formBody
      requestBody:
        required: true
        content:
          application/x-www-form-urlencoded:
            schema:
              type: object
      responses:
        '200':
          description: ok
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(spec),
		BaseURL: mustParseURL(t, "https://api.example.test"),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	result, err := catalog.callOperation(context.Background(), findOperation(t, catalog, "POST", "/required"), callRequest(nil))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, "missing required request body")

	optionalReq, err := catalog.buildHTTPRequest(context.Background(), findOperation(t, catalog, "POST", "/optional"), nil)
	if err != nil {
		t.Fatalf("optional buildHTTPRequest() error = %v", err)
	}
	if optionalReq.Body != nil {
		body, _ := io.ReadAll(optionalReq.Body)
		t.Fatalf("optional request body = %q, want no body", string(body))
	}
	if got := optionalReq.Header.Get("Content-Type"); got != "" {
		t.Fatalf("optional Content-Type = %q, want empty", got)
	}

	vendorReq, err := catalog.buildHTTPRequest(context.Background(), findOperation(t, catalog, "POST", "/vendor"), map[string]any{
		"body": map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatalf("vendor buildHTTPRequest() error = %v", err)
	}
	if got := vendorReq.Header.Get("Content-Type"); got != "application/vnd.example+json" {
		t.Fatalf("vendor Content-Type = %q, want application/vnd.example+json", got)
	}

	result, err = catalog.callOperation(context.Background(), findOperation(t, catalog, "POST", "/form"), callRequest(map[string]any{
		"body": "not-an-object",
	}))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, "form request body must be an object")
}

func TestCallOperationAppliesStaticHeadersSecurityDefaultsAndFormBody(t *testing.T) {
	var gotForm url.Values
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Static") != "configured" {
			t.Fatalf("X-Static = %q, want configured", r.Header.Get("X-Static"))
		}
		if r.Header.Get("X-API-Key") != "secret-key" {
			t.Fatalf("X-API-Key = %q, want secret-key", r.Header.Get("X-API-Key"))
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Fatalf("Content-Type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer backend.Close()

	spec := `
openapi: 3.0.3
info:
  title: Form API
  version: 1.0.0
components:
  securitySchemes:
    ApiKeyAuth:
      type: apiKey
      in: header
      name: X-API-Key
      x-defaultCredential: secret-key
security:
  - ApiKeyAuth: []
paths:
  /tokens:
    post:
      operationId: createToken
      requestBody:
        required: true
        content:
          application/x-www-form-urlencoded:
            schema:
              type: object
              required: [username]
              properties:
                username:
                  type: string
                scopes:
                  type: array
                  items:
                    type: string
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  ok:
                    type: boolean
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:                strings.NewReader(spec),
		BaseURL:             mustParseURL(t, backend.URL),
		StaticHeaders:       http.Header{"X-Static": []string{"configured"}},
		UseSecurityDefaults: true,
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err := catalog.callOperation(context.Background(), catalog.Operations[0], callRequest(map[string]any{
		"body": map[string]any{
			"username": "alex",
			"scopes":   []any{"read", "write"},
		},
	}))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	if result.IsError {
		t.Fatalf("callOperation() IsError = true")
	}
	if gotForm.Get("username") != "alex" {
		t.Fatalf("username form value = %q", gotForm.Get("username"))
	}
	if values := gotForm["scopes"]; len(values) != 2 || values[0] != "read" || values[1] != "write" {
		t.Fatalf("scopes form values = %#v", values)
	}
}

func TestBuildHTTPRequestSerializesParameterStyles(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Serialization API
  version: 1.0.0
paths:
  /items/{id}:
    get:
      operationId: getItem
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
        - name: tags
          in: query
          style: form
          explode: true
          schema:
            type: array
            items:
              type: string
        - name: ids
          in: query
          style: form
          explode: false
          schema:
            type: array
            items:
              type: integer
        - name: colors
          in: query
          style: spaceDelimited
          schema:
            type: array
            items:
              type: string
        - name: flags
          in: query
          style: pipeDelimited
          schema:
            type: array
            items:
              type: string
        - name: filter
          in: query
          style: form
          explode: true
          schema:
            type: object
            properties:
              active:
                type: boolean
              role:
                type: string
        - name: packed
          in: query
          style: form
          explode: false
          schema:
            type: object
            properties:
              a:
                type: integer
              b:
                type: integer
        - name: X-Trace
          in: header
          schema:
            type: array
            items:
              type: string
        - name: session
          in: cookie
          schema:
            type: string
      responses:
        '200':
          description: ok
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(spec),
		BaseURL: mustParseURL(t, "https://api.example.test/base?tenant=acme"),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	req, err := catalog.buildHTTPRequest(context.Background(), catalog.Operations[0], map[string]any{
		"path": map[string]any{"id": "a/b c?"},
		"query": map[string]any{
			"tags":   []any{"red", "blue"},
			"ids":    []any{float64(1), float64(2)},
			"colors": []any{"red", "blue"},
			"flags":  []any{"hot", "cold"},
			"filter": map[string]any{"active": true, "role": "admin"},
			"packed": map[string]any{"b": float64(2), "a": float64(1)},
		},
		"header": map[string]any{"X-Trace": []any{"one", "two"}},
		"cookie": map[string]any{"session": "abc"},
	})
	if err != nil {
		t.Fatalf("buildHTTPRequest() error = %v", err)
	}

	if got := req.URL.EscapedPath(); got != "/base/items/a%2Fb%20c%3F" {
		t.Fatalf("EscapedPath = %q, want escaped path parameter", got)
	}
	query := req.URL.Query()
	if got := query["tags"]; len(got) != 2 || got[0] != "red" || got[1] != "blue" {
		t.Fatalf("tags query = %#v, want repeated red/blue", got)
	}
	assertQueryValue(t, query, "tenant", "acme")
	assertQueryValue(t, query, "ids", "1,2")
	assertQueryValue(t, query, "colors", "red blue")
	assertQueryValue(t, query, "flags", "hot|cold")
	assertQueryValue(t, query, "active", "true")
	assertQueryValue(t, query, "role", "admin")
	assertQueryValue(t, query, "packed", "a,1,b,2")
	if got := req.Header.Get("X-Trace"); got != "one,two" {
		t.Fatalf("X-Trace = %q, want one,two", got)
	}
	cookie, err := req.Cookie("session")
	if err != nil {
		t.Fatalf("session cookie missing: %v", err)
	}
	if cookie.Value != "abc" {
		t.Fatalf("session cookie = %q, want abc", cookie.Value)
	}
}

func TestBuildHTTPRequestAppliesSecurityDefaultsWithoutOverwritingExplicitValues(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Security API
  version: 1.0.0
components:
  securitySchemes:
    HeaderKey:
      type: apiKey
      in: header
      name: X-API-Key
      x-defaultCredential: header-secret
    QueryKey:
      type: apiKey
      in: query
      name: api_key
      x-defaultCredential: query-secret
    CookieKey:
      type: apiKey
      in: cookie
      name: sid
      x-defaultCredential: cookie-secret
    BasicAuth:
      type: http
      scheme: basic
      x-defaultCredential: user:pass
    BearerAuth:
      type: http
      scheme: bearer
      x-defaultCredential: bearer-secret
security:
  - HeaderKey: []
  - QueryKey: []
  - CookieKey: []
paths:
  /protected:
    get:
      operationId: protected
      responses:
        '200':
          description: ok
  /basic:
    get:
      operationId: basic
      security:
        - BasicAuth: []
      responses:
        '200':
          description: ok
  /bearer:
    get:
      operationId: bearer
      security:
        - BearerAuth: []
      responses:
        '200':
          description: ok
  /public:
    get:
      operationId: public
      security: []
      responses:
        '200':
          description: ok
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:                strings.NewReader(spec),
		BaseURL:             mustParseURL(t, "https://api.example.test?api_key=provided-query"),
		StaticHeaders:       http.Header{"X-API-Key": []string{"provided-header"}, "Cookie": []string{"sid=provided-cookie"}},
		UseSecurityDefaults: true,
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	protectedReq, err := catalog.buildHTTPRequest(context.Background(), findOperation(t, catalog, "GET", "/protected"), nil)
	if err != nil {
		t.Fatalf("build protected request: %v", err)
	}
	if got := protectedReq.Header.Get("X-API-Key"); got != "provided-header" {
		t.Fatalf("X-API-Key = %q, want provided-header", got)
	}
	if got := protectedReq.URL.Query().Get("api_key"); got != "provided-query" {
		t.Fatalf("api_key = %q, want provided-query", got)
	}
	cookie, err := protectedReq.Cookie("sid")
	if err != nil {
		t.Fatalf("sid cookie missing: %v", err)
	}
	if cookie.Value != "provided-cookie" {
		t.Fatalf("sid cookie = %q, want provided-cookie", cookie.Value)
	}

	basicReq, err := catalog.buildHTTPRequest(context.Background(), findOperation(t, catalog, "GET", "/basic"), nil)
	if err != nil {
		t.Fatalf("build basic request: %v", err)
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if got := basicReq.Header.Get("Authorization"); got != wantBasic {
		t.Fatalf("basic Authorization = %q, want %q", got, wantBasic)
	}

	bearerReq, err := catalog.buildHTTPRequest(context.Background(), findOperation(t, catalog, "GET", "/bearer"), nil)
	if err != nil {
		t.Fatalf("build bearer request: %v", err)
	}
	if got := bearerReq.Header.Get("Authorization"); got != "Bearer bearer-secret" {
		t.Fatalf("bearer Authorization = %q, want bearer token", got)
	}

	cleanCatalog, err := LoadCatalog(context.Background(), Config{
		Spec:                strings.NewReader(spec),
		BaseURL:             mustParseURL(t, "https://api.example.test"),
		UseSecurityDefaults: true,
	})
	if err != nil {
		t.Fatalf("LoadCatalog() clean security error = %v", err)
	}
	publicReq, err := cleanCatalog.buildHTTPRequest(context.Background(), findOperation(t, cleanCatalog, "GET", "/public"), nil)
	if err != nil {
		t.Fatalf("build public request: %v", err)
	}
	if got := publicReq.Header.Get("Authorization"); got != "" {
		t.Fatalf("public Authorization = %q, want empty", got)
	}
	if got := publicReq.Header.Get("X-API-Key"); got != "" {
		t.Fatalf("public X-API-Key = %q, want empty", got)
	}
	if got := publicReq.URL.Query().Get("api_key"); got != "" {
		t.Fatalf("public api_key = %q, want empty", got)
	}
	if _, err := publicReq.Cookie("sid"); err == nil {
		t.Fatalf("public sid cookie unexpectedly set")
	}
}

func TestCallOperationReportsArgumentAndConfigurationErrors(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Required API
  version: 1.0.0
paths:
  /status:
    get:
      operationId: getStatus
      parameters:
        - name: q
          in: query
          required: true
          schema:
            type: string
      responses:
        '200':
          description: ok
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(spec),
		BaseURL: mustParseURL(t, "https://api.example.test"),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	op := catalog.Operations[0]

	result, err := catalog.callOperation(context.Background(), op, rawCallRequest(`{"query":`))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, "decode arguments")

	result, err = catalog.callOperation(context.Background(), op, callRequest(map[string]any{"query": "bad"}))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, `argument group "query" must be an object`)

	result, err = catalog.callOperation(context.Background(), op, callRequest(nil))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, `missing required query parameter "q"`)

	noBaseCatalog, err := LoadCatalog(context.Background(), Config{Spec: strings.NewReader(simpleGetSpec())})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err = noBaseCatalog.callOperation(context.Background(), noBaseCatalog.Operations[0], callRequest(nil))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, "no BaseURL configured")
}

func TestCallOperationReportsTransportAndOversizedResponseErrors(t *testing.T) {
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(simpleGetSpec()),
		BaseURL: mustParseURL(t, "https://api.example.test"),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network boom")
		})},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err := catalog.callOperation(context.Background(), catalog.Operations[0], callRequest(nil))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, "network boom")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer backend.Close()

	catalog, err = LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(simpleGetSpec()),
		BaseURL: mustParseURL(t, backend.URL),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err = catalog.callOperation(context.Background(), catalog.Operations[0], callRequest(nil))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	assertToolErrorContains(t, result, "response body exceeds 10 MiB limit")
}

func TestCallOperationMarksNon2xxAsToolError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer backend.Close()

	spec := simpleGetSpec()
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(spec),
		BaseURL: mustParseURL(t, backend.URL),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err := catalog.callOperation(context.Background(), catalog.Operations[0], callRequest(nil))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if got := decodePayload(t, result).Status; got != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", got)
	}
}

func TestHandlerCanBeMountedOnExistingMux(t *testing.T) {
	ctx := context.Background()
	handler, err := NewHandler(ctx, Config{
		Spec:    strings.NewReader(simpleGetSpec()),
		BaseURL: mustParseURL(t, "https://api.example.test"),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	session := connectMCP(t, ctx, server.URL+"/mcp")
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "getStatus" {
		t.Fatalf("tools = %#v, want getStatus", tools.Tools)
	}
}

func TestNewHandlerFromFileCreatesMountableMCPHandler(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "openapi.yaml")
	writeFile(t, path, simpleGetSpec())

	handler, err := NewHandlerFromFile(ctx, path, Config{
		BaseURL: mustParseURL(t, "https://api.example.test"),
	})
	if err != nil {
		t.Fatalf("NewHandlerFromFile() error = %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	session := connectMCP(t, ctx, server.URL)
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "getStatus" {
		t.Fatalf("tools = %#v, want getStatus", tools.Tools)
	}
}

func TestWithCORSHandlesPreflightAndPassesRequests(t *testing.T) {
	var served bool
	handler := WithCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusAccepted)
	}), CORSOptions{
		AllowedOrigins: []string{"https://app.example.test"},
	})

	preflight := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	preflight.Header.Set("Origin", "https://app.example.test")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflightRecorder := httptest.NewRecorder()
	handler.ServeHTTP(preflightRecorder, preflight)

	if preflightRecorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", preflightRecorder.Code)
	}
	if served {
		t.Fatalf("preflight reached wrapped handler")
	}
	if got := preflightRecorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.test" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := preflightRecorder.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Fatalf("Access-Control-Allow-Methods = %q, want POST", got)
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://app.example.test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("request status = %d, want 202", recorder.Code)
	}
	if !served {
		t.Fatalf("request did not reach wrapped handler")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.test" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestWithCORSEdgeOptions(t *testing.T) {
	handler := WithCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Result", "ok")
		w.WriteHeader(http.StatusAccepted)
	}), CORSOptions{
		AllowedOrigins:   []string{"https://allowed.example.test"},
		AllowedMethods:   []string{http.MethodPost},
		AllowedHeaders:   []string{"Content-Type", "X-Agent"},
		ExposedHeaders:   []string{"X-Result"},
		AllowCredentials: true,
		MaxAge:           10 * time.Minute,
	})

	denied := httptest.NewRecorder()
	deniedReq := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	deniedReq.Header.Set("Origin", "https://evil.example.test")
	handler.ServeHTTP(denied, deniedReq)
	if got := denied.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("denied Access-Control-Allow-Origin = %q, want empty", got)
	}

	allowed := httptest.NewRecorder()
	allowedReq := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	allowedReq.Header.Set("Origin", "https://allowed.example.test")
	handler.ServeHTTP(allowed, allowedReq)
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "https://allowed.example.test" {
		t.Fatalf("allowed origin = %q", got)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("Allow-Credentials = %q, want true", got)
	}
	if got := allowed.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("Max-Age = %q, want 600", got)
	}
	if got := allowed.Header().Get("Access-Control-Expose-Headers"); got != "X-Result" {
		t.Fatalf("Expose-Headers = %q, want X-Result", got)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Methods"); got != http.MethodPost {
		t.Fatalf("Allow-Methods = %q, want POST", got)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Agent" {
		t.Fatalf("Allow-Headers = %q", got)
	}

	wildcardCredentialHandler := WithCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), CORSOptions{AllowCredentials: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	request.Header.Set("Origin", "https://app.example.test")
	wildcardCredentialHandler.ServeHTTP(recorder, request)
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.test" {
		t.Fatalf("wildcard credentials origin = %q, want request origin", got)
	}
	if got := recorder.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

func TestCallOperationRejectsUnsupportedBodyMediaType(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Runtime API
  version: 1.0.0
paths:
  /messages:
    post:
      operationId: createMessage
      requestBody:
        required: true
        content:
          text/plain:
            schema:
              type: string
      responses:
        '201':
          description: created
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:    strings.NewReader(spec),
		BaseURL: mustParseURL(t, "https://api.example.test"),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	result, err := catalog.callOperation(context.Background(), catalog.Operations[0], callRequest(map[string]any{
		"body": "hello",
	}))
	if err != nil {
		t.Fatalf("callOperation() protocol error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}
	payload := decodePayload(t, result)
	if !strings.Contains(payload.Body.(string), "supported JSON or form request body") {
		t.Fatalf("payload.Body = %#v", payload.Body)
	}
}

func TestToolAnnotationsByHTTPMethod(t *testing.T) {
	tests := []struct {
		method      string
		readOnly    bool
		idempotent  bool
		destructive bool
	}{
		{method: http.MethodGet, readOnly: true, idempotent: true, destructive: false},
		{method: http.MethodHead, readOnly: true, idempotent: true, destructive: false},
		{method: http.MethodOptions, readOnly: true, idempotent: true, destructive: false},
		{method: http.MethodPut, readOnly: false, idempotent: true, destructive: true},
		{method: http.MethodDelete, readOnly: false, idempotent: true, destructive: true},
		{method: http.MethodPost, readOnly: false, idempotent: false, destructive: true},
		{method: http.MethodPatch, readOnly: false, idempotent: false, destructive: true},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			annotations := toolAnnotations(tt.method, "title")
			if annotations.Title != "title" {
				t.Fatalf("Title = %q, want title", annotations.Title)
			}
			if annotations.ReadOnlyHint != tt.readOnly {
				t.Fatalf("ReadOnlyHint = %v, want %v", annotations.ReadOnlyHint, tt.readOnly)
			}
			if annotations.IdempotentHint != tt.idempotent {
				t.Fatalf("IdempotentHint = %v, want %v", annotations.IdempotentHint, tt.idempotent)
			}
			if annotations.DestructiveHint == nil || *annotations.DestructiveHint != tt.destructive {
				t.Fatalf("DestructiveHint = %v, want %v", annotations.DestructiveHint, tt.destructive)
			}
			if annotations.OpenWorldHint == nil || !*annotations.OpenWorldHint {
				t.Fatalf("OpenWorldHint = %v, want true", annotations.OpenWorldHint)
			}
		})
	}
}

func TestParseResponseBodyEdgeCases(t *testing.T) {
	if got := parseResponseBody("application/json", nil); got != nil {
		t.Fatalf("empty JSON body = %#v, want nil", got)
	}
	if got := parseResponseBody("application/json", []byte(`{"ok":`)); got != `{"ok":` {
		t.Fatalf("invalid JSON body = %#v, want original string", got)
	}
	body := parseResponseBody("application/vnd.example+json; charset=utf-8", []byte(`{"ok":true}`))
	object, ok := body.(map[string]any)
	if !ok || object["ok"] != true {
		t.Fatalf("vendor JSON body = %#v, want parsed object", body)
	}
	if got := parseResponseBody("text/plain", []byte("hello")); got != "hello" {
		t.Fatalf("text body = %#v, want hello", got)
	}
}

func connectMCP(t *testing.T, ctx context.Context, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return session
}

func callRequest(args map[string]any) *mcp.CallToolRequest {
	var raw json.RawMessage
	if args != nil {
		raw, _ = json.Marshal(args)
	}
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: raw},
	}
}

func rawCallRequest(raw string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(raw)},
	}
}

func assertQueryValue(t *testing.T, values url.Values, name string, want string) {
	t.Helper()
	if got := values.Get(name); got != want {
		t.Fatalf("query %s = %q, want %q; all query = %s", name, got, want, values.Encode())
	}
}

func assertToolErrorContains(t *testing.T, result *mcp.CallToolResult, want string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}
	payload := decodePayload(t, result)
	message, ok := payload.Body.(string)
	if !ok {
		t.Fatalf("payload.Body = %#v, want string containing %q", payload.Body, want)
	}
	if !strings.Contains(message, want) {
		t.Fatalf("payload.Body = %q, want substring %q", message, want)
	}
}

func decodePayload(t *testing.T, result *mcp.CallToolResult) toolPayload {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var payload toolPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal StructuredContent: %v", err)
	}
	return payload
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type headerTransport struct {
	headers http.Header
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	for name, values := range t.headers {
		cloned.Header.Del(name)
		for _, value := range values {
			cloned.Header.Add(name, value)
		}
	}

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func simpleGetSpec() string {
	return `
openapi: 3.0.3
info:
  title: Runtime API
  version: 1.0.0
paths:
  /status:
    get:
      operationId: getStatus
      responses:
        '200':
          description: ok
`
}
