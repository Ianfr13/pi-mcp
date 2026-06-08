package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
	if pidAlive(0) {
		t.Fatal("pid 0 should not be considered alive")
	}
	// PID that almost certainly does not exist.
	if pidAlive(2147483646) {
		t.Fatal("absurd pid should not be alive")
	}
}

func TestEffectiveStatusTerminalUnchanged(t *testing.T) {
	now := time.Unix(10_000, 0)
	for _, st := range []model.JobStatus{model.JobCompleted, model.JobFailed, model.JobAborted} {
		got := effectiveStatus(model.JobRecord{Status: st, PID: 999999}, time.Time{}, false, now)
		if got != st {
			t.Fatalf("terminal %q should be unchanged, got %q", st, got)
		}
	}
}

func TestEffectiveStatusStaleRunningBecomesFailed(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rec := model.JobRecord{Status: model.JobRunning, PID: os.Getpid()}
	// updatedAt older than StaleThreshold -> failed, even though PID is alive.
	stale := now.Add(-config.StaleThreshold - time.Second)
	got := effectiveStatus(rec, stale, true, now)
	if got != model.JobFailed {
		t.Fatalf("stale running should be failed, got %q", got)
	}
}

func TestEffectiveStatusFreshRunningPidAliveStaysRunning(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rec := model.JobRecord{Status: model.JobRunning, PID: os.Getpid()}
	fresh := now.Add(-time.Second)
	got := effectiveStatus(rec, fresh, true, now)
	if got != model.JobRunning {
		t.Fatalf("fresh running w/ live pid should stay running, got %q", got)
	}
}

func TestEffectiveStatusSameSessionDeadPidBecomesFailed(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rec := model.JobRecord{Status: model.JobRunning, PID: 2147483646}
	fresh := now.Add(-time.Second)
	// sameSession=true, pid dead -> failed even when not stale.
	got := effectiveStatus(rec, fresh, true, now)
	if got != model.JobFailed {
		t.Fatalf("same-session dead pid should be failed, got %q", got)
	}
}

func TestEffectiveStatusPostRestartNoPidCheckUsesStalenessOnly(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	rec := model.JobRecord{Status: model.JobRunning, PID: 2147483646}
	fresh := now.Add(-time.Second)
	// sameSession=false (post-restart): pid is NOT trusted; fresh -> still running.
	got := effectiveStatus(rec, fresh, false, now)
	if got != model.JobRunning {
		t.Fatalf("post-restart fresh running should stay running, got %q", got)
	}
}

func TestStatusOfAppliesStalenessOverride(t *testing.T) {
	dir := t.TempDir()
	clock := time.Unix(2_000_000, 0)
	r := mustRegistry(t, Config{Cap: 4, PersistPath: filepath.Join(dir, "registry.db"),
		Now: func() time.Time { return clock }}, newFakeLauncher("s"),
		&fakeCorrelator{}, &fakePruner{})

	// Inject a running job with a stale updatedAt directly.
	j := NewJob(Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	j.Record.Status = model.JobRunning
	j.Record.PID = os.Getpid()
	j.updatedAt = clock.Add(-config.StaleThreshold - time.Minute)
	r.jobs[j.Record.JobID] = j

	st := r.StatusOf(j.Record.JobID, true)
	if st != model.JobFailed {
		t.Fatalf("stale running should surface as failed, got %q", st)
	}
}
