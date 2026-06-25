package goapitomcp

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

const maxToolNameLength = 128

// LoadCatalog parses the configured OpenAPI document and builds the dynamic
// operation catalog used by the MCP server.
func LoadCatalog(ctx context.Context, cfg Config) (*Catalog, error) {
	if cfg.Spec == nil {
		return nil, errors.New("Spec is required")
	}
	data, err := io.ReadAll(cfg.Spec)
	if err != nil {
		return nil, fmt.Errorf("read OpenAPI spec: %w", err)
	}

	loader := openapi3.NewLoader()
	loader.Context = ctx

	if cfg.RefFS != nil {
		loader.IsExternalRefsAllowed = true
		loader.ReadFromURIFunc = fsRefReader(cfg.RefFS, fsRefRoot(cfg.SpecBaseURI))
	} else if cfg.SpecBaseURI != nil {
		refRoot, err := resolveRefRoot(cfg)
		if err != nil {
			return nil, err
		}
		loader.IsExternalRefsAllowed = true
		loader.ReadFromURIFunc = guardedRefReader(refRoot)
	}

	location := cfg.SpecBaseURI
	if location == nil {
		location = &url.URL{Path: "openapi.yaml"}
	}
	doc, err := loader.LoadFromDataWithPath(data, location)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec: %w", err)
	}
	if !supportedOpenAPIVersion(doc.OpenAPI) {
		return nil, fmt.Errorf("unsupported OpenAPI version %q: only 3.0.x and 3.1.x are supported", doc.OpenAPI)
	}
	if !cfg.SkipValidation {
		if err := doc.Validate(loader.Context); err != nil {
			return nil, fmt.Errorf("validate OpenAPI spec: %w", err)
		}
	}

	name := cfg.Name
	if name == "" {
		name = "goapitomcp"
		if doc.Info != nil && doc.Info.Title != "" {
			name = sanitizeToolName(doc.Info.Title)
		}
	}
	version := cfg.Version
	if version == "" {
		version = "0.1.0"
		if doc.Info != nil && doc.Info.Version != "" {
			version = doc.Info.Version
		}
	}

	catalog := &Catalog{
		Name:            name,
		Version:         version,
		OpenAPI:         doc.OpenAPI,
		SecuritySchemes: collectSecuritySchemes(doc),
		cfg:             cfg,
	}
	for _, server := range doc.Servers {
		if server != nil {
			catalog.specServers = append(catalog.specServers, server.URL)
		}
	}
	if cfg.BaseURL != nil {
		catalog.BaseURL = cloneURL(cfg.BaseURL)
	} else {
		catalog.BaseURL = firstAbsoluteServerURL(doc.Servers)
	}

	if doc.Paths != nil {
		usedNames := make(map[string]int)
		for _, pathName := range sortedPathKeys(doc.Paths) {
			pathItem := doc.Paths.Value(pathName)
			if pathItem == nil {
				continue
			}
			for _, method := range sortedMethods(pathItem.Operations()) {
				operation := pathItem.GetOperation(method)
				if operation == nil {
					continue
				}
				if !operationFilterAllows(cfg.Filter, method, pathName, operation) {
					continue
				}
				op, err := buildOperation(pathName, method, pathItem, operation, doc.Security, cfg.ToolNamePrefix, usedNames)
				if err != nil {
					return nil, err
				}
				catalog.Operations = append(catalog.Operations, op)
			}
		}
	}

	return catalog, nil
}

func supportedOpenAPIVersion(version string) bool {
	return version == "3" ||
		version == "3.0" || strings.HasPrefix(version, "3.0.") ||
		version == "3.1" || strings.HasPrefix(version, "3.1.")
}

func operationFilterAllows(filter *OperationFilter, method string, pathName string, operation *openapi3.Operation) bool {
	if operation == nil {
		return false
	}
	if filter != nil && filter.ExcludeInternal && isTrueExtension(operation.Extensions["x-internal"]) {
		return false
	}
	return filter.Allows(method, pathName, operation.OperationID, operation.Tags)
}

func isTrueExtension(value any) bool {
	result, ok := value.(bool)
	return ok && result
}

func resolveRefRoot(cfg Config) (string, error) {
	if cfg.RefRoot != "" {
		return cleanAbs(cfg.RefRoot)
	}
	if cfg.SpecBaseURI == nil {
		return "", errors.New("SpecBaseURI is required when RefRoot is not set")
	}
	if cfg.SpecBaseURI.Scheme != "" && cfg.SpecBaseURI.Scheme != "file" {
		return "", fmt.Errorf("SpecBaseURI scheme %q cannot be used for local refs", cfg.SpecBaseURI.Scheme)
	}
	if cfg.SpecBaseURI.Host != "" {
		return "", fmt.Errorf("SpecBaseURI host %q is not supported for local refs", cfg.SpecBaseURI.Host)
	}
	basePath := filepath.FromSlash(cfg.SpecBaseURI.Path)
	if basePath == "" {
		return "", errors.New("SpecBaseURI must include a local file path")
	}
	return cleanAbs(filepath.Dir(basePath))
}

