# goapitomcp

`goapitomcp` exposes OpenAPI 3.0 and 3.1 operations as runtime MCP tools over a standard Go `http.Handler`.

No code generation is required: pass an OpenAPI document reader to the library, mount the returned handler wherever your application already serves HTTP, and agents can discover/call the API through MCP.

```sh
go get github.com/alexliesenfeld/openapimcp
```

## Handler Usage

```go
specFile, err := os.Open("openapi.yaml")
if err != nil {
    log.Fatal(err)
}
defer specFile.Close()

apiBase, _ := url.Parse("https://api.example.com")
mcpHandler, err := goapitomcp.NewHandler(context.Background(), goapitomcp.Config{
    Spec:        specFile,
    SpecBaseURI: &url.URL{Scheme: "file", Path: "/absolute/path/openapi.yaml"},
    BaseURL:     apiBase,
    StaticHeaders: http.Header{
        "X-Service": []string{"my-app"},
    },
    ForwardHeaders: []string{"Authorization"},
})
if err != nil {
    log.Fatal(err)
}

mux := http.NewServeMux()
mux.Handle("/mcp", mcpHandler)
log.Fatal(http.ListenAndServe(":8080", mux))
```

If your spec is on disk, the file helpers set `SpecBaseURI` automatically so
relative `$ref`s resolve from the spec directory:

```go
apiBase, _ := url.Parse("https://api.example.com")
mcpHandler, err := goapitomcp.NewHandlerFromFile(ctx, "openapi.yaml", goapitomcp.Config{
    BaseURL: apiBase,
})
```

If your spec is embedded, the FS helpers resolve relative `$ref`s from the same
filesystem:

```go
//go:embed api/*
var apiFiles embed.FS

apiBase, _ := url.Parse("https://api.example.com")
mcpHandler, err := goapitomcp.NewHandlerFromFS(ctx, apiFiles, "api/openapi.yaml", goapitomcp.Config{
    BaseURL: apiBase,
})
```

## Filtering

Use `Config.Filter` to decide which OpenAPI operations are exposed as MCP tools.
Include-only rules run first; exclude rules run second.

```go
mcpHandler, err := goapitomcp.NewHandlerFromFile(ctx, "openapi.yaml", goapitomcp.Config{
    Filter: &goapitomcp.OperationFilter{
        ExcludeInternal:     true,
        ExcludePathPatterns: []string{"/admin/*", "/internal/*"},
        ExcludeMethods:      []string{"DELETE", "PATCH"},
        ExcludeTags:         []string{"debug", "internal"},
        IncludeOnlyMethods:  []string{"GET", "POST"},
    },
})
```

Path patterns support `*` within a path segment and OpenAPI-style `{param}`
segments.

## CORS

`NewHandler` returns the raw MCP handler. Applications can keep using their
existing middleware stack, or wrap it with the built-in helper:

```go
mux.Handle("/mcp", goapitomcp.WithCORS(mcpHandler, goapitomcp.CORSOptions{
    AllowedOrigins: []string{"https://agent.example.com"},
}))
```

## Features

- Dynamic OpenAPI-to-MCP conversion at startup.
- Standard `http.Handler` and `*mcp.Server` constructors, including file and `fs.FS` helpers.
- OpenAPI 3.0 and 3.1 support.
- Local multi-file `$ref` support with `SpecBaseURI`, `RefRoot`, and embedded `fs.FS` specs.
- Operation filtering by path, wildcard path pattern, operation ID, method, tag, and `x-internal`.
- Path, query, header, cookie, JSON body, and form body request mapping.
- Tool-name prefixing, response-derived output schemas, structured MCP results, and response field documentation for agents.
- Explicit forwarding of selected inbound MCP request headers to upstream API requests.
- OpenAPI security scheme metadata plus opt-in default credential application.
- Optional CORS middleware for embedded HTTP usage.
