package jobs

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

// lateCorrelator simulates the real-world timing: the run file does not exist
// yet, so RunIDForSession returns not-found for the first failUntil calls, then
// returns runID on every subsequent call (the run file finally landed). This is
// the BUG 1 scenario: a single lookup at the session event always misses.
type lateCorrelator struct {
	failUntil int32  // number of leading not-found responses
	runID     string // returned once the (simulated) run file exists
	calls     int32  // total RunIDForSession calls (atomic)
}

func (c *lateCorrelator) RunIDForSession(runsDir, sessionID string) (string, bool) {
	n := atomic.AddInt32(&c.calls, 1)
	if n <= atomic.LoadInt32(&c.failUntil) {
		return "", false // run file not written yet (blind window)
	}
	return c.runID, true
}

func (c *lateCorrelator) callCount() int { return int(atomic.LoadInt32(&c.calls)) }

// neverCorrelator always reports not-found: the run file never appears. Used to
// prove correlate() stops cleanly when the job ends without resolving (no leak,
// no deadlock).
type neverCorrelator struct {
	mu    sync.Mutex
	calls int
}

func (c *neverCorrelator) RunIDForSession(runsDir, sessionID string) (string, bool) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return "", false
}

// TestCorrelateRetriesUntilRunFileAppears is the BUG 1 regression: a single
// lookup at the session event misses (run file lands ~20-26s later), so
// correlate() must POLL until RunIDForSession resolves and then set + persist
// JobRecord.RunID. Without the retry, RunID stays "" forever and pi_status is
// stuck in the blind window even after pi finished.
func TestCorrelateRetriesUntilRunFileAppears(t *testing.T) {
	// Speed up the poll for the test (default is 500ms).
	old := correlatePollInterval
	correlatePollInterval = 2 * time.Millisecond
	defer func() { correlatePollInterval = old }()

	dir := t.TempDir()
	fl := newFakeLauncher("sess-late")
	// Miss the first 3 lookups (the immediate one + 2 polls), then resolve.
	corr := &lateCorrelator{failUntil: 3, runID: "run-xyz"}
	r := mustRegistry(t, Config{Cap: 1, PersistPath: filepath.Join(dir, "registry.db")},
		fl, corr, &fakePruner{})

	rec, err := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// While the job is still RUNNING (wait() not released), the retry loop must
	// eventually resolve the runId and persist it onto the record.
	deadline := time.Now().Add(3 * time.Second)
	var got model.JobRecord
	for time.Now().Before(deadline) {
		got, _ = r.Lookup(rec.JobID)
		if got.RunID == "run-xyz" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got.RunID != "run-xyz" {
		t.Fatalf("correlate did not retry to resolve RunID while job running; got RunID=%q after %d correlator calls",
			got.RunID, corr.callCount())
	}
	if got.SessionID != "sess-late" {
		t.Fatalf("session id should be pushed immediately; got %q", got.SessionID)
	}
	if got.Status != model.JobRunning {
		t.Fatalf("precondition: job should still be running, got %q", got.Status)
	}
	if corr.callCount() < 2 {
		t.Fatalf("expected MULTIPLE correlator lookups (retry), got %d", corr.callCount())
	}

	// pi_status would now resolve: the record carries a non-empty RunID + RunsDir.
	if got.RunsDir == "" {
		t.Fatalf("RunsDir must be set for pi_status Load")
	}

	// Let the job finish cleanly.
	fl.release(0)
	r.waitJob(rec.JobID)
	final, _ := r.Lookup(rec.JobID)
	if final.Status != model.JobCompleted {
		t.Fatalf("expected completed, got %q", final.Status)
	}
	if final.RunID != "run-xyz" {
		t.Fatalf("RunID must persist through completion, got %q", final.RunID)
	}
}

// TestCorrelateStopsWhenJobEndsWithoutRunFile proves the retry loop terminates
// cleanly when the run file NEVER appears: the job ends (wait() returns ->
// cancel()), correlate() observes ctx.Done(), exits, finish() proceeds, and
// j.done closes. No goroutine leak, no deadlock (run under -race to confirm).
func TestCorrelateStopsWhenJobEndsWithoutRunFile(t *testing.T) {
	old := correlatePollInterval
	correlatePollInterval = 1 * time.Millisecond
	defer func() { correlatePollInterval = old }()

	dir := t.TempDir()
	fl := newFakeLauncher("sess-none")
	corr := &neverCorrelator{}
	r := mustRegistry(t, Config{Cap: 1, PersistPath: filepath.Join(dir, "registry.db")},
		fl, corr, &fakePruner{})

	rec, err := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Let the poll loop spin a few times, then end the job.
	time.Sleep(20 * time.Millisecond)
	fl.release(0)

	// waitJob blocks on j.done; finish() blocks on `correlated`; correlate() must
	// have exited via ctx.Done(). A deadlock here would hang this test.
	done := make(chan struct{})
	go func() {
		r.waitJob(rec.JobID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: correlate() did not stop when the job ended without a run file")
	}

	final, _ := r.Lookup(rec.JobID)
	if final.Status != model.JobCompleted {
		t.Fatalf("expected completed, got %q", final.Status)
	}
	if final.RunID != "" {
		t.Fatalf("RunID must stay empty when the run file never appears, got %q", final.RunID)
	}
	if final.SessionID != "sess-none" {
		t.Fatalf("session id should still be pushed immediately, got %q", final.SessionID)
	}
}

// TestCorrelate_EventWakeResolvesWithoutTick proves the fsnotify wake resolves
// the runId without waiting for the fallback tick: the ticker is 1h, so ONLY
// the injected wake can drive the second lookup.
func TestCorrelate_EventWakeResolvesWithoutTick(t *testing.T) {
	old := correlatePollInterval
	correlatePollInterval = time.Hour
	defer func() { correlatePollInterval = old }()

	dir := t.TempDir()
	fl := newFakeLauncher("sess-evt")
	// First (immediate) lookup misses; the wake-driven lookup resolves.
	corr := &lateCorrelator{failUntil: 1, runID: "run-evt"}
	wake := make(chan struct{}, 1)
	r := mustRegistry(t, Config{
		Cap: 1, PersistPath: filepath.Join(dir, "registry.db"),
		Subscribe: func(string) (<-chan struct{}, func(), error) { return wake, func() {}, nil },
	}, fl, corr, &fakePruner{})

	rec, err := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let correlate consume the sessionId + first lookup
	wake <- struct{}{}                // "the run file just appeared"

	deadline := time.Now().Add(3 * time.Second)
	var got model.JobRecord
	for time.Now().Before(deadline) {
		got, _ = r.Lookup(rec.JobID)
		if got.RunID == "run-evt" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got.RunID != "run-evt" {
		t.Fatalf("event wake did not resolve RunID (ticker is 1h); correlator calls=%d", corr.callCount())
	}
	fl.release(0)
	r.waitJob(rec.JobID)
}
