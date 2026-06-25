package goapitomcp

import (
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

func buildOutputSchema(operation *openapi3.Operation) map[string]any {
	responseInfo := selectSuccessResponse(operation)
	bodySchema := map[string]any{}
	description := ""
	if responseInfo.response != nil && responseInfo.response.Description != nil {
		description = *responseInfo.response.Description
	}
	if responseInfo.media != nil && responseInfo.media.Schema != nil {
		bodySchema = schemaRefToMap(responseInfo.media.Schema)
	}

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"status": map[string]any{
				"type":        "integer",
				"description": "HTTP response status code.",
			},
			"contentType": map[string]any{
				"type":        "string",
				"description": "HTTP response Content-Type header.",
			},
			"body": bodySchema,
		},
		"required": []string{"status", "body"},
	}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func buildResponseDocumentation(operation *openapi3.Operation) string {
	responseInfo := selectSuccessResponse(operation)
	if responseInfo.media == nil || responseInfo.media.Schema == nil || responseInfo.media.Schema.Value == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Successful response structure")
	if responseInfo.status != "" {
		b.WriteString(" (")
		b.WriteString(responseInfo.status)
		b.WriteString(")")
	}
	if responseInfo.mediaType != "" {
		b.WriteString("\nContent-Type: ")
		b.WriteString(responseInfo.mediaType)
	}
	b.WriteString("\n")
	writeSchemaDocumentation(&b, responseInfo.media.Schema.Value, "body", 0, 8)
	return strings.TrimSpace(b.String())
}

type successResponseInfo struct {
	status    string
	mediaType string
	response  *openapi3.Response
	media     *openapi3.MediaType
}

func selectSuccessResponse(operation *openapi3.Operation) successResponseInfo {
	if operation == nil || operation.Responses == nil {
		return successResponseInfo{}
	}
	keys := sortedResponseKeys(operation.Responses)
	var emptySuccess successResponseInfo
	for _, key := range keys {
		if !strings.HasPrefix(key, "2") {
			continue
		}
		ref := operation.Responses.Value(key)
		if ref == nil || ref.Value == nil {
			continue
		}
		mediaType, media := selectJSONMediaType(ref.Value.Content)
		if media != nil {
			return successResponseInfo{status: key, mediaType: mediaType, response: ref.Value, media: media}
		}
		if len(ref.Value.Content) == 0 && emptySuccess.response == nil {
			emptySuccess = successResponseInfo{status: key, response: ref.Value}
		}
	}
	if emptySuccess.response != nil {
		return emptySuccess
	}
	if ref := operation.Responses.Default(); ref != nil && ref.Value != nil {
		mediaType, media := selectJSONMediaType(ref.Value.Content)
		return successResponseInfo{status: "default", mediaType: mediaType, response: ref.Value, media: media}
	}
	return successResponseInfo{}
}

func writeSchemaDocumentation(b *strings.Builder, schema *openapi3.Schema, path string, depth int, maxDepth int) {
	if schema == nil || depth > maxDepth {
		return
	}
	indent := strings.Repeat("  ", depth)
	schemaType := schemaTypeName(schema)
	description := strings.TrimSpace(schema.Description)
	if description != "" || schemaType != "" {
		fmt.Fprintf(b, "%s- %s", indent, path)
		if description != "" {
			fmt.Fprintf(b, ": %s", description)
		}
		if schemaType != "" {
			fmt.Fprintf(b, " (Type: %s)", schemaType)
		}
		b.WriteString("\n")
	}

	if schema.Items != nil && schema.Items.Value != nil {
		writeSchemaDocumentation(b, schema.Items.Value, path+"[]", depth+1, maxDepth)
	}

	if len(schema.Properties) == 0 {
		return
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		ref := schema.Properties[name]
		if ref == nil || ref.Value == nil {
			continue
		}
		writeSchemaDocumentation(b, ref.Value, path+"."+name, depth+1, maxDepth)
	}
}

func schemaTypeName(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil || len(schema.Type.Slice()) == 0 {
		if len(schema.Properties) > 0 {
			return "object"
		}
		if schema.Items != nil {
			return "array"
		}
		return ""
	}
	return strings.Join(schema.Type.Slice(), "|")
}
