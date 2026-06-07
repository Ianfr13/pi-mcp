package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	return NewRegistry(Config{
		Cap:         config.DefaultConcurrencyCap,
		PersistPath: filepath.Join(dir, "registry.json"),
		Now:         func() time.Time { return time.Unix(1_000_000, 0) },
	}, newFakeLauncher("sess-x"), &fakeCorrelator{table: map[string]string{}}, &fakePruner{})
}

func TestNewRegistryDefaults(t *testing.T) {
	r := newTestRegistry(t)
	if r.cap != config.DefaultConcurrencyCap {
		t.Fatalf("expected cap %d, got %d", config.DefaultConcurrencyCap, r.cap)
	}
	if cap(r.slots) != config.DefaultConcurrencyCap {
		t.Fatalf("expected %d slots, got %d", config.DefaultConcurrencyCap, cap(r.slots))
	}
	if r.jobs == nil {
		t.Fatal("expected jobs map initialized")
	}
}

func TestNewRegistryCapZeroFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Config{Cap: 0, PersistPath: filepath.Join(dir, "r.json")},
		newFakeLauncher("s"), &fakeCorrelator{}, &fakePruner{})
	if r.cap != config.DefaultConcurrencyCap {
		t.Fatalf("cap 0 should fall back to %d, got %d", config.DefaultConcurrencyCap, r.cap)
	}
}

func TestSubmitRunningCorrelatesRunID(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("sess-A")
	fc := &fakeCorrelator{table: map[string]string{"sess-A": "run-A"}}
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json"),
		Now: func() time.Time { return time.Unix(1_000_000, 0) }}, fl, fc, &fakePruner{})

	rec, err := r.Submit(context.Background(), Spec{Mode: "read", CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if rec.Status != "running" {
		t.Fatalf("expected running, got %q", rec.Status)
	}
	if rec.PID != 4242 {
		t.Fatalf("expected pid 4242, got %d", rec.PID)
	}

	// Correlation happens asynchronously; poll the registry.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, ok := r.Lookup(rec.JobID)
		if !ok {
			t.Fatal("job vanished")
		}
		if got.RunID == "run-A" && got.SessionID == "sess-A" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("correlation never happened: runID=%q sessionID=%q", got.RunID, got.SessionID)
		}
		time.Sleep(5 * time.Millisecond)
	}

	fl.release(0)
	r.waitJob(rec.JobID)
	got, _ := r.Lookup(rec.JobID)
	if got.Status != "completed" {
		t.Fatalf("expected completed after wait, got %q", got.Status)
	}

	// Persisted file reflects the terminal state.
	recs, err := loadPersisted(filepath.Join(dir, "r.json"))
	if err != nil {
		t.Fatalf("loadPersisted: %v", err)
	}
	if len(recs) != 1 || recs[0].Status != "completed" || recs[0].RunID != "run-A" {
		t.Fatalf("persisted state wrong: %+v", recs)
	}
}

