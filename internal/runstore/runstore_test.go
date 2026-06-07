package runstore

import (
	"path/filepath"
	"testing"
)

func TestReadRun_Canonical(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if r.RunID != "mq40rdpt-yij9hj" {
		t.Errorf("RunID = %q, want mq40rdpt-yij9hj", r.RunID)
	}
	if r.Status != "completed" {
		t.Errorf("Status = %q, want completed", r.Status)
	}
	if r.WorkflowName != "judge_claims" {
		t.Errorf("WorkflowName = %q, want judge_claims", r.WorkflowName)
	}
	if len(r.Agents) != 4 {
		t.Fatalf("len(Agents) = %d, want 4", len(r.Agents))
	}
	if len(r.Journal) != 4 {
		t.Fatalf("len(Journal) = %d, want 4", len(r.Journal))
	}
	// Journal is in completion order, not array position: 0,2,1,3.
	gotOrder := []int{r.Journal[0].Index, r.Journal[1].Index, r.Journal[2].Index, r.Journal[3].Index}
	want := []int{0, 2, 1, 3}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Errorf("Journal index order[%d] = %d, want %d", i, gotOrder[i], want[i])
		}
	}
	if r.TokenUsage == nil {
		t.Fatal("TokenUsage = nil, want non-nil")
	}
	if r.TokenUsage.Cost != 0.1463847 {
		t.Errorf("Cost = %v, want 0.1463847 (verbatim)", r.TokenUsage.Cost)
	}
	if r.DurationMs == nil || *r.DurationMs != 22391 {
		t.Errorf("DurationMs = %v, want 22391", r.DurationMs)
	}
}

func TestReadRun_PartialOmitsOptionals(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run-partial.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if r.Status != "running" {
		t.Errorf("Status = %q, want running", r.Status)
	}
	if r.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil (omitted)", r.CompletedAt)
	}
	if r.DurationMs != nil {
		t.Errorf("DurationMs = %v, want nil (omitted)", r.DurationMs)
	}
	if r.TokenUsage != nil {
		t.Errorf("TokenUsage = %v, want nil (omitted)", r.TokenUsage)
	}
	if len(r.Result) != 0 {
		t.Errorf("Result = %s, want empty (omitted)", r.Result)
	}
	if r.CurrentPhase == nil || *r.CurrentPhase != "Scan" {
		t.Errorf("CurrentPhase = %v, want Scan", r.CurrentPhase)
	}
}

func TestReadRun_PausedOmitsOptionals(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run-partial-paused.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if r.Status != "paused" {
		t.Errorf("Status = %q, want paused", r.Status)
	}
	if r.CompletedAt != nil || r.DurationMs != nil || r.TokenUsage != nil {
		t.Errorf("paused run must omit completedAt/durationMs/tokenUsage: %+v", r)
	}
	if len(r.Result) != 0 {
		t.Errorf("Result = %s, want empty (omitted)", r.Result)
	}
}

func TestReadRun_CorruptFallsBackToBak(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run-corrupt.json"))
	if err != nil {
		t.Fatalf("ReadRun (expected .bak fallback): %v", err)
	}
	if r.RunID != "corrupt-recovered" {
		t.Errorf("RunID = %q, want corrupt-recovered (from .bak)", r.RunID)
	}
}

func TestReadRun_CorruptNoBakErrors(t *testing.T) {
	_, err := ReadRun(filepath.Join("testdata", "does-not-exist.json"))
	if err == nil {
		t.Fatal("ReadRun on missing file: err = nil, want error")
	}
}
