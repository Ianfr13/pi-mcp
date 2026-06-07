package runstore

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRunsDir lays out a temp runs dir: the canonical completed run, the
// partial running run, plus a .bak and a .tmp that MUST be excluded from the glob.
func writeRunsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFile := func(src, dstName string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(dir, dstName), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", dstName, err)
		}
	}
	copyFile(filepath.Join("testdata", "sample-run.json"), "mq40rdpt-yij9hj.json")
	copyFile(filepath.Join("testdata", "sample-run-partial.json"), "pq71abcd-partial.json")
	// Must be excluded:
	copyFile(filepath.Join("testdata", "sample-run.json"), "ignored.json.bak")
	copyFile(filepath.Join("testdata", "sample-run.json"), "ignored.json.tmp")
	return dir
}

func TestListRuns_SortAndFields(t *testing.T) {
	dir := writeRunsDir(t)
	out, err := ListRuns(dir, 20)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("len(Runs) = %d, want 2 (.bak/.tmp excluded)", len(out.Runs))
	}
	// Both fixtures share updatedAt timestamp; sort is updatedAt desc, ties by
	// runId. "pq71abcd-partial" > "mq40rdpt-yij9hj" lexically, so it comes first.
	if out.Runs[0].RunID != "pq71abcd-partial" {
		t.Errorf("Runs[0].RunID = %q, want pq71abcd-partial (tie broken by runId)", out.Runs[0].RunID)
	}

	// Find the completed run and assert its derived fields.
	var completed *struct {
		idx int
	}
	for i := range out.Runs {
		if out.Runs[i].RunID == "mq40rdpt-yij9hj" {
			completed = &struct{ idx int }{i}
		}
	}
	if completed == nil {
		t.Fatal("completed run not in list")
	}
	c := out.Runs[completed.idx]
	if c.Status != "completed" {
		t.Errorf("completed.Status = %q, want completed", c.Status)
	}
	if c.WorkflowName != "judge_claims" {
		t.Errorf("completed.WorkflowName = %q, want judge_claims", c.WorkflowName)
	}
	if c.AgentCount != 4 {
		t.Errorf("completed.AgentCount = %d, want 4", c.AgentCount)
	}
	if c.ByModel["deepseek/deepseek-v4-flash"] != 3 || c.ByModel["openai-codex/gpt-5.5"] != 1 {
		t.Errorf("completed.ByModel = %v, want {flash:3, gpt-5.5:1}", c.ByModel)
	}
	if c.Cost == nil || *c.Cost != 0.1463847 {
		t.Errorf("completed.Cost = %v, want 0.1463847", c.Cost)
	}
	if c.DurationMs == nil || *c.DurationMs != 22391 {
		t.Errorf("completed.DurationMs = %v, want 22391", c.DurationMs)
	}
	if c.CompletedAt == nil {
		t.Errorf("completed.CompletedAt = nil, want non-nil")
	}

	// Find the partial (running) run: Cost/DurationMs/CompletedAt all nil.
	for i := range out.Runs {
		if out.Runs[i].RunID == "pq71abcd-partial" {
			p := out.Runs[i]
			if p.Status != "running" {
				t.Errorf("partial.Status = %q, want running", p.Status)
			}
			if p.Cost != nil {
				t.Errorf("partial.Cost = %v, want nil", p.Cost)
			}
			if p.DurationMs != nil {
				t.Errorf("partial.DurationMs = %v, want nil", p.DurationMs)
			}
			if p.CompletedAt != nil {
				t.Errorf("partial.CompletedAt = %v, want nil", p.CompletedAt)
			}
			if p.AgentCount != 3 {
				t.Errorf("partial.AgentCount = %d, want 3", p.AgentCount)
			}
		}
	}
}

func TestListRuns_LimitTruncates(t *testing.T) {
	dir := writeRunsDir(t)
	out, err := ListRuns(dir, 1)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(out.Runs) != 1 {
		t.Errorf("len(Runs) = %d, want 1 (limit applied)", len(out.Runs))
	}
}

func TestListRuns_MissingDirEmpty(t *testing.T) {
	out, err := ListRuns(filepath.Join(t.TempDir(), "nope"), 20)
	if err != nil {
		t.Fatalf("ListRuns on missing dir: %v, want nil err + empty list", err)
	}
	if len(out.Runs) != 0 {
		t.Errorf("len(Runs) = %d, want 0", len(out.Runs))
	}
}

func TestListRuns_SkipsCorruptFileWithoutBak(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join("testdata", "sample-run.json"))
	if err := os.WriteFile(filepath.Join(dir, "good.json"), b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := ListRuns(dir, 20)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(out.Runs) != 1 || out.Runs[0].RunID != "mq40rdpt-yij9hj" {
		t.Errorf("Runs = %+v, want only the good run (corrupt-without-.bak skipped)", out.Runs)
	}
}
