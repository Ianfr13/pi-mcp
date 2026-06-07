package mcpserver

import (
	"context"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

func ctxBG() context.Context { return context.Background() }

// --- M8: wake-predicate snapshot ---

func TestWakeChanged(t *testing.T) {
	base := snapshot{journalLen: 2, agentsLen: 3, phase: "Scan", terminal: false}

	// identical -> no wake
	if wakeChanged(base, base) {
		t.Fatalf("identical snapshots should not wake")
	}
	// journal grew -> wake
	g := base
	g.journalLen = 3
	if !wakeChanged(base, g) {
		t.Fatalf("journal growth should wake")
	}
	// agents grew -> wake
	g = base
	g.agentsLen = 4
	if !wakeChanged(base, g) {
		t.Fatalf("agents growth should wake")
	}
	// phase changed -> wake
	g = base
	g.phase = "Final"
	if !wakeChanged(base, g) {
		t.Fatalf("phase change should wake")
	}
	// terminal -> wake
	g = base
	g.terminal = true
	if !wakeChanged(base, g) {
		t.Fatalf("terminal should wake")
	}
}

func TestSnapshotOf_IgnoresRunningPausedFlap(t *testing.T) {
	r := buildRun()
	r.Status = "running"
	a := snapshotOf(r)
	r.Status = "paused" // flap; both map to "running" -> not terminal, same phase/lens
	b := snapshotOf(r)
	if wakeChanged(a, b) {
		t.Fatalf("running<->paused flap must NOT wake")
	}
}

func TestSnapshotOf_NilPhase(t *testing.T) {
	r := &model.Run{Status: "running"} // CurrentPhase nil
	s := snapshotOf(r)
	if s.phase != "" {
		t.Fatalf("nil currentPhase -> empty phase, got %q", s.phase)
	}
	if s.terminal {
		t.Fatalf("running is not terminal")
	}
}

// --- M9: pi_status handler ---

func TestStatus_BlindWindow(t *testing.T) {
	j := newFakeJobs()
	j.lookup["job-1"] = model.JobRecord{
		JobID: "job-1", RunsDir: "/runs", RunID: "", Status: model.JobRunning, PID: 4242,
	}
	store := newFakeStore() // empty -> Load returns ErrRunNotFound
	srv := New(j, store)

	_, out, err := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out.Status != "running" || !out.BlindWindow {
		t.Fatalf("blind window expected, got %+v", out)
	}
	if out.RunID != nil {
		t.Fatalf("runId must be null in blind window, got %v", *out.RunID)
	}
	if len(out.Intermediate) != 0 {
		t.Fatalf("no intermediate in blind window")
	}
}

func TestStatus_RunIdPathNonexistentRun_IsQueuedNotError(t *testing.T) {
	j := newFakeJobs() // no job owns it
	store := newFakeStore()
	srv := New(j, store)
	_, out, err := srv.handleStatus(ctxBG(), nil, model.StatusInput{RunID: "ghost", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("runId path for missing run must NOT error, got %v", err)
	}
	if out.Status != "queued" {
		t.Fatalf("missing run via runId -> queued, got %q", out.Status)
	}
	if out.Error == config.ErrNoWorkflowRun {
		t.Fatalf("must NOT be NO_WORKFLOW_RUN")
	}
}

func TestStatus_CompletedReadResultAndIntermediate(t *testing.T) {
	run := buildRun() // completed, journal 0,2,1,3
	j := newFakeJobs()
	j.lookup["job-2"] = model.JobRecord{JobID: "job-2", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeRead, PID: 1, Status: model.JobRunning}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)

	_, out, err := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-2"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out.Status != "completed" {
		t.Fatalf("want completed, got %q", out.Status)
	}
	if out.RunID == nil || *out.RunID != run.RunID {
		t.Fatalf("runId not surfaced")
	}
	if out.Phase == nil || *out.Phase != "Final" {
		t.Fatalf("phase wrong: %v", out.Phase)
	}
	if len(out.Intermediate) != 4 || out.Intermediate[1].Label != "claim C" {
		t.Fatalf("intermediate join wrong: %+v", out.Intermediate)
	}
	if out.Metadata == nil || out.Metadata.ByModel["deepseek/deepseek-v4-flash"] != 3 {
		t.Fatalf("metadata histogram wrong")
	}
	if out.Write != nil {
		t.Fatalf("read mode must not populate write")
	}
	// .result coerced ({claims,overall} -> summary added, originals preserved).
	// out.Result is now `any` (decoded JSON object), not raw bytes.
	res, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not object: %#v", out.Result)
	}
	if _, ok := res["claims"]; !ok {
		t.Fatalf("result lost original keys: %#v", out.Result)
	}
	if _, ok := res["summary"]; !ok {
		t.Fatalf("result missing synthesized summary: %#v", out.Result)
	}
}