func guardedRefReader(refRoot string) openapi3.ReadFromURIFunc {
	return func(_ *openapi3.Loader, uri *url.URL) ([]byte, error) {
		if uri.Scheme != "" && uri.Scheme != "file" {
			return nil, fmt.Errorf("remote OpenAPI refs are not supported: %s", uri.String())
		}
		if uri.Host != "" {
			return nil, fmt.Errorf("OpenAPI refs with hosts are not supported: %s", uri.String())
		}
		candidate, err := cleanAbs(filepath.FromSlash(uri.Path))
		if err != nil {
			return nil, err
		}
		if !isWithinRoot(refRoot, candidate) {
			return nil, fmt.Errorf("OpenAPI ref %q escapes RefRoot %q", candidate, refRoot)
		}
		return os.ReadFile(candidate)
	}
}

func fsRefRoot(specBaseURI *url.URL) string {
	if specBaseURI == nil || specBaseURI.Path == "" {
		return "."
	}
	root := path.Dir(path.Clean(specBaseURI.Path))
	return strings.TrimPrefix(root, "/")
}

func fsRefReader(files fs.FS, refRoot string) openapi3.ReadFromURIFunc {
	return func(_ *openapi3.Loader, uri *url.URL) ([]byte, error) {
		if uri.Scheme != "" && uri.Scheme != "file" {
			return nil, fmt.Errorf("remote OpenAPI refs are not supported: %s", uri.String())
		}
		if uri.Host != "" {
			return nil, fmt.Errorf("OpenAPI refs with hosts are not supported: %s", uri.String())
		}
		name := strings.TrimPrefix(path.Clean(uri.Path), "/")
		if name == "." || name == "" {
			return nil, errors.New("empty OpenAPI ref path")
		}
		if refRoot != "." && !isWithinFSRoot(refRoot, name) {
			return nil, fmt.Errorf("OpenAPI ref %q escapes RefFS root %q", name, refRoot)
		}
		return fs.ReadFile(files, name)
	}
}

func isWithinFSRoot(root string, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func cleanAbs(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = abs
	}
	return filepath.Clean(path), nil
}

