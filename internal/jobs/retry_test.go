package jobs

import (
	"context"
	"path/filepath"
	"testing"

	"pi-mcp/internal/model"
)

// An authoring failure (pi failed before any run file appeared — the fleet never
// ran, RunID stays "") is retried up to config.MaxAuthoringRetries times. With
// two failures then a success, the job completes after 3 total launches.
func TestAuthoringFailureRetriedThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	sl := &seqLauncher{waitErrs: []error{errFake, errFake, nil}} // fail, fail, succeed
	r := NewRegistry(Config{Cap: 1, PersistPath: filepath.Join(dir, "r.json")},
		sl, &fakeCorrelator{table: map[string]string{}}, &fakePruner{})
	r.hasRunFile = func(string) bool { return false } // authoring failure (no run file) -> retry

	rec, err := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	r.waitJob(rec.JobID)

	got, _ := r.Lookup(rec.JobID)
	if got.Status != model.JobCompleted {
		t.Fatalf("expected completed after retries, got %q", got.Status)
	}
	if sl.count() != 3 {
		t.Fatalf("expected 3 launches (1 + 2 retries), got %d", sl.count())
	}
}

// When all attempts fail at authoring, the job ends failed after exactly
// MaxAuthoringRetries+1 launches, carrying the last wait error.
func TestAuthoringFailureExhaustsRetries(t *testing.T) {
	dir := t.TempDir()
	sl := &seqLauncher{waitErrs: []error{errFake, errFake, errFake, errFake}}
	r := NewRegistry(Config{Cap: 1, PersistPath: filepath.Join(dir, "r.json")},
		sl, &fakeCorrelator{table: map[string]string{}}, &fakePruner{})
	r.hasRunFile = func(string) bool { return false } // never a run file -> retry to the cap

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	r.waitJob(rec.JobID)

	got, _ := r.Lookup(rec.JobID)
	if got.Status != model.JobFailed {
		t.Fatalf("expected failed after exhausting retries, got %q", got.Status)
	}
	if sl.count() != 3 {
		t.Fatalf("expected 3 launches (1 + 2 retries cap), got %d", sl.count())
	}
	if got.ErrorMessage != errFake.Error() {
		t.Fatalf("expected last wait error on record, got %q", got.ErrorMessage)
	}
}

// A failure AFTER a run file exists (the fleet ran) must NOT be retried —
// re-running the fleet would be expensive and wrong.
func TestExecutionFailureNotRetried(t *testing.T) {
	dir := t.TempDir()
	sl := &seqLauncher{waitErrs: []error{errFake, errFake, errFake}}
	r := NewRegistry(Config{Cap: 1, PersistPath: filepath.Join(dir, "r.json")},
		sl, &fakeCorrelator{table: map[string]string{}}, &fakePruner{})
	r.hasRunFile = func(string) bool { return true } // run file present -> execution failure

	rec, _ := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	r.waitJob(rec.JobID)

	got, _ := r.Lookup(rec.JobID)
	if got.Status != model.JobFailed {
		t.Fatalf("want failed, got %q", got.Status)
	}
	if sl.count() != 1 {
		t.Fatalf("execution failure (run file exists) must NOT retry, got %d launches", sl.count())
	}
}
