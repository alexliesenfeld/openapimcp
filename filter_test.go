package goapitomcp

import (
	"context"
	"strings"
	"testing"
)

func TestLoadCatalogFiltersOperations(t *testing.T) {
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec: strings.NewReader(filterSpec()),
		Filter: &OperationFilter{
			ExcludePathPatterns: []string{"/admin/*"},
			ExcludeMethods:      []string{"DELETE"},
			ExcludeTags:         []string{"debug"},
		},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if len(catalog.Operations) != 4 {
		t.Fatalf("operations = %d, want 4", len(catalog.Operations))
	}
	assertHasOperation(t, catalog, "GET", "/public")
	assertHasOperation(t, catalog, "GET", "/internal/status")
	assertHasOperation(t, catalog, "GET", "/users/{id}")
	assertHasOperation(t, catalog, "POST", "/users/{id}")
	assertNoOperation(t, catalog, "GET", "/admin/users")
	assertNoOperation(t, catalog, "DELETE", "/public")
	assertNoOperation(t, catalog, "GET", "/debug/status")
}

func TestLoadCatalogIncludeOnlyFilters(t *testing.T) {
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec: strings.NewReader(filterSpec()),
		Filter: &OperationFilter{
			IncludeOnlyPathPatterns: []string{"/users/*"},
			IncludeOnlyMethods:      []string{"get"},
			IncludeOnlyTags:         []string{"users"},
		},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if len(catalog.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(catalog.Operations))
	}
	op := catalog.Operations[0]
	if op.OperationID != "getUser" {
		t.Fatalf("OperationID = %q, want getUser", op.OperationID)
	}
}

func TestLoadCatalogAppliesExcludeAfterIncludeOnlyFilters(t *testing.T) {
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec: strings.NewReader(filterSpec()),
		Filter: &OperationFilter{
			IncludeOnlyPaths:        []string{"/users/{id}"},
			IncludeOnlyOperationIDs: []string{"getUser", "updateUser"},
			ExcludeOperationIDs:     []string{"updateUser"},
		},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	if len(catalog.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(catalog.Operations))
	}
	op := catalog.Operations[0]
	if op.OperationID != "getUser" {
		t.Fatalf("OperationID = %q, want getUser", op.OperationID)
	}
}

func TestLoadCatalogExcludesInternalOperations(t *testing.T) {
	catalog, err := LoadCatalog(context.Background(), Config{
		Spec: strings.NewReader(filterSpec()),
		Filter: &OperationFilter{
			ExcludeInternal: true,
		},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	assertNoOperation(t, catalog, "GET", "/internal/status")
	assertHasOperation(t, catalog, "GET", "/public")
}

func TestOperationFilterAllowsWildcardPathPatterns(t *testing.T) {
	filter := &OperationFilter{
		ExcludePathPatterns: []string{"/api/v*/{resource}"},
	}
	if filter.Allows("GET", "/api/v1/users", "listUsers", []string{"users"}) {
		t.Fatalf("Allows() = true, want false for wildcard match")
	}
	if !filter.Allows("GET", "/api/v1/users/active", "listUsers", []string{"users"}) {
		t.Fatalf("Allows() = false, want true for different segment count")
	}
}

func assertHasOperation(t *testing.T, catalog *Catalog, method string, path string) {
	t.Helper()
	for _, op := range catalog.Operations {
		if op.Method == method && op.Path == path {
			return
		}
	}
	t.Fatalf("operation %s %s not found in %#v", method, path, operationNames(catalog))
}

func assertNoOperation(t *testing.T, catalog *Catalog, method string, path string) {
	t.Helper()
	for _, op := range catalog.Operations {
		if op.Method == method && op.Path == path {
			t.Fatalf("operation %s %s unexpectedly found", method, path)
		}
	}
}

func operationNames(catalog *Catalog) []string {
	names := make([]string, 0, len(catalog.Operations))
	for _, op := range catalog.Operations {
		names = append(names, op.Method+" "+op.Path)
	}
	return names
}

func filterSpec() string {
	return `
openapi: 3.0.3
info:
  title: Filter API
  version: 1.0.0
paths:
  /public:
    get:
      operationId: listPublic
      tags: [public]
      responses:
        '200':
          description: ok
    delete:
      operationId: deletePublic
      tags: [public]
      responses:
        '204':
          description: deleted
  /admin/users:
    get:
      operationId: listAdminUsers
      tags: [admin]
      responses:
        '200':
          description: ok
  /debug/status:
    get:
      operationId: debugStatus
      tags: [debug]
      responses:
        '200':
          description: ok
  /internal/status:
    get:
      operationId: internalStatus
      tags: [internal]
      x-internal: true
      responses:
        '200':
          description: ok
  /users/{id}:
    get:
      operationId: getUser
      tags: [users]
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: ok
    post:
      operationId: updateUser
      tags: [users]
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: ok
`
}
