package goapitomcp

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadCatalogBuildsOperationNamesAndGroupedSchemas(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Demo API
  version: 1.2.3
servers:
  - url: https://api.example.test/v1
paths:
  /pets/{id}:
    get:
      operationId: get pet!
      summary: Get a pet
      tags: [pets]
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
  /pets:
    get:
      summary: List pets
      responses:
        '200':
          description: ok
  /things:
    get:
      operationId: duplicate!
      responses:
        '200':
          description: ok
    post:
      operationId: duplicate?
      responses:
        '201':
          description: created
`

	catalog, err := LoadCatalog(context.Background(), Config{Spec: strings.NewReader(spec)})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if catalog.Name != "Demo_API" {
		t.Fatalf("catalog.Name = %q, want sanitized title", catalog.Name)
	}
	if catalog.Version != "1.2.3" {
		t.Fatalf("catalog.Version = %q, want spec version", catalog.Version)
	}
	if got := catalog.BaseURL.String(); got != "https://api.example.test/v1" {
		t.Fatalf("BaseURL = %q", got)
	}
	if len(catalog.Operations) != 4 {
		t.Fatalf("operations = %d, want 4", len(catalog.Operations))
	}

	op := findOperation(t, catalog, "GET", "/pets/{id}")
	if op.ToolName != "get_pet" {
		t.Fatalf("ToolName = %q, want get_pet", op.ToolName)
	}
	properties := op.InputSchema["properties"].(map[string]any)
	if _, ok := properties["path"]; !ok {
		t.Fatalf("input schema missing path group: %#v", op.InputSchema)
	}
	if _, ok := properties["query"]; !ok {
		t.Fatalf("input schema missing query group: %#v", op.InputSchema)
	}
	required := stringSlice(op.InputSchema["required"])
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("top-level required = %#v, want [path]", required)
	}

	fallback := findOperation(t, catalog, "GET", "/pets")
	if fallback.ToolName != "get_pets" {
		t.Fatalf("fallback ToolName = %q, want get_pets", fallback.ToolName)
	}
	first := findOperation(t, catalog, "GET", "/things")
	second := findOperation(t, catalog, "POST", "/things")
	if first.ToolName != "duplicate" {
		t.Fatalf("first duplicate ToolName = %q", first.ToolName)
	}
	if second.ToolName == "duplicate" || !strings.HasPrefix(second.ToolName, "duplicate_") {
		t.Fatalf("second duplicate ToolName = %q, want hash suffix", second.ToolName)
	}
}

func TestLoadCatalogIncludesPrefixSecurityAndResponseSchemas(t *testing.T) {
	spec := `
openapi: 3.0.3
info:
  title: Secure API
  version: 1.0.0
components:
  securitySchemes:
    QueryKey:
      type: apiKey
      in: query
      name: api_key
      x-defaultCredential: query-secret
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: JWT
security:
  - QueryKey: []
paths:
  /users/{id}:
    get:
      operationId: getUser
      summary: Get user
      security:
        - BearerAuth: []
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: Successful response
          content:
            application/json:
              schema:
                type: object
                required: [id, email]
                properties:
                  id:
                    type: string
                    description: User ID
                  email:
                    type: string
                    description: Email address
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:           strings.NewReader(spec),
		ToolNamePrefix: "api_",
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if len(catalog.SecuritySchemes) != 2 {
		t.Fatalf("SecuritySchemes = %#v, want two schemes", catalog.SecuritySchemes)
	}
	if catalog.SecuritySchemes[1].ID != "QueryKey" || catalog.SecuritySchemes[1].DefaultCredential != "query-secret" {
		t.Fatalf("SecuritySchemes = %#v, want sorted QueryKey with default credential", catalog.SecuritySchemes)
	}

	op := findOperation(t, catalog, "GET", "/users/{id}")
	if op.ToolName != "api_getUser" {
		t.Fatalf("ToolName = %q, want api_getUser", op.ToolName)
	}
	if len(op.Security) != 1 || op.Security[0].ID != "BearerAuth" {
		t.Fatalf("Security = %#v, want BearerAuth", op.Security)
	}
	if !strings.Contains(op.Description, "Response schema") || !strings.Contains(op.ResponseDocumentation, "body.email") {
		t.Fatalf("response documentation missing from description: %q", op.Description)
	}
	properties := op.OutputSchema["properties"].(map[string]any)
	body := properties["body"].(map[string]any)
	bodyProps := body["properties"].(map[string]any)
	if bodyProps["email"] == nil {
		t.Fatalf("output body schema = %#v, want email property", body)
	}
}

func TestLoadCatalogRejectsUnsupportedOpenAPIVersion(t *testing.T) {
	spec := `
openapi: 3.2.0
info:
  title: Demo
  version: 1.0.0
paths: {}
`
	_, err := LoadCatalog(context.Background(), Config{Spec: strings.NewReader(spec)})
	if err == nil || !strings.Contains(err.Error(), "unsupported OpenAPI version") {
		t.Fatalf("LoadCatalog() error = %v, want unsupported version", err)
	}
}

func TestLoadCatalogAllowsNoOperationSpec(t *testing.T) {
	spec := `
openapi: 3.1.0
info:
  title: Empty
  version: 1.0.0
paths: {}
`
	catalog, err := LoadCatalog(context.Background(), Config{Spec: strings.NewReader(spec)})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if len(catalog.Operations) != 0 {
		t.Fatalf("operations = %d, want 0", len(catalog.Operations))
	}
}

