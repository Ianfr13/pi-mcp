package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/config"
)

// fixturePath resolves a file under docs/research/fixtures from the repo root. The
// parser package lives at internal/parser, so the repo root is two levels up.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "docs", "research", "fixtures", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture %s not found at %s: %v", name, p, err)
	}
	return p
}

func TestParseStream_PositiveFixture(t *testing.T) {
	f, err := os.Open(fixturePath(t, "sample-pi-mode-json-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ParseStream(context.Background(), f)
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.SessionID != "019ea2fe-db76-7e31-85ad-9718d3fbc23a" {
		t.Fatalf("SessionID = %q", got.SessionID)
	}
	if !got.WorkflowFound {
		t.Fatal("WorkflowFound = false, want true")
	}
	if got.IsError {
		t.Fatal("IsError = true, want false")
	}
	if e := got.Err(); e != "" {
		t.Fatalf("Err() = %q, want \"\"", e)
	}
}

func TestParseStream_NegativeFixture_NoWorkflowRun(t *testing.T) {
	f, err := os.Open(fixturePath(t, "sample-run-no-workflow.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := ParseStream(context.Background(), f)
	if err != nil {
		t.Fatalf("ParseStream error: %v", err)
	}
	if got.SessionID != "019ea300-0000-7000-8000-000000000001" {
		t.Fatalf("SessionID = %q", got.SessionID)
	}
	if got.WorkflowFound {
		t.Fatal("WorkflowFound = true, want false")
	}
	if e := got.Err(); e != config.ErrNoWorkflowRun {
		t.Fatalf("Err() = %q, want %q", e, config.ErrNoWorkflowRun)
	}
}
