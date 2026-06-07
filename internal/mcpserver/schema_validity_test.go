package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"pi-mcp/internal/model"
)

// schemaJSON reflects T's JSON schema exactly as mcp.AddTool does, then decodes
// it into a generic tree for structural assertions.
func schemaJSON[T any](t *testing.T) any {
	t.Helper()
	s, err := jsonschema.For[T](nil)
	if err != nil {
		t.Fatalf("jsonschema.For: %v", err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return v
}

// walkAssertNoBoolProperty fails if any value under a "properties" object is a
// JSON boolean schema (true/false). JSON Schema permits boolean subschemas, but
// Claude Code's MCP client validates tool outputSchemas with a stricter Zod
// validator that rejects a boolean property schema with
// {"code":"custom","message":"Invalid input"} — which made tools/list fail and
// the whole server show as errored. An `any`/interface{} field with no
// `jsonschema:"..."` tag reflects to `true`; a description tag makes it an
// object schema ({"description":...}) that accepts any JSON and validates.
func walkAssertNoBoolProperty(t *testing.T, where string, node any) {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			for name, sub := range props {
				if _, isBool := sub.(bool); isBool {
					t.Errorf("%s: property %q is a boolean schema (%v) — Claude Code rejects this; give the field a jsonschema description tag", where, name, sub)
				}
			}
		}
		for k, v := range n {
			walkAssertNoBoolProperty(t, where+"."+k, v)
		}
	case []any:
		for _, v := range n {
			walkAssertNoBoolProperty(t, where, v)
		}
	}
}

// TestToolOutputSchemasHaveNoBooleanPropertySchemas reproduces the Claude Code
// "tools/list failed ... outputSchema.properties.result: Invalid input" bug for
// every tool's output struct, so any future `any` field that loses its
// jsonschema tag is caught before it reaches a real client.
func TestToolOutputSchemasHaveNoBooleanPropertySchemas(t *testing.T) {
	cases := map[string]any{
		"WorkflowOutput": schemaJSON[model.WorkflowOutput](t),
		"StatusOutput":   schemaJSON[model.StatusOutput](t),
		"ListOutput":     schemaJSON[model.ListOutput](t),
		"CancelOutput":   schemaJSON[model.CancelOutput](t),
	}
	for name, sc := range cases {
		walkAssertNoBoolProperty(t, name, sc)
	}
}