func isWithinRoot(root string, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func sortedPathKeys(paths *openapi3.Paths) []string {
	keys := make([]string, 0, paths.Len())
	for key := range paths.Map() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func sortedMethods(operations map[string]*openapi3.Operation) []string {
	methods := make([]string, 0, len(operations))
	for method := range operations {
		methods = append(methods, method)
	}
	slices.Sort(methods)
	return methods
}

func sortedResponseKeys(responses *openapi3.Responses) []string {
	keys := make([]string, 0, responses.Len())
	for key := range responses.Map() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func buildOperation(pathName string, method string, pathItem *openapi3.PathItem, operation *openapi3.Operation, rootSecurity openapi3.SecurityRequirements, toolNamePrefix string, usedNames map[string]int) (*Operation, error) {
	params := mergeParameters(pathItem.Parameters, operation.Parameters)
	requestBody := buildRequestBody(operation.RequestBody)
	security := effectiveSecurity(operation, rootSecurity)
	responseDocumentation := buildResponseDocumentation(operation)
	toolName := uniqueToolName(operation.OperationID, method, pathName, toolNamePrefix, usedNames)

	op := &Operation{
		ToolName:              toolName,
		Title:                 operation.Summary,
		Description:           buildDescription(method, pathName, operation, security, requestBody, responseDocumentation),
		Method:                strings.ToUpper(method),
		Path:                  pathName,
		OperationID:           operation.OperationID,
		Parameters:            params,
		RequestBody:           requestBody,
		OutputSchema:          buildOutputSchema(operation),
		ResponseDocumentation: responseDocumentation,
		Security:              security,
	}
	op.InputSchema = buildInputSchema(params, requestBody)
	return op, nil
}

func mergeParameters(pathParams openapi3.Parameters, operationParams openapi3.Parameters) []Parameter {
	type keyed struct {
		in   string
		name string
	}
	order := make([]keyed, 0, len(pathParams)+len(operationParams))
	merged := make(map[keyed]Parameter)

	add := func(ref *openapi3.ParameterRef) {
		if ref == nil || ref.Value == nil {
			return
		}
		p := ref.Value
		key := keyed{in: p.In, name: p.Name}
		if _, ok := merged[key]; !ok {
			order = append(order, key)
		}
		merged[key] = Parameter{
			Name:        p.Name,
			In:          p.In,
			Description: p.Description,
			Required:    p.Required || p.In == openapi3.ParameterInPath,
			Style:       defaultStyle(p),
			Explode:     defaultExplode(p),
			Schema:      schemaRefToMap(p.Schema),
		}
	}
	for _, ref := range pathParams {
		add(ref)
	}
	for _, ref := range operationParams {
		add(ref)
	}

	params := make([]Parameter, 0, len(order))
	for _, key := range order {
		params = append(params, merged[key])
	}
	return params
}

func defaultStyle(p *openapi3.Parameter) string {
	if p.Style != "" {
		return p.Style
	}
	switch p.In {
	case openapi3.ParameterInPath:
		return "simple"
	case openapi3.ParameterInQuery, openapi3.ParameterInCookie:
		return "form"
	case openapi3.ParameterInHeader:
		return "simple"
	default:
		return "form"
	}
}

func defaultExplode(p *openapi3.Parameter) bool {
	if p.Explode != nil {
		return *p.Explode
	}
	return defaultStyle(p) == "form"
}

func buildRequestBody(ref *openapi3.RequestBodyRef) *RequestBody {
	if ref == nil || ref.Value == nil {
		return nil
	}
	body := ref.Value
	mediaType, media := selectRequestMediaType(body.Content)
	rb := &RequestBody{
		Required:    body.Required,
		MediaType:   mediaType,
		Description: body.Description,
	}
	if media != nil {
		rb.Schema = schemaRefToMap(media.Schema)
	}
	return rb
}

func selectJSONMediaType(content openapi3.Content) (string, *openapi3.MediaType) {
	if content == nil {
		return "", nil
	}
	if media := content.Get("application/json"); media != nil {
		return "application/json", media
	}
	keys := make([]string, 0, len(content))
	for mediaType := range content {
		keys = append(keys, mediaType)
	}
	slices.Sort(keys)
	for _, mediaType := range keys {
		if strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json") {
			return mediaType, content[mediaType]
		}
	}
	return "", nil
}

func selectRequestMediaType(content openapi3.Content) (string, *openapi3.MediaType) {
	mediaType, media := selectJSONMediaType(content)
	if media != nil {
		return mediaType, media
	}
	if content == nil {
		return "", nil
	}
	if media := content.Get("application/x-www-form-urlencoded"); media != nil {
		return "application/x-www-form-urlencoded", media
	}
	return "", nil
}

func uniqueToolName(operationID string, method string, pathName string, prefix string, used map[string]int) string {
	base := operationID
	if base == "" {
		base = strings.ToLower(method) + "_" + pathName
	}
	if prefix != "" {
		base = prefix + base
	}
	name := sanitizeToolName(base)
	if name == "" {
		name = "operation"
	}
	name = trimToolName(name, "")

	used[name]++
	if used[name] == 1 {
		return name
	}
	hash := shortHash(strings.ToUpper(method) + " " + pathName)
	candidate := trimToolName(name, hash) + "_" + hash
	for used[candidate] > 0 {
		used[name]++
		hash = shortHash(fmt.Sprintf("%s %s %d", strings.ToUpper(method), pathName, used[name]))
		candidate = trimToolName(name, hash) + "_" + hash
	}
	used[candidate] = 1
	return candidate
}

func sanitizeToolName(name string) string {
	var b strings.Builder
	lastSep := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.'
		if valid {
			if (r == '_' || r == '-' || r == '.') && lastSep {
				continue
			}
			b.WriteRune(r)
			lastSep = r == '_' || r == '-' || r == '.'
			continue
		}
		if !lastSep {
			b.WriteByte('_')
			lastSep = true
		}
	}
	return strings.Trim(b.String(), "_.-")
}

func trimToolName(name string, hash string) string {
	limit := maxToolNameLength
	if hash != "" {
		limit -= len(hash) + 1
	}
	if len(name) <= limit {
		return name
	}
	return strings.TrimRight(name[:limit], "_.-")
}

func shortHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

func buildDescription(method string, pathName string, operation *openapi3.Operation, security []SecurityRequirement, body *RequestBody, responseDocumentation string) string {
	var parts []string
	if operation.Summary != "" {
		parts = append(parts, operation.Summary)
	}
	if operation.Description != "" && operation.Description != operation.Summary {
		parts = append(parts, operation.Description)
	}
	parts = append(parts, fmt.Sprintf("%s %s", strings.ToUpper(method), pathName))
	if len(operation.Tags) > 0 {
		parts = append(parts, "Tags: "+strings.Join(operation.Tags, ", "))
	}
	if body != nil {
		if body.MediaType != "" {
			parts = append(parts, "Request body: "+body.MediaType)
		} else {
			parts = append(parts, "Request body: unsupported media type in v1")
		}
	}
	if operation.Responses != nil {
		codes := sortedResponseKeys(operation.Responses)
		if len(codes) > 0 {
			parts = append(parts, "Responses: "+strings.Join(codes, ", "))
		}
	}
	if security := securityRequirementNames(security); len(security) > 0 {
		parts = append(parts, "Security: "+strings.Join(security, ", "))
	}
	if responseDocumentation != "" {
		parts = append(parts, "Response schema:\n"+responseDocumentation)
	}
	return strings.Join(parts, "\n\n")
}

func firstAbsoluteServerURL(servers openapi3.Servers) *url.URL {
	for _, server := range servers {
		if server == nil || server.URL == "" || strings.Contains(server.URL, "{") {
			continue
		}
		u, err := url.Parse(server.URL)
		if err == nil && u.IsAbs() && u.Host != "" {
			return u
		}
	}
	return nil
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

func schemaRefToMap(ref *openapi3.SchemaRef) map[string]any {
	if ref == nil || ref.Value == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(ref.Value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}
