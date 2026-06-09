package dashboard

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

// TestBuildDetail_RendersFromSnapshotWhenFileGone proves the reported symptom is
// fixed: a terminal job whose on-disk run file is gone (temp cwd cleaned /
// worktree pruned) still renders its full detail from the persisted snapshot.
func TestBuildDetail_RendersFromSnapshotWhenFileGone(t *testing.T) {
	run := model.Run{
		RunID:        "r1",
		Status:       "completed",
		WorkflowName: "judge",
		Phases:       []string{"Scan"},
		Agents: []model.Agent{
			{ID: 1, CallIndex: 0, Label: "a0", Model: "m1", Phase: "Scan", Status: "done", Tokens: 10},
		},
		Journal: []model.JournalEntry{
			{Index: 0, Result: json.RawMessage(`{"verdict":"TRUE"}`)},
		},
		Result: json.RawMessage(`{"summary":"two true one false"}`),
	}
	snap, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	rec := model.JobRecord{
		JobID: "j1", RunID: "r1", Mode: model.ModeRead, CWD: "/gone",
		RunsDir:     filepath.Join(t.TempDir(), "absent"), // no run file on disk
		Status:      model.JobCompleted,
		StartedAt:   nowFresh.Add(-time.Minute),
		RunSnapshot: snap,
	}

	d, ok := BuildDetail(rec, nowFresh)
	if !ok {
		t.Fatal("BuildDetail ok=false")
	}
	if d.Status != "completed" {
		t.Fatalf("status=%q want completed (derived from snapshot)", d.Status)
	}
	if len(d.Agents) != 1 || d.Agents[0].Label != "a0" {
		t.Fatalf("agents not rendered from snapshot: %+v", d.Agents)
	}
	if len(d.Intermediate) != 1 || d.Intermediate[0].Label != "a0" {
		t.Fatalf("intermediate not rendered from snapshot: %+v", d.Intermediate)
	}
	if d.Result == nil {
		t.Fatalf("result not rendered from snapshot")
	}
}

// TestBuildDetail_NoSnapshotNoFile keeps the legit empty state: neither run file
// nor snapshot -> summary-only (the real "No run data" case).
func TestBuildDetail_NoSnapshotNoFile(t *testing.T) {
	rec := model.JobRecord{
		JobID: "j2", RunID: "r2", Mode: model.ModeRead, CWD: "/gone",
		RunsDir: filepath.Join(t.TempDir(), "absent"), Status: model.JobCompleted,
		StartedAt: nowFresh.Add(-time.Minute),
	}
	d, ok := BuildDetail(rec, nowFresh)
	if !ok {
		t.Fatal("BuildDetail ok=false")
	}
	if len(d.Agents) != 0 {
		t.Fatalf("expected summary-only (no agents), got %+v", d.Agents)
	}
	if d.Status != "completed" {
		t.Fatalf("status=%q want completed (from registry row)", d.Status)
	}
}
