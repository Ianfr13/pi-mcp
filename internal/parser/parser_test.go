package parser

import (
	"context"
	"strings"
	"testing"

	"pi-mcp/internal/config"
)

func TestParseStream_CapturesSessionID(t *testing.T) {
	const in = `{"type":"session","version":3,"id":"sess-abc","cwd":"/x"}
{"type":"agent_start"}
`
	got, err := ParseStream(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.SessionID != "sess-abc" {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, "sess-abc")
	}
}

func TestParseStream_CapturesFirstSessionIDOnly(t *testing.T) {
	const in = `{"type":"session","id":"first"}
{"type":"session","id":"second"}
`
	got, err := ParseStream(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.SessionID != "first" {
		t.Fatalf("SessionID = %q, want %q (first session wins)", got.SessionID, "first")
	}
}

func TestParseStream_IgnoresUnknownType(t *testing.T) {
	const in = `{"type":"session","id":"s1"}
{"type":"__unknown__","weird":{"nested":[1,2,3]}}
{"type":"turn_start"}
not even json at all
{"type":"agent_end"}
`
	got, err := ParseStream(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.SessionID != "s1" {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, "s1")
	}
	if got.WorkflowFound {
		t.Fatalf("WorkflowFound = true, want false (no workflow event)")
	}
}

func TestResult_Err_NoWorkflowRun(t *testing.T) {
	const in = `{"type":"session","id":"s9"}
{"type":"message_start"}
{"type":"text","text":"Here is a direct answer, no workflow."}
{"type":"message_end"}
`
	got, err := ParseStream(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.WorkflowFound {
		t.Fatalf("WorkflowFound = true, want false")
	}
	if e := got.Err(); e != config.ErrNoWorkflowRun {
		t.Fatalf("Err() = %q, want %q", e, config.ErrNoWorkflowRun)
	}
}

func TestResult_Err_OKWhenWorkflowFound(t *testing.T) {
	const in = `{"type":"session","id":"s1"}
{"type":"tool_execution_end","toolName":"workflow","isError":false,"result":{"content":[{"type":"text","text":"x"}]}}
`
	got, _ := ParseStream(context.Background(), strings.NewReader(in))
	if e := got.Err(); e != "" {
		t.Fatalf("Err() = %q, want \"\"", e)
	}
}

func TestParseStream_WorkflowIsErrorSurfaced(t *testing.T) {
	const in = `{"type":"session","id":"s1"}
{"type":"tool_execution_end","toolName":"workflow","isError":true,"result":{"content":[{"type":"text","text":"boom"}]}}
`
	got, err := ParseStream(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if !got.WorkflowFound {
		t.Fatalf("WorkflowFound = false, want true")
	}
	if !got.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if got.RawText != "boom" {
		t.Fatalf("RawText = %q, want %q", got.RawText, "boom")
	}
}

func TestParseStream_NonWorkflowToolEndIgnored(t *testing.T) {
	const in = `{"type":"session","id":"s1"}
{"type":"tool_execution_end","toolName":"read_file","isError":false,"result":{"content":[{"type":"text","text":"file contents"}]}}
`
	got, err := ParseStream(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.WorkflowFound {
		t.Fatalf("WorkflowFound = true, want false (non-workflow tool)")
	}
}