func TestStatus_StaleRunningBecomesFailed(t *testing.T) {
	run := buildRun()
	run.Status = "running"
	old := mustTime("2020-01-01T00:00:00Z")
	run.UpdatedAt = &old
	run.Result = nil
	run.TokenUsage = nil
	j := newFakeJobs()
	j.lookup["job-3"] = model.JobRecord{JobID: "job-3", RunsDir: "/runs", RunID: run.RunID, PID: 999, Status: model.JobRunning}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	srv.now = func() time.Time { return mustTime("2026-06-07T00:00:00Z") }

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-3"})
	if out.Status != "failed" {
		t.Fatalf("stale running must map to failed, got %q", out.Status)
	}
}

func TestStatus_WriteInfoPopulated(t *testing.T) {
	run := buildRun()
	j := newFakeJobs()
	j.lookup["job-w"] = model.JobRecord{
		JobID: "job-w", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 1, Status: model.JobRunning,
		WorktreePath: "/wt/job-w", Branch: "pi-mcp/job-job-w",
	}
	j.writeInfo["job-w"] = model.WriteInfo{
		Branch: "pi-mcp/job-job-w", WorktreePath: "/wt/job-w",
		DiffStat: "1 file changed", FilesChanged: []string{"a.go"},
	}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-w"})
	if out.Write == nil || out.Write.Branch != "pi-mcp/job-job-w" || out.Write.WorktreePath != "/wt/job-w" {
		t.Fatalf("write info not populated: %+v", out.Write)
	}
	if out.Write.DiffStat != "1 file changed" || len(out.Write.FilesChanged) != 1 {
		t.Fatalf("write info diff/files not from WriteInfoFor: %+v", out.Write)
	}
}

func TestStatus_WriteInfoFallbackWhenNotReady(t *testing.T) {
	run := buildRun()
	j := newFakeJobs()
	j.lookup["job-wf"] = model.JobRecord{
		JobID: "job-wf", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 1, Status: model.JobRunning,
		WorktreePath: "/wt/job-wf", Branch: "pi-mcp/job-job-wf",
	}
	// no writeInfo entry -> WriteInfoFor returns ok=false -> fall back to record
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-wf"})
	if out.Write == nil || out.Write.Branch != "pi-mcp/job-job-wf" || out.Write.WorktreePath != "/wt/job-wf" {
		t.Fatalf("write fallback not populated: %+v", out.Write)
	}
}

// --- IA8 / §9 failure-message extraction order ---

func TestStatus_FailureMessagePrefersRecordErrorMessage(t *testing.T) {
	run := buildRun()
	run.Status = "failed"
	run.Result = nil
	run.Logs = []string{"run-xyz log line"} // #3
	run.Agents[0].Status = "error"
	em := "agent boom"
	run.Agents[0].Error = &em // #2
	j := newFakeJobs()
	j.lookup["job-f"] = model.JobRecord{
		JobID: "job-f", RunsDir: "/runs", RunID: run.RunID, PID: 1, Status: model.JobFailed,
		ErrorMessage: "WORKFLOW isError text", ErrorCode: config.ErrAgentExecutionError, // #1
	}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-f"})
	if out.Error != "WORKFLOW isError text" {
		t.Fatalf("want #1 record ErrorMessage, got %q", out.Error)
	}
}

