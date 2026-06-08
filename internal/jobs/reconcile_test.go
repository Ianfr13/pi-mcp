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

func TestReconcile_OwnerScoped(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "registry.db")
	// Seed the DB directly: one DEAD-owner running write job, one LIVE-owner (self) running job.
	seed, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	deadWrite := model.JobRecord{JobID: "dead1", Mode: model.ModeWrite, CWD: "/c",
		RunsDir: "/c/r", WorktreePath: "/wt/job-dead1", Branch: "pi-mcp/job-dead1",
		Status: model.JobRunning, StartedAt: timeNowUTC()}
	_ = seed.UpsertJobs([]model.JobRecord{deadWrite}, 999999, "old") // pid 999999 = not alive
	liveRead := model.JobRecord{JobID: "live1", Mode: model.ModeRead, CWD: "/c",
		RunsDir: "/c/r", Status: model.JobRunning, StartedAt: timeNowUTC()}
	_ = seed.UpsertJobs([]model.JobRecord{liveRead}, os.Getpid(), "self") // self pid = alive
	seed.Close()

	pruner := &fakePruner{}
	r, err := NewRegistry(Config{Cap: 4, PersistPath: dbPath, WorktreeRoot: dir}, newFakeLauncher("s"), &fakeCorrelator{}, pruner)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	recs := readBackJobs(t, dbPath)
	m := map[string]model.JobRecord{}
	for _, x := range recs {
		m[x.JobID] = x
	}
	if string(m["dead1"].Status) != "failed" {
		t.Errorf("dead-owner job: status=%q want failed", m["dead1"].Status)
	}
	if string(m["live1"].Status) != "running" {
		t.Errorf("live-owner job MUST be untouched: status=%q want running", m["live1"].Status)
	}
	// dead-owner write job's worktree IS pruned; live owner's is NOT.
	if !pruner.prunedBranch("pi-mcp/job-dead1") {
		t.Errorf("dead-owner worktree should be pruned; pruned=%v", pruner.branches())
	}
}

// seedDB seeds records into a SQLite DB at dbPath (creates it), using ownerPid
// and ownerStartedAt for the ownership stamp. Use ownerPid=0 for "dead owner".
func seedDB(t *testing.T, dbPath string, recs []model.JobRecord, ownerPid int, ownerStartedAt string) {
	t.Helper()
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("seedDB OpenStore: %v", err)
	}
	defer s.Close()
	if err := s.UpsertJobs(recs, ownerPid, ownerStartedAt); err != nil {
		t.Fatalf("seedDB UpsertJobs: %v", err)
	}
}

func writeWorktreeDir(t *testing.T, root, jobID string) string {
	t.Helper()
	p := filepath.Join(root, config.WorktreeSubdir, "job-"+jobID)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	return p
}

// TestReconcileMarksStaleRunningFailed: a dead-owner running job is claimed
// terminal (failed/SERVER_RESTARTED) in the DB by owner-scoped reconcile.
// The staleness of the job is irrelevant — dead owner is enough.
func TestReconcileMarksStaleRunningFailed(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")
	clock := time.Unix(5_000_000, 0)

	// Seed a running job with a dead owner (pid=0). Owner-scoped reconcile will
	// claim it terminal regardless of start time.
	stale := clock.Add(-config.StaleThreshold - time.Hour)
	prior := []model.JobRecord{
		{JobID: "old", Status: model.JobRunning, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", PID: 2147483646, StartedAt: stale},
	}
	seedDB(t, persistPath, prior, 0, "") // ownerPid=0 -> dead owner

	fp := &fakePruner{}
	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"),
		&fakeCorrelator{}, fp)

	n, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected reconcile count 1, got %d", n)
	}
	// Persisted DB updated to failed.
	recs := readBackJobs(t, persistPath)
	if len(recs) != 1 || recs[0].Status != model.JobFailed {
		t.Fatalf("persisted reconciled status wrong: %+v", recs)
	}
}

// TestReconcileKeepsTerminalRecords: a dead-owner terminal job is never touched
// by owner-scoped reconcile (terminal rows are always skipped).
func TestReconcileKeepsTerminalRecords(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")
	prior := []model.JobRecord{
		{JobID: "done", Status: model.JobCompleted, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", StartedAt: time.Unix(1, 0)},
	}
	seedDB(t, persistPath, prior, 0, "") // ownerPid=0 -> dead, but terminal -> skipped
	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return time.Unix(9_000_000, 0) }},
		newFakeLauncher("s"), &fakeCorrelator{}, &fakePruner{})
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Terminal record must stay completed in the DB.
	recs := readBackJobs(t, persistPath)
	if len(recs) != 1 || recs[0].Status != model.JobCompleted {
		t.Fatalf("terminal record must be preserved, got %+v", recs)
	}
}