func TestSubmitWaitErrorMarksFailed(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("sess-B")
	fl.waitErr = errFake
	fc := &fakeCorrelator{table: map[string]string{"sess-B": "run-B"}}
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")}, fl, fc, &fakePruner{})

	rec, err := r.Submit(context.Background(), Spec{Mode: "read", CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	fl.release(0)
	r.waitJob(rec.JobID)
	got, _ := r.Lookup(rec.JobID)
	if got.Status != "failed" {
		t.Fatalf("expected failed, got %q", got.Status)
	}
	if got.ErrorCode != config.ErrAgentExecutionError {
		t.Fatalf("expected error code %q, got %q", config.ErrAgentExecutionError, got.ErrorCode)
	}
	if got.ErrorMessage != errFake.Error() {
		t.Fatalf("expected wait error text on record, got %q", got.ErrorMessage)
	}
}

func TestLookupByRun(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")},
		newFakeLauncher("s"), &fakeCorrelator{}, &fakePruner{})

	// Inject deterministic records (no goroutines) so we can assert both match
	// branches without racing the async correlate goroutine.
	read := NewJob(Spec{Mode: model.ModeRead, CWD: "/proj", RunsDir: "/proj/runs"})
	read.Record.RunID = "run-R"
	read.Record.Status = model.JobRunning
	write := NewJob(Spec{Mode: model.ModeWrite, CWD: "/wt", RunsDir: "/wt/runs",
		Worktree: "/wt", Branch: "pi-mcp/job-w"})
	write.Record.RunID = "run-W"
	write.Record.Status = model.JobRunning

	r.mu.Lock()
	r.jobs[read.Record.JobID] = read
	r.jobs[write.Record.JobID] = write
	r.mu.Unlock()

	// read job matched by CWD == cwd
	got, ok := r.LookupByRun("run-R", "/proj")
	if !ok || got.JobID != read.Record.JobID {
		t.Fatalf("LookupByRun by cwd failed: ok=%v got=%+v", ok, got)
	}

	// write job matched by WorktreePath == cwd
	got, ok = r.LookupByRun("run-W", "/wt")
	if !ok || got.JobID != write.Record.JobID {
		t.Fatalf("LookupByRun by worktree path failed: ok=%v got=%+v", ok, got)
	}

	// runID matches but neither cwd nor worktree matches -> no hit
	if _, ok := r.LookupByRun("run-R", "/elsewhere"); ok {
		t.Fatal("expected no match when cwd/worktree differ")
	}
	// unknown runID -> no hit
	if _, ok := r.LookupByRun("nope", "/proj"); ok {
		t.Fatal("expected no match for unknown run id")
	}
}

func TestCapQueuesFifthJobAndPromotesFIFO(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("sess") // all share a sessionID; correlation irrelevant here
	fc := &fakeCorrelator{table: map[string]string{}}
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")}, fl, fc, &fakePruner{})

	var ids []string
	for i := 0; i < 5; i++ {
		rec, err := r.Submit(context.Background(), Spec{Mode: "read", CWD: "/p", RunsDir: "/p/runs"})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		ids = append(ids, rec.JobID)
	}

	// First 4 running, 5th queued.
	running := 0
	queued := 0
	for _, id := range ids {
		got, _ := r.Lookup(id)
		switch got.Status {
		case "running":
			running++
		case "queued":
			queued++
		}
	}
	if running != 4 || queued != 1 {
		t.Fatalf("expected 4 running / 1 queued, got %d running / %d queued", running, queued)
	}
	if got, _ := r.Lookup(ids[4]); got.Status != "queued" {
		t.Fatalf("5th job should be queued, got %q", got.Status)
	}

	// Only the 4 running jobs were actually launched.
	if c := fl.count(); c != 4 {
		t.Fatalf("expected 4 launches, got %d", c)
	}

	// Complete job 0 -> its slot promotes the queued job (FIFO = ids[4]).
	fl.release(0)
	r.waitJob(ids[0])

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := r.Lookup(ids[4])
		if got.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued job never promoted, status=%q", got.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The promoted job's Launch happens in start() after finish() marks it
	// running, so poll for the launch count rather than reading it once.
	deadline = time.Now().Add(2 * time.Second)
	for {
		if fl.count() == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 5 launches after promotion, got %d", fl.count())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Drain the rest so the goroutines exit cleanly.
	for i := 1; i < 5; i++ {
		fl.release(i)
	}
	for _, id := range ids {
		r.waitJob(id)
	}
}

func TestCloseCancelsRunningAndFlushes(t *testing.T) {
	dir := t.TempDir()
	fl := newFakeLauncher("sess-C")
	r := NewRegistry(Config{Cap: 4, PersistPath: filepath.Join(dir, "r.json")},
		fl, &fakeCorrelator{}, &fakePruner{})

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})

	// Close cancels the running job's context (best-effort kill) and flushes.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The wait()-goroutine should observe ctx cancellation and finish.
	r.waitJob(rec.JobID)

	// Idempotent.
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// Persisted file exists after flush.
	if _, err := loadPersisted(filepath.Join(dir, "r.json")); err != nil {
		t.Fatalf("loadPersisted after Close: %v", err)
	}
}
