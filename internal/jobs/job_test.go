package jobs

import (
	"testing"

	"pi-mcp/internal/model"
)

func TestNewJobInitializesQueuedRecord(t *testing.T) {
	spec := Spec{
		Mode:     model.ModeRead,
		CWD:      "/proj",
		RunsDir:  "/proj/.pi/workflows/runs",
		Worktree: "",
		Branch:   "",
	}
	j := NewJob(spec)

	if j.Record.JobID == "" {
		t.Fatal("expected non-empty jobID")
	}
	if len(j.Record.JobID) != 36 {
		t.Fatalf("expected uuidv4 (36 chars), got %q", j.Record.JobID)
	}
	if j.Record.Status != model.JobQueued {
		t.Fatalf("expected status %q, got %q", model.JobQueued, j.Record.Status)
	}
	if j.Record.Mode != model.ModeRead {
		t.Fatalf("expected mode %q, got %q", model.ModeRead, j.Record.Mode)
	}
	if j.Record.CWD != "/proj" || j.Record.RunsDir != "/proj/.pi/workflows/runs" {
		t.Fatalf("spec fields not copied: %+v", j.Record)
	}
	if j.Record.StartedAt.IsZero() {
		t.Fatal("expected StartedAt to be set")
	}
	if j.done == nil {
		t.Fatal("expected done channel initialized")
	}

	j2 := NewJob(spec)
	if j2.Record.JobID == j.Record.JobID {
		t.Fatal("expected unique jobIDs")
	}
}

func TestNewJobHonorsPreassignedID(t *testing.T) {
	j := NewJob(Spec{Mode: model.ModeWrite, PreassignedID: "preset-id"})
	if j.Record.JobID != "preset-id" {
		t.Fatalf("expected preassigned id, got %q", j.Record.JobID)
	}
}

func TestNewJobCopiesTaskContextOffRecord(t *testing.T) {
	j := NewJob(Spec{Mode: model.ModeRead, Task: "do the thing", Context: "secret ctx"})
	if j.task != "do the thing" {
		t.Fatalf("expected task copied onto Job, got %q", j.task)
	}
	if j.context != "secret ctx" {
		t.Fatalf("expected context copied onto Job, got %q", j.context)
	}
	// Task/Context must NOT be persisted onto the JobRecord (secrets, spec §1).
	rec := j.snapshot()
	if got := stringContains(rec, "do the thing"); got {
		t.Fatal("task must not appear in persisted record")
	}
	if got := stringContains(rec, "secret ctx"); got {
		t.Fatal("context must not appear in persisted record")
	}
}

// stringContains marshals the record-derived string fields and checks no field
// carries the secret. JobRecord has no Task/Context fields by contract; this is
// a defensive check that NewJob does not smuggle them in.
func stringContains(rec model.JobRecord, needle string) bool {
	fields := []string{rec.JobID, rec.RunID, rec.SessionID, rec.CWD, rec.RunsDir,
		rec.WorktreePath, rec.Branch, rec.ErrorCode, rec.ErrorMessage}
	for _, f := range fields {
		if f == needle {
			return true
		}
	}
	return false
}

func TestNewID(t *testing.T) {
	a := NewID()
	b := NewID()
	if len(a) != 36 || len(b) != 36 {
		t.Fatalf("expected uuidv4 strings, got %q and %q", a, b)
	}
	if a == b {
		t.Fatal("expected unique ids")
	}
}
