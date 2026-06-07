package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/model"
)

// TestStatusOutputPassesGoSDKSchemaValidation is the BUG 2 regression. go-sdk
// v1.0.0 reflects json.RawMessage as schema type "null|array" AND validates
// outgoing tool output, so a StatusOutput whose Result (and intermediate
// Result) is a JSON OBJECT was rejected with:
//
//	validating tool output: ... /properties/intermediate/items/properties/result:
//	value has type "object", want one of "null, array"
//
// With Result typed as `any` (unconstrained schema) the object/array/scalar
// passes. This test registers the REAL pi_status tool, calls it through an
// in-process go-sdk client/server (the same validation path the e2e exercises),
// and asserts: no MCP/validation error, not IsError, and an OBJECT result plus
// an intermediate OBJECT result round-trip intact.
func TestStatusOutputPassesGoSDKSchemaValidation(t *testing.T) {
	ctx := context.Background()

	// buildRun() is a completed run with OBJECT journal results and an OBJECT
	// .result — exactly the shape that tripped the validator.
	run := buildRun()
	js := newFakeJobs()
	js.lookup["job-obj"] = model.JobRecord{
		JobID: "job-obj", RunsDir: "/runs", RunID: run.RunID,
		Mode: model.ModeRead, PID: 1, Status: model.JobRunning,
	}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run

	srv := New(js, store)

	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "pi-mcp", Version: "v0.1.0"}, nil)
	srv.Register(mcpSrv)

	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := mcpSrv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "pi_status",
		Arguments: map[string]any{"jobId": "job-obj"},
	})
	if err != nil {
		// A "validating tool output" error here is the BUG 2 failure.
		t.Fatalf("pi_status CallTool returned an MCP error (output-schema validation?): %v", err)
	}
	if res.IsError {
		t.Fatalf("pi_status returned IsError: %s", contentTextOf(res))
	}

	// Decode the structured output and assert the OBJECT results survived.
	var out model.StatusOutput
	if err := decodeToolResult(res, &out); err != nil {
		t.Fatalf("decode StatusOutput: %v; content=%s", err, contentTextOf(res))
	}
	if out.Status != "completed" {
		t.Fatalf("status = %q, want completed", out.Status)
	}

	resObj, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("final result is not a JSON object: %#v", out.Result)
	}
	if _, ok := resObj["claims"]; !ok {
		t.Fatalf("final result lost original object keys: %#v", resObj)
	}
	if _, ok := resObj["summary"]; !ok {
		t.Fatalf("final result missing coerced summary: %#v", resObj)
	}

	if len(out.Intermediate) != 4 {
		t.Fatalf("want 4 intermediate, got %d", len(out.Intermediate))
	}
	first := out.Intermediate[0]
	irObj, ok := first.Result.(map[string]any)
	if !ok {
		t.Fatalf("intermediate[0].result is not a JSON object: %#v", first.Result)
	}
	if irObj["claim"] == nil {
		t.Fatalf("intermediate[0].result lost object content: %#v", irObj)
	}
}

// decodeToolResult re-marshals StructuredContent (arrives as a generic map) and
// unmarshals into out; falls back to the first text content block.
func decodeToolResult(res *mcp.CallToolResult, out any) error {
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	return json.Unmarshal([]byte(contentTextOf(res)), out)
}

func contentTextOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
