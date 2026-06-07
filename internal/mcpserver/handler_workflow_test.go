package mcpserver

import (
	"context"
	"testing"

	"pi-mcp/internal/model"
)

func newServerWith(j JobsService, s RunStore) *Server {
	return New(j, s) // wait poll interval defaulted; clock = time.Now
}

func TestWorkflow_ValidationErrors(t *testing.T) {
	srv := newServerWith(newFakeJobs(), newFakeStore())
	dir := t.TempDir()

	cases := []model.WorkflowInput{
		{Task: "t", Mode: "", CWD: dir},       // missing mode
		{Task: "t", Mode: "read", CWD: ""},    // missing cwd
		{Task: "t", Mode: "read", CWD: "rel"}, // relative cwd
		{Task: "", Mode: "read", CWD: dir},    // missing task
		{Task: "t", Mode: "bogus", CWD: dir},  // bad mode
	}
	for i, in := range cases {
		_, _, err := srv.handleWorkflow(context.Background(), nil, in)
		if err == nil {
			t.Fatalf("case %d expected validation error, got nil", i)
		}
	}
}

func TestWorkflow_TraversalRejected(t *testing.T) {
	srv := newServerWith(newFakeJobs(), newFakeStore())
	real := t.TempDir()
	_, _, err := srv.handleWorkflow(context.Background(), nil, model.WorkflowInput{
		Task: "t", Mode: "read", CWD: real + "/../escape",
	})
	if err == nil {
		t.Fatalf("'..' traversal in cwd must be rejected")
	}
}

func TestWorkflow_SubmitReadHappy(t *testing.T) {
	j := newFakeJobs()
	j.submitRec = model.JobRecord{
		JobID:     "job-123",
		Mode:      model.ModeRead,
		Status:    model.JobRunning,
		CWD:       "/resolved/cwd",
		StartedAt: mustTime("2026-06-07T16:51:33Z"),
	}
	srv := newServerWith(j, newFakeStore())
	dir := t.TempDir()

	_, out, err := srv.handleWorkflow(context.Background(), nil, model.WorkflowInput{
		Task: "judge claims", Mode: "read", CWD: dir, Context: "ctx",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.JobID != "job-123" || out.Status != "running" || out.Mode != "read" {
		t.Fatalf("bad output: %+v", out)
	}
	if out.WorktreePath != "" {
		t.Fatalf("read mode must not return worktree_path: %q", out.WorktreePath)
	}
	if out.StartedAt == "" {
		t.Fatalf("started_at must be set (RFC3339)")
	}
	// spec must carry the RESOLVED cwd (symlink-resolved temp dir), not the raw input.
	if j.lastSpec.CWD == "" || j.lastSpec.Task != "judge claims" || j.lastSpec.Mode != model.ModeRead {
		t.Fatalf("spec not built correctly: %+v", j.lastSpec)
	}
	if j.lastSpec.Context != "ctx" {
		t.Fatalf("context not forwarded: %+v", j.lastSpec)
	}
}

func TestWorkflow_SubmitWriteReturnsWorktree(t *testing.T) {
	j := newFakeJobs()
	j.submitRec = model.JobRecord{
		JobID: "job-w", Mode: model.ModeWrite, Status: model.JobQueued,
		CWD: "/c", WorktreePath: "/state/pi-mcp/worktrees/job-w",
		StartedAt: mustTime("2026-06-07T16:51:33Z"),
	}
	srv := newServerWith(j, newFakeStore())
	dir := t.TempDir()
	_, out, err := srv.handleWorkflow(context.Background(), nil, model.WorkflowInput{
		Task: "fix bug", Mode: "write", CWD: dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != "queued" || out.Mode != "write" {
		t.Fatalf("bad output: %+v", out)
	}
	if out.WorktreePath != "/state/pi-mcp/worktrees/job-w" {
		t.Fatalf("write must surface worktree_path, got %q", out.WorktreePath)
	}
}
