package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/alexliesenfeld/openapimcp"
)

const openAPISpec = `
openapi: 3.1.0
info:
  title: Example Pet API
  version: 1.0.0
paths:
  /pets/{id}:
    get:
      operationId: getPet
      summary: Get a pet by ID
      description: Returns a single pet from the example API.
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
          description: Pet response
          content:
            application/json:
              schema:
                type: object
                required: [id, name]
                properties:
                  id:
                    type: string
                  name:
                    type: string
                  verbose:
                    type: boolean
`

func main() {
	ctx := context.Background()
	baseURL, err := url.Parse("http://localhost:8080/api")
	if err != nil {
		log.Fatal(err)
	}

	mcpHandler, err := goapitomcp.NewHandler(ctx, goapitomcp.Config{
		Spec:    strings.NewReader(openAPISpec),
		BaseURL: baseURL,
		BeforeRequest: func(_ context.Context, _ goapitomcp.OperationContext, r *http.Request) error {
			r.Header.Set("X-Example-Client", "goapitomcp")
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/pets/", handlePet)
	mux.Handle("/mcp", mcpHandler)

	log.Println("example API: http://localhost:8080/api/pets/123?verbose=true")
	log.Println("MCP endpoint: http://localhost:8080/mcp")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func handlePet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/pets/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	verbose := r.URL.Query().Get("verbose") == "true"

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      id,
		"name":    "Milo",
		"verbose": verbose,
	})
}
