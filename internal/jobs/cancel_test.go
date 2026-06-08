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
	r := mustRegistry(t, Config{Cap: 4, PersistPath: filepath.Join(dir, "registry.db")},
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
	r := mustRegistry(t, Config{Cap: 4, PersistPath: filepath.Join(dir, "registry.db")},
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
	r := mustRegistry(t, Config{Cap: 1, PersistPath: filepath.Join(dir, "registry.db")},
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
	r := mustRegistry(t, Config{Cap: 4, PersistPath: filepath.Join(dir, "registry.db")},
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

// FIX #1 (B): a clean-exit finish(JobCompleted) must NOT overwrite an 'aborted'
// status that Cancel set. The launcher's wait() returns nil even after the kill,
// so the wait-goroutine calls finish(JobCompleted); the compare-and-set guard in
// finish() must preserve aborted. The freed slot must still let a new job run.
func TestFinishDoesNotOverwriteAbortedOnCleanExit(t *testing.T) {
	dir := t.TempDir()
	cl := newCleanExitLauncher()
	r := mustRegistry(t, Config{Cap: 1, PersistPath: filepath.Join(dir, "registry.db")},
		cl, &fakeCorrelator{}, &fakePruner{})

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})

	// Cancel sets aborted (and cancels ctx, but wait() ignores ctx here).
	if out, err := r.Cancel(rec.JobID); err != nil || out.Status != model.JobAborted {
		t.Fatalf("Cancel: out=%q err=%v", out.Status, err)
	}
	// Let the process "exit cleanly" so the wait-goroutine runs finish(JobCompleted).
	cl.releaseAll()
	r.waitJob(rec.JobID)

	if got, _ := r.Lookup(rec.JobID); got.Status != model.JobAborted {
		t.Fatalf("clean exit must not overwrite aborted, got %q", got.Status)
	}

	// The slot must have been freed: a fresh Submit can run.
	fl := newFakeLauncher("s2")
	r.launcher = fl
	rec2, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	if got, _ := r.Lookup(rec2.JobID); got.Status != model.JobRunning {
		t.Fatalf("expected second job running after slot freed, got %q", got.Status)
	}
	fl.release(0)
	r.waitJob(rec2.JobID)
}

// FIX #1 (A): after a job is admitted running, j.cancel must be non-nil (the
// launch ctx/cancel are installed atomically with the running mark, so the race
// where running coexists with a nil cancel cannot occur).
func TestAdmittedRunningJobHasNonNilCancel(t *testing.T) {
	dir := t.TempDir()
	bl := newBlockingWaitLauncher()
	r := mustRegistry(t, Config{Cap: 1, PersistPath: filepath.Join(dir, "registry.db")},
		bl, &fakeCorrelator{}, &fakePruner{})

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})

	r.mu.Lock()
	j := r.jobs[rec.JobID]
	status := j.Record.Status
	hasCancel := j.cancel != nil
	hasCtx := j.ctx != nil
	r.mu.Unlock()

	if status != model.JobRunning {
		t.Fatalf("expected running, got %q", status)
	}
	if !hasCancel || !hasCtx {
		t.Fatalf("running job must have ctx+cancel installed: ctx=%v cancel=%v", hasCtx, hasCancel)
	}

	bl.releaseAll()
	r.waitJob(rec.JobID)
}

// A reconcile-recovered running WRITE job has no live process we own (cancel==nil),
// its done channel is already closed, and it holds no slot — so finish() will never
// run for it. Cancelling it must still prune the worktree synchronously (for jobs we
// launched, that prune was relocated into finish()). Regression guard for the #2
// prune relocation: pre-relocation Cancel pruned recovered write jobs too.
func TestCancelRecoveredRunningWriteJobPrunes(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")
	clock := time.Unix(5_000_000, 0)
	fresh := clock.Add(-time.Second) // within StaleThreshold -> recovered as running

	prior := []model.JobRecord{
		{JobID: "rec-w", Status: model.JobRunning, Mode: model.ModeWrite,
			CWD: "/wt", RunsDir: "/wt/runs", WorktreePath: "/wt", Branch: "pi-mcp/job-rec-w",
			StartedAt: fresh},
	}
	seedDB(t, persistPath, prior, 0, "")

	fp := &fakePruner{}
	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"), &fakeCorrelator{}, fp)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got, _ := r.Lookup("rec-w"); got.Status != model.JobRunning {
		t.Fatalf("precondition: recovered job should be running, got %q", got.Status)
	}
	if fp.callCount() != 0 {
		t.Fatalf("precondition: reconcile must not prune a live worktree, got %d", fp.callCount())
	}

	out, err := r.Cancel("rec-w")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if out.Status != model.JobAborted {
		t.Fatalf("want aborted, got %q", out.Status)
	}
	if fp.callCount() != 1 {
		t.Fatalf("recovered running write job must prune once on cancel, got %d", fp.callCount())
	}
	if fp.calls[0].Worktree != "/wt" || fp.calls[0].Branch != "pi-mcp/job-rec-w" {
		t.Fatalf("prune args wrong: %+v", fp.calls[0])
	}
}

func TestCancelUnknownJobErrors(t *testing.T) {
	dir := t.TempDir()
	r := mustRegistry(t, Config{Cap: 4, PersistPath: filepath.Join(dir, "registry.db")},
		newFakeLauncher("s"), &fakeCorrelator{}, &fakePruner{})
	if _, err := r.Cancel("does-not-exist"); err == nil {
		t.Fatal("expected error cancelling unknown job")
	}
}

var _ = time.Second
