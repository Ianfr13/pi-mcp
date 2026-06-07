package jobs

import (
	"path/filepath"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

func TestPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	in := []model.JobRecord{
		{JobID: "a", RunID: "r1", SessionID: "s1", Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/.pi/workflows/runs", PID: 111,
			Status: model.JobRunning, StartedAt: time.Unix(1000, 0).UTC()},
		{JobID: "b", RunID: "", SessionID: "", Mode: model.ModeWrite,
			CWD: "/wt", WorktreePath: "/wt", Branch: "pi-mcp/job-b", PID: 222,
			Status: model.JobCompleted, StartedAt: time.Unix(2000, 0).UTC()},
	}
	if err := persist(path, in); err != nil {
		t.Fatalf("persist: %v", err)
	}

	out, err := loadPersisted(path)
	if err != nil {
		t.Fatalf("loadPersisted: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 records, got %d", len(out))
	}
	if out[0].JobID != "a" || out[0].PID != 111 || out[0].Status != model.JobRunning {
		t.Fatalf("record 0 mismatch: %+v", out[0])
	}
	if out[1].Branch != "pi-mcp/job-b" || out[1].Status != model.JobCompleted {
		t.Fatalf("record 1 mismatch: %+v", out[1])
	}
}

func TestLoadPersistedMissingFileIsEmpty(t *testing.T) {
	out, err := loadPersisted(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty slice, got %d", len(out))
	}
}

func TestPersistIsAtomicNoTmpLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	if err := persist(path, []model.JobRecord{{JobID: "x", Status: model.JobQueued}}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("expected no .tmp leftovers, got %v", matches)
	}
}
