package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

func writeWorktreeDir(t *testing.T, root, jobID string) string {
	t.Helper()
	p := filepath.Join(root, config.WorktreeSubdir, "job-"+jobID)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	return p
}

func TestReconcileMarksStaleRunningFailed(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.json")
	clock := time.Unix(5_000_000, 0)

	// Persist a running job whose StartedAt is far in the past (post-restart, no
	// live updatedAt, so reconcile compares against StartedAt).
	stale := clock.Add(-config.StaleThreshold - time.Hour)
	prior := []model.JobRecord{
		{JobID: "old", Status: model.JobRunning, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", PID: 2147483646, StartedAt: stale},
	}
	if err := persist(persistPath, prior); err != nil {
		t.Fatalf("seed persist: %v", err)
	}

	fp := &fakePruner{}
	r := NewRegistry(Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"),
		&fakeCorrelator{}, fp)

	n, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected reconcile count 1, got %d", n)
	}
	got, ok := r.Lookup("old")
	if !ok {
		t.Fatal("reconciled job missing")
	}
	if got.Status != model.JobFailed {
		t.Fatalf("stale running should reconcile to failed, got %q", got.Status)
	}
	// Persisted file updated too.
	recs, _ := loadPersisted(persistPath)
	if recs[0].Status != model.JobFailed {
		t.Fatalf("persisted reconciled status wrong: %q", recs[0].Status)
	}
}

func TestReconcileKeepsTerminalRecords(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.json")
	prior := []model.JobRecord{
		{JobID: "done", Status: model.JobCompleted, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", StartedAt: time.Unix(1, 0)},
	}
	_ = persist(persistPath, prior)
	r := NewRegistry(Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return time.Unix(9_000_000, 0) }},
		newFakeLauncher("s"), &fakeCorrelator{}, &fakePruner{})
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := r.Lookup("done")
	if got.Status != model.JobCompleted {
		t.Fatalf("terminal record must be preserved, got %q", got.Status)
	}
}

func TestReconcileGCsOrphanWorktrees(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.json")

	// Worktree on disk for a job that has NO record at all -> orphan -> pruned.
	orphan := writeWorktreeDir(t, dir, "ghost")
	// Worktree for a job with a TERMINAL record -> also an orphan -> pruned.
	termWT := writeWorktreeDir(t, dir, "donewrite")
	// Worktree for a LIVE (running, fresh) record -> kept (not pruned).
	liveWT := writeWorktreeDir(t, dir, "alive")

	clock := time.Unix(7_000_000, 0)
	prior := []model.JobRecord{
		{JobID: "donewrite", Status: model.JobCompleted, Mode: model.ModeWrite,
			WorktreePath: termWT, Branch: "pi-mcp/job-donewrite", StartedAt: time.Unix(1, 0)},
		{JobID: "alive", Status: model.JobRunning, Mode: model.ModeWrite,
			WorktreePath: liveWT, Branch: "pi-mcp/job-alive",
			StartedAt: clock.Add(-time.Second)}, // fresh -> stays running
	}
	_ = persist(persistPath, prior)

	fp := &fakePruner{}
	r := NewRegistry(Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"),
		&fakeCorrelator{}, fp)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// ghost + donewrite pruned; alive kept.
	pruned := map[string]bool{}
	for _, c := range fp.calls {
		pruned[c.Branch] = true
	}
	if !pruned["pi-mcp/job-ghost"] {
		t.Errorf("orphan ghost worktree should be pruned; calls=%+v", fp.calls)
	}
	if !pruned["pi-mcp/job-donewrite"] {
		t.Errorf("terminal-job worktree should be pruned; calls=%+v", fp.calls)
	}
	if pruned["pi-mcp/job-alive"] {
		t.Errorf("live job worktree must NOT be pruned; calls=%+v", fp.calls)
	}

	// Sanity: orphan dir actually existed.
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan dir vanished unexpectedly: %v", err)
	}
}