func TestLoadCatalogResolvesLocalMultiFileRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "params.yaml"), `
openapi: 3.0.3
info:
  title: Params
  version: 1.0.0
paths: {}
components:
  parameters:
    PetID:
      name: id
      in: path
      required: true
      schema:
        type: string
`)
	schemaDir := filepath.Join(dir, "schemas")
	if err := os.Mkdir(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(schemaDir, "pet.yaml"), `
openapi: 3.0.3
info:
  title: Schemas
  version: 1.0.0
paths: {}
components:
  schemas:
    Pet:
      type: object
      required: [id]
      properties:
        id:
          type: string
`)
	rootPath := filepath.Join(dir, "openapi.yaml")
	root := `
openapi: 3.0.3
info:
  title: Multi
  version: 1.0.0
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - $ref: './params.yaml#/components/parameters/PetID'
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: './schemas/pet.yaml#/components/schemas/Pet'
`
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec:        strings.NewReader(root),
		SpecBaseURI: fileURL(t, rootPath),
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	op := findOperation(t, catalog, "GET", "/pets/{id}")
	if len(op.Parameters) != 1 || op.Parameters[0].Name != "id" {
		t.Fatalf("parameters = %#v, want resolved id parameter", op.Parameters)
	}
}

func TestLoadCatalogFromFSResolvesLocalRefs(t *testing.T) {
	files := fstest.MapFS{
		"openapi.yaml": &fstest.MapFile{Data: []byte(`
openapi: 3.0.3
info:
  title: Embedded API
  version: 1.0.0
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - $ref: './parameters.yaml#/components/parameters/PetID'
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: './schemas.yaml#/components/schemas/Pet'
`)},
		"parameters.yaml": &fstest.MapFile{Data: []byte(`
openapi: 3.0.3
info:
  title: Parameters
  version: 1.0.0
paths: {}
components:
  parameters:
    PetID:
      name: petId
      in: path
      required: true
      schema:
        type: string
`)},
		"schemas.yaml": &fstest.MapFile{Data: []byte(`
openapi: 3.0.3
info:
  title: Schemas
  version: 1.0.0
paths: {}
components:
  schemas:
    Pet:
      type: object
      properties:
        id:
          type: string
          description: Pet ID
`)},
	}

	catalog, err := LoadCatalogFromFS(context.Background(), files, "openapi.yaml", Config{})
	if err != nil {
		t.Fatalf("LoadCatalogFromFS() error = %v", err)
	}

	op := findOperation(t, catalog, "GET", "/pets/{petId}")
	if len(op.Parameters) != 1 || op.Parameters[0].Name != "petId" {
		t.Fatalf("Parameters = %#v, want resolved petId", op.Parameters)
	}
	if !strings.Contains(op.ResponseDocumentation, "body.id") {
		t.Fatalf("ResponseDocumentation = %q, want resolved schema fields", op.ResponseDocumentation)
	}
}

func TestLoadCatalogFromFileSetsSpecBaseURIForLocalRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "schemas.yaml"), `
openapi: 3.0.3
info:
  title: Schemas
  version: 1.0.0
paths: {}
components:
  schemas:
    Status:
      type: object
      properties:
        ok:
          type: boolean
`)
	rootPath := filepath.Join(dir, "openapi.yaml")
	writeFile(t, rootPath, `
openapi: 3.0.3
info:
  title: File API
  version: 1.0.0
paths:
  /status:
    get:
      operationId: getStatus
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: './schemas.yaml#/components/schemas/Status'
`)

	catalog, err := LoadCatalogFromFile(context.Background(), rootPath, Config{})
	if err != nil {
		t.Fatalf("LoadCatalogFromFile() error = %v", err)
	}
	op := findOperation(t, catalog, "GET", "/status")
	if !strings.Contains(op.ResponseDocumentation, "body.ok") {
		t.Fatalf("ResponseDocumentation = %q, want resolved body.ok", op.ResponseDocumentation)
	}
}

func TestLoadCatalogRejectsRefsEscapingRoot(t *testing.T) {
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "spec")
	if err := os.Mkdir(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "outside.yaml"), `
openapi: 3.0.3
info:
  title: Outside
  version: 1.0.0
paths: {}
components:
  schemas:
    Pet:
      type: object
`)
	rootPath := filepath.Join(rootDir, "openapi.yaml")
	root := `
openapi: 3.0.3
info:
  title: Escape
  version: 1.0.0
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: '../outside.yaml#/components/schemas/Pet'
`
	_, err := LoadCatalog(context.Background(), Config{
		Spec:        strings.NewReader(root),
		SpecBaseURI: fileURL(t, rootPath),
	})
	if err == nil || !strings.Contains(err.Error(), "escapes RefRoot") {
		t.Fatalf("LoadCatalog() error = %v, want escaping ref rejection", err)
	}
}

func findOperation(t *testing.T, catalog *Catalog, method, path string) *Operation {
	t.Helper()
	for _, op := range catalog.Operations {
		if op.Method == method && op.Path == path {
			return op
		}
	}
	t.Fatalf("operation %s %s not found", method, path)
	return nil
}

func stringSlice(v any) []string {
	raw, ok := v.([]string)
	if ok {
		return raw
	}
	anys, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(anys))
	for _, item := range anys {
		out = append(out, item.(string))
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileURL(t *testing.T, path string) *url.URL {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
}