func TestStatus_FailureMessageFallsBackToAgentThenLogs(t *testing.T) {
	// #1 absent (record has no ErrorMessage) -> use #2 agent error
	run := buildRun()
	run.Status = "failed"
	run.Result = nil
	run.Logs = []string{"run-xyz log line"}
	em := "agent boom"
	run.Agents[0].Status = "error"
	run.Agents[0].Error = &em
	j := newFakeJobs()
	j.lookup["job-g"] = model.JobRecord{JobID: "job-g", RunsDir: "/runs", RunID: run.RunID, PID: 1, Status: model.JobFailed}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-g"})
	if out.Error != "agent boom" {
		t.Fatalf("want #2 agent error, got %q", out.Error)
	}

	// #1 and #2 absent -> use #3 logs[0] verbatim
	run2 := buildRun()
	run2.Status = "failed"
	run2.Result = nil
	run2.Logs = []string{"run-xyz log line"}
	for i := range run2.Agents {
		run2.Agents[i].Status = "done"
		run2.Agents[i].Error = nil
	}
	j.lookup["job-h"] = model.JobRecord{JobID: "job-h", RunsDir: "/runs", RunID: run2.RunID + "-2", PID: 1, Status: model.JobFailed}
	store.runs["/runs/"+run2.RunID+"-2"] = run2
	_, out2, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-h"})
	if out2.Error != "run-xyz log line" {
		t.Fatalf("want #3 logs[0] verbatim, got %q", out2.Error)
	}
}

// --- M10: long-poll wait ---

func TestStatus_WaitWakesOnJournalGrowth(t *testing.T) {
	// Load #1: running, journal len 1. Load #2+: journal len 2 -> wake.
	r1 := buildRun()
	r1.Status = "running"
	r1.Journal = r1.Journal[:1] // index 0 only
	r1.Result = nil
	r1.TokenUsage = nil

	r2 := buildRun()
	r2.Status = "running"
	r2.Journal = r2.Journal[:2] // index 0,2 -> grew
	r2.Result = nil
	r2.TokenUsage = nil

	store := newFakeStore()
	store.seq = []*model.Run{r1, r2} // successive Loads
	j := newFakeJobs()
	j.lookup["job-lp"] = model.JobRecord{JobID: "job-lp", RunsDir: "/runs", RunID: r1.RunID, PID: 1, Status: model.JobRunning}
	srv := New(j, store)
	srv.pollInterval = time.Millisecond
	srv.waitCap = 2 * time.Second

	_, out, err := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-lp", Wait: true})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out.Status != "running" {
		t.Fatalf("want running, got %q", out.Status)
	}
	// after wake, buildStatus loads again (seq sticks to last) -> 2 intermediate
	if len(out.Intermediate) != 2 {
		t.Fatalf("want 2 intermediate after wake, got %d", len(out.Intermediate))
	}
}

func TestStatus_WaitCapReturnsWithoutChange(t *testing.T) {
	r := buildRun()
	r.Status = "running"
	r.Result = nil
	r.TokenUsage = nil
	store := newFakeStore()
	store.runs["/runs/"+r.RunID] = r // same value every Load -> never wakes
	j := newFakeJobs()
	j.lookup["job-cap"] = model.JobRecord{JobID: "job-cap", RunsDir: "/runs", RunID: r.RunID, PID: 1, Status: model.JobRunning}
	srv := New(j, store)
	srv.pollInterval = time.Millisecond
	srv.waitCap = 20 * time.Millisecond // tiny cap -> returns quickly

	done := make(chan struct{})
	go func() {
		_, _, _ = srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-cap", Wait: true})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("wait did not return at cap")
	}
}