// TestReconcileTerminalizesDeadOwnerNonTerminal: owner-scoped reconcile claims
// both a dead-owner running job AND a dead-owner queued job as failed
// (SERVER_RESTARTED). There is no correlation resume in the new reconcile
// (that was part of the old in-memory-load path which is now removed).
func TestReconcileTerminalizesDeadOwnerNonTerminal(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")
	clock := time.Unix(5_000_000, 0)
	fresh := clock.Add(-time.Second)

	prior := []model.JobRecord{
		{JobID: "run1", Status: model.JobRunning, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", SessionID: "sess-1", RunID: "",
			StartedAt: fresh},
		{JobID: "q1", Status: model.JobQueued, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", StartedAt: fresh},
	}
	seedDB(t, persistPath, prior, 0, "") // ownerPid=0 -> dead owner

	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"),
		&fakeCorrelator{table: map[string]string{"sess-1": "run-resolved"}}, &fakePruner{})

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	recs := readBackJobs(t, persistPath)
	m := map[string]model.JobRecord{}
	for _, x := range recs {
		m[x.JobID] = x
	}
	if m["run1"].Status != model.JobFailed {
		t.Errorf("dead-owner running job should be failed, got %q", m["run1"].Status)
	}
	if m["q1"].Status != model.JobFailed {
		t.Errorf("dead-owner queued job should be failed, got %q", m["q1"].Status)
	}
}

// TestReconcileDeadOwnerClaimsAllNonTerminal: owner-scoped reconcile claims all
// dead-owner non-terminal jobs as failed; the correlator is never consulted
// (reconcile no longer does in-memory correlation). Existing field values like
// RunID are preserved in DB since ClaimTerminal only updates status/errorCode.
func TestReconcileDeadOwnerClaimsAllNonTerminal(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")
	clock := time.Unix(5_000_000, 0)
	fresh := clock.Add(-time.Second)

	prior := []model.JobRecord{
		{JobID: "has-run", Status: model.JobRunning, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", SessionID: "sess-a", RunID: "already",
			StartedAt: fresh},
		{JobID: "no-match", Status: model.JobRunning, Mode: model.ModeRead,
			CWD: "/p", RunsDir: "/p/runs", SessionID: "sess-b", RunID: "",
			StartedAt: fresh},
	}
	seedDB(t, persistPath, prior, 0, "") // ownerPid=0 -> dead owner

	fc := &fakeCorrelator{table: map[string]string{"sess-a": "should-not-apply"}}
	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"), fc, &fakePruner{})

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	recs := readBackJobs(t, persistPath)
	m := map[string]model.JobRecord{}
	for _, x := range recs {
		m[x.JobID] = x
	}
	// Both dead-owner running jobs are claimed failed.
	if m["has-run"].Status != model.JobFailed {
		t.Errorf("has-run: want failed, got %q", m["has-run"].Status)
	}
	if m["no-match"].Status != model.JobFailed {
		t.Errorf("no-match: want failed, got %q", m["no-match"].Status)
	}
}

// TestReconcileDeadOwnerWriteWorktreePruned: owner-scoped reconcile prunes the
// worktree of a dead-owner running write job. Terminal dead-owner jobs and
// live-owner jobs are NOT pruned (write mode with no WorktreePath also skipped).
func TestReconcileDeadOwnerWriteWorktreePruned(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")

	termWT := writeWorktreeDir(t, dir, "donewrite")
	deadWT := writeWorktreeDir(t, dir, "deadwrite")
	liveWT := writeWorktreeDir(t, dir, "alive")

	clock := time.Unix(7_000_000, 0)
	// donewrite: terminal, dead owner -> skipped (terminal rows never touched).
	// deadwrite: running, dead owner -> claimed + pruned.
	// alive:     running, live owner (self) -> untouched.
	prior := []model.JobRecord{
		{JobID: "donewrite", Status: model.JobCompleted, Mode: model.ModeWrite,
			WorktreePath: termWT, Branch: "pi-mcp/job-donewrite", StartedAt: time.Unix(1, 0)},
		{JobID: "deadwrite", Status: model.JobRunning, Mode: model.ModeWrite,
			WorktreePath: deadWT, Branch: "pi-mcp/job-deadwrite",
			StartedAt: clock.Add(-time.Second)},
		{JobID: "alive", Status: model.JobRunning, Mode: model.ModeWrite,
			WorktreePath: liveWT, Branch: "pi-mcp/job-alive",
			StartedAt: clock.Add(-time.Second)},
	}
	// donewrite + deadwrite owned by dead pid; alive owned by self (live).
	seed, err := OpenStore(persistPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = seed.UpsertJobs(prior[:2], 0, "") // dead owner
	_ = seed.UpsertJobs(prior[2:], os.Getpid(), "self")
	seed.Close()

	fp := &fakePruner{}
	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"),
		&fakeCorrelator{}, fp)
	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	pruned := map[string]bool{}
	for _, c := range fp.calls {
		pruned[c.Branch] = true
	}
	// Only the dead-owner non-terminal write job is pruned.
	if !pruned["pi-mcp/job-deadwrite"] {
		t.Errorf("dead-owner write job worktree should be pruned; calls=%+v", fp.calls)
	}
	if pruned["pi-mcp/job-donewrite"] {
		t.Errorf("terminal job worktree must NOT be pruned; calls=%+v", fp.calls)
	}
	if pruned["pi-mcp/job-alive"] {
		t.Errorf("live-owner job worktree must NOT be pruned; calls=%+v", fp.calls)
	}
}
