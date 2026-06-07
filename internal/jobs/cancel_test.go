package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

func TestCancelRunningReadJobAbortsNoPrune(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("s")
	fp := &fakePruner{}
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")},
		fl, &fakeCorrelator{}, fp)

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})

	out, err := r.Cancel(rec.JobID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if out.Status != model.JobAborted {
		t.Fatalf("expected aborted, got %q", out.Status)
	}
	r.waitJob(rec.JobID)
	if got, _ := r.Lookup(rec.JobID); got.Status != model.JobAborted {
		t.Fatalf("expected aborted after wait, got %q", got.Status)
	}
	if fp.callCount() != 0 {
		t.Fatalf("read job must not prune, got %d prune calls", fp.callCount())
	}
}

func TestCancelRunningWriteJobPrunesWorktree(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("s")
	fp := &fakePruner{}
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")},
		fl, &fakeCorrelator{}, fp)

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeWrite, CWD: "/wt", RunsDir: "/wt/runs",
		Worktree: "/wt", Branch: "pi-mcp/job-x"})

	if _, err := r.Cancel(rec.JobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	r.waitJob(rec.JobID)
	if fp.callCount() != 1 {
		t.Fatalf("write job must prune once, got %d", fp.callCount())
	}
	if fp.calls[0].Worktree != "/wt" || fp.calls[0].Branch != "pi-mcp/job-x" {
		t.Fatalf("prune called with wrong args: %+v", fp.calls[0])
	}
}

func TestCancelQueuedJobAbortsWithoutLaunch(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("s")
	r := NewRegistry(Config{Cap: 1, PersistPath: filepath.Join(dir, "r.json")},
		fl, &fakeCorrelator{}, &fakePruner{})

	a, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	b, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"}) // queued

	if got, _ := r.Lookup(b.JobID); got.Status != model.JobQueued {
		t.Fatalf("precondition: b should be queued, got %q", got.Status)
	}
	out, err := r.Cancel(b.JobID)
	if err != nil {
		t.Fatalf("Cancel queued: %v", err)
	}
	if out.Status != model.JobAborted {
		t.Fatalf("expected aborted, got %q", out.Status)
	}
	// Cancelling a queued job must NOT have launched it.
	if fl.count() != 1 {
		t.Fatalf("only job a should have launched, got %d", fl.count())
	}

	fl.release(0)
	r.waitJob(a.JobID)
}

func TestCancelTerminalIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("s")
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")},
		fl, &fakeCorrelator{}, &fakePruner{})

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	fl.release(0)
	r.waitJob(rec.JobID)
	// Now completed; cancel must be a no-op returning the terminal status.
	out, err := r.Cancel(rec.JobID)
	if err != nil {
		t.Fatalf("Cancel terminal: %v", err)
	}
	if out.Status != model.JobCompleted {
		t.Fatalf("expected completed unchanged, got %q", out.Status)
	}
}

func TestCancelUnknownJobErrors(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")},
		newFakeLauncher("s"), &fakeCorrelator{}, &fakePruner{})
	if _, err := r.Cancel("does-not-exist"); err == nil {
		t.Fatal("expected error cancelling unknown job")
	}
}

var _ = time.Second
