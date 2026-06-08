package dashboard

import (
	"os"
	"testing"
	"time"

	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
)

// nowFresh is close to the fixtures' updatedAt (2026-06-07T16:51:55Z) so running
// fixtures are not stale.
var nowFresh = time.Date(2026, 6, 7, 16, 52, 0, 0, time.UTC)

func recsForTest() []model.JobRecord {
	st := mustTime("2026-06-07T16:51:33.041Z")
	return []model.JobRecord{
		{JobID: "job-completed", RunID: "completed", Mode: model.ModeRead, CWD: "/tmp/proj", RunsDir: "testdata", PID: 111, Status: model.JobCompleted, StartedAt: st},
		{JobID: "job-running", RunID: "running", Mode: model.ModeRead, CWD: "/tmp/proj", RunsDir: "testdata", PID: 222, Status: model.JobRunning, StartedAt: st},
		{JobID: "job-blind", RunID: "", Mode: model.ModeRead, CWD: "/tmp/proj", RunsDir: "testdata", PID: 333, Status: model.JobRunning, StartedAt: nowFresh.Add(-10 * time.Second)},
		{JobID: "job-queued", RunID: "", Mode: model.ModeWrite, CWD: "/tmp/proj", RunsDir: "testdata", WorktreePath: "", Branch: "pi-mcp/job-queued", Status: model.JobQueued, StartedAt: nowFresh},
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func findJob(s DashboardState, id string) *JobSummary {
	for i := range s.Jobs {
		if s.Jobs[i].JobID == id {
			return &s.Jobs[i]
		}
	}
	return nil
}

func TestBuildState_Counts(t *testing.T) {
	s := BuildState(recsForTest(), "/state", nowFresh)
	if s.Counts.Total != 4 {
		t.Errorf("total=%d want 4", s.Counts.Total)
	}
	if s.Counts.Completed != 1 || s.Counts.Running != 2 || s.Counts.Queued != 1 {
		t.Errorf("counts=%+v want completed1 running2 queued1", s.Counts)
	}
}

func TestBuildState_CompletedJob(t *testing.T) {
	s := BuildState(recsForTest(), "/state", nowFresh)
	j := findJob(s, "job-completed")
	if j == nil {
		t.Fatal("job-completed missing")
	}
	if j.Status != "completed" {
		t.Errorf("status=%q want completed", j.Status)
	}
	if j.WorkflowName != "judge_claims" {
		t.Errorf("workflowName=%q", j.WorkflowName)
	}
	if j.LiveTokens != 120469 {
		t.Errorf("liveTokens=%d want 120469", j.LiveTokens)
	}
	if j.Cost == nil || *j.Cost != 0.1463847 {
		t.Errorf("cost=%v want 0.1463847", j.Cost)
	}
	if j.AgentsDone != 4 || j.AgentsTotal != 4 {
		t.Errorf("agents=%d/%d want 4/4", j.AgentsDone, j.AgentsTotal)
	}
	if j.FleetByModel["deepseek/deepseek-v4-flash"] != 3 || j.FleetByModel["openai-codex/gpt-5.5"] != 1 {
		t.Errorf("fleet=%v", j.FleetByModel)
	}
	if j.BlindWindow {
		t.Errorf("completed job must not be blind")
	}
}

func TestBuildState_RunningJob(t *testing.T) {
	s := BuildState(recsForTest(), "/state", nowFresh)
	j := findJob(s, "job-running")
	if j == nil {
		t.Fatal("job-running missing")
	}
	if j.Status != "running" {
		t.Errorf("status=%q want running", j.Status)
	}
	if j.LiveTokens != 63082 {
		t.Errorf("liveTokens=%d want 63082 (31514+0+31568)", j.LiveTokens)
	}
	if j.Cost != nil {
		t.Errorf("running job cost must be nil, got %v", j.Cost)
	}
	if j.AgentsDone != 2 || j.AgentsTotal != 3 {
		t.Errorf("agents=%d/%d want 2/3", j.AgentsDone, j.AgentsTotal)
	}
}

func TestBuildState_BlindWindow(t *testing.T) {
	s := BuildState(recsForTest(), "/state", nowFresh)
	j := findJob(s, "job-blind")
	if j == nil {
		t.Fatal("job-blind missing")
	}
	if j.Status != "running" || !j.BlindWindow {
		t.Errorf("blind job: status=%q blind=%v want running/true", j.Status, j.BlindWindow)
	}
}

func TestBuildState_StaleBlindFails(t *testing.T) {
	recs := recsForTest()
	// job-blind started long ago, no run file, not worktree-active -> failed.
	for i := range recs {
		if recs[i].JobID == "job-blind" {
			recs[i].StartedAt = nowFresh.Add(-2 * 300 * time.Second)
		}
	}
	s := BuildState(recs, "/state", nowFresh)
	if j := findJob(s, "job-blind"); j == nil || j.Status != "failed" {
		t.Errorf("stale blind job should be failed, got %+v", j)
	}
}

func TestBuildState_SortActiveFirst(t *testing.T) {
	s := BuildState(recsForTest(), "/state", nowFresh)
	// First jobs must be non-terminal (running/queued), terminal last.
	seenTerminal := false
	for _, j := range s.Jobs {
		terminal := j.Status == "completed" || j.Status == "failed" || j.Status == "aborted"
		if terminal {
			seenTerminal = true
		} else if seenTerminal {
			t.Errorf("active job after a terminal one: %+v", s.Jobs)
		}
	}
}

func TestBuildDetail_Completed(t *testing.T) {
	recs := recsForTest()
	d, ok := BuildDetail(recs[0], nowFresh) // job-completed
	if !ok {
		t.Fatal("BuildDetail not ok")
	}
	if len(d.Agents) != 4 {
		t.Errorf("agents=%d want 4", len(d.Agents))
	}
	if len(d.Intermediate) != 4 {
		t.Errorf("intermediate=%d want 4", len(d.Intermediate))
	}
	if d.TokenUsage == nil || d.TokenUsage.Total != 120469 {
		t.Errorf("tokenUsage=%v", d.TokenUsage)
	}
	if d.Result == nil {
		t.Errorf("completed detail must carry a result")
	}
	if d.Agents[0].Prompt == "" {
		t.Errorf("agent prompt should be populated")
	}
}

func TestBuildDetail_BlindHasNoRunFile(t *testing.T) {
	recs := recsForTest()
	var blind model.JobRecord
	for _, r := range recs {
		if r.JobID == "job-blind" {
			blind = r
		}
	}
	d, ok := BuildDetail(blind, nowFresh)
	if !ok {
		t.Fatal("blind detail should still build (summary only)")
	}
	if len(d.Agents) != 0 || !d.BlindWindow {
		t.Errorf("blind detail: agents=%d blind=%v", len(d.Agents), d.BlindWindow)
	}
}

func TestBuildDetail_BlindWindowAttachesAuthoring(t *testing.T) {
	dir := t.TempDir()
	// running job, runId empty => blind window (readRun returns fs.ErrNotExist)
	rec := model.JobRecord{JobID: "job-A", RunsDir: dir, Mode: model.ModeRead, Status: model.JobRunning, StartedAt: nowFresh.Add(-5 * time.Second)}
	if err := os.WriteFile(runstore.AuthoringPath(dir, "job-A"),
		[]byte(`{"jobId":"job-A","model":"openai-codex/gpt-5.5","preview":"phase('Recon')","done":false,"updatedAt":"2026-06-08T17:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, ok := BuildDetail(rec, nowFresh)
	if !ok {
		t.Fatal("BuildDetail ok=false")
	}
	if !d.BlindWindow {
		t.Fatalf("expected blindWindow; got status=%q", d.Status)
	}
	if d.Authoring == nil || d.Authoring.Preview != "phase('Recon')" {
		t.Errorf("Authoring = %+v", d.Authoring)
	}
}
