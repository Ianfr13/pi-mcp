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

	if len(out.Events) != 4 {
		t.Fatalf("want 4 events, got %d", len(out.Events))
	}
	first := out.Events[0]
	if first.Result != nil {
		t.Fatalf("events default to no result body; got %#v", first.Result)
	}
	// Agent count fields are part of the schema: the reflection walk must
	// cover them, so the validation pass exercises the wire types end-to-end.
	if out.AgentsTotal != 4 {
		t.Fatalf("agentsTotal: want 4, got %d", out.AgentsTotal)
	}
	if out.AgentsDone != 4 {
		t.Fatalf("agentsDone: want 4 (every journal entry joined an agent), got %d", out.AgentsDone)
	}
	// A separate from_start + include_results call must surface the journal object.
	irRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "pi_status",
		Arguments: map[string]any{"jobId": "job-obj", "from_start": true, "include_results": true},
	})
	if err != nil {
		t.Fatalf("include_results CallTool: %v", err)
	}
	if irRes.IsError {
		t.Fatalf("include_results IsError: %s", contentTextOf(irRes))
	}
	var irOut model.StatusOutput
	if err := decodeToolResult(irRes, &irOut); err != nil {
		t.Fatalf("decode include_results StatusOutput: %v", err)
	}
	if len(irOut.Events) == 0 {
		t.Fatalf("from_start+include_results must surface events")
	}
	evObj, ok := irOut.Events[0].Result.(map[string]any)
	if !ok {
		t.Fatalf("events[0].result (include_results=true) is not a JSON object: %#v", irOut.Events[0].Result)
	}
	if evObj["claim"] == nil {
		t.Fatalf("events[0].result lost object content: %#v", evObj)
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
