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

// --- Fix #5: terminal job status must surface even when the run file is absent ---

func TestStatus_TerminalJobInBlindWindowReportsTerminal(t *testing.T) {
	// An aborted job whose run file never appeared must report "aborted" with the
	// real error code, NOT a misleading running/blind_window.
	j := newFakeJobs()
	j.lookup["job-ab"] = model.JobRecord{
		JobID: "job-ab", RunsDir: "/runs", RunID: "", Status: model.JobAborted,
		ErrorCode: config.ErrWorkflowAborted, PID: 4242,
	}
	store := newFakeStore() // empty -> Load returns ErrRunNotFound
	srv := New(j, store)

	_, out, err := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-ab"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out.Status != "aborted" {
		t.Fatalf("terminal job with no run file must report aborted, got %q", out.Status)
	}
	if out.BlindWindow {
		t.Fatalf("a terminal job must not report blind_window")
	}
	if out.Error != config.ErrWorkflowAborted {
		t.Fatalf("want WORKFLOW_ABORTED, got %q", out.Error)
	}
}

func TestStatus_CompletedJobInBlindWindowHasNoError(t *testing.T) {
	// A completed job whose correlation never resolved (RunID empty) must report
	// "completed" with no error — failureMessage must NOT run for completed.
	j := newFakeJobs()
	j.lookup["job-cc"] = model.JobRecord{
		JobID: "job-cc", RunsDir: "/runs", RunID: "", Status: model.JobCompleted, PID: 4242,
	}
	store := newFakeStore()
	srv := New(j, store)

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-cc"})
	if out.Status != "completed" {
		t.Fatalf("want completed, got %q", out.Status)
	}
	if out.BlindWindow {
		t.Fatalf("completed job must not report blind_window")
	}
	if out.Error != "" {
		t.Fatalf("completed job must carry no error, got %q", out.Error)
	}
}

func TestStatus_RunningJobInBlindWindowStillBlind(t *testing.T) {
	// Regression guard: a genuinely running job with no run file keeps the
	// running/blind_window behavior (fix #5 must not over-fire).
	j := newFakeJobs()
	j.lookup["job-bw"] = model.JobRecord{
		JobID: "job-bw", RunsDir: "/runs", RunID: "", Status: model.JobRunning, PID: 4242,
	}
	store := newFakeStore()
	srv := New(j, store)

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-bw"})
	if out.Status != "running" || !out.BlindWindow {
		t.Fatalf("running job with no run file must stay running/blind, got %+v", out)
	}
}

func TestStatus_FailureMessageFallsBackToErrorCode(t *testing.T) {
	// With a run file present but no #1 ErrorMessage / #2 agent error / #3 logs,
	// the error must fall back to the record's ErrorCode, not UNKNOWN.
	run := buildRun()
	run.Status = "aborted"
	run.Result = nil
	run.Logs = nil
	for i := range run.Agents {
		run.Agents[i].Status = "done"
		run.Agents[i].Error = nil
	}
	j := newFakeJobs()
	j.lookup["job-ec"] = model.JobRecord{
		JobID: "job-ec", RunsDir: "/runs", RunID: run.RunID, PID: 1, Status: model.JobAborted,
		ErrorCode: config.ErrWorkflowAborted, // no ErrorMessage
	}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-ec"})
	if out.Status != "aborted" {
		t.Fatalf("want aborted, got %q", out.Status)
	}
	if out.Error != config.ErrWorkflowAborted {
		t.Fatalf("want errorCode fallback WORKFLOW_ABORTED, got %q", out.Error)
	}
}

// --- Observability: write job with a stale run file but an ACTIVE worktree ---
// (root cause of the d129db4c "no response" incident: pi edits the worktree
// directly, the run file freezes, and staleness falsely flips it to failed.)

func TestStatus_WriteActiveWorktreeNotFalselyFailed(t *testing.T) {
	now := mustTime("2026-06-08T12:00:00Z")
	staleUpd := now.Add(-(config.StaleThreshold + time.Minute)) // run file frozen 6 min ago
	startedAt := now.Add(-15 * time.Minute)                     // job running 15 min
	recentWt := now.Add(-30 * time.Second)                      // worktree touched 30s ago

	run := buildRun()
	run.Status = "paused" // non-terminal disk status, run file not advancing
	run.Result = nil
	run.TokenUsage = nil
	run.UpdatedAt = &staleUpd

	j := newFakeJobs()
	j.lookup["job-wa"] = model.JobRecord{
		JobID: "job-wa", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 4242,
		Status: model.JobRunning, WorktreePath: "/wt/job-wa", Branch: "pi-mcp/job-job-wa",
		StartedAt: startedAt,
	}
	j.activity["job-wa"] = wtActivity{files: 56, lastModified: recentWt, ok: true}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	srv.now = func() time.Time { return now }

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-wa"})
	if out.Status != "running" {
		t.Fatalf("active worktree must keep the job running, got %q (false-failure regression)", out.Status)
	}
	if out.Progress == nil {
		t.Fatalf("expected a progress heartbeat for a running job")
	}
	if out.Progress.WorktreeFiles != 56 {
		t.Fatalf("want 56 worktree files, got %d", out.Progress.WorktreeFiles)
	}
	if out.Progress.ElapsedSeconds != 900 {
		t.Fatalf("want elapsed 900s, got %d", out.Progress.ElapsedSeconds)
	}
	if out.Progress.LastActivitySeconds == nil || *out.Progress.LastActivitySeconds != 30 {
		t.Fatalf("want last_activity 30s, got %v", out.Progress.LastActivitySeconds)
	}
}

// Clock skew (networked FS, container clock drift) can leave the newest worktree
// mtime slightly AHEAD of the pi-mcp host clock. That future mtime is still proof
// of very recent activity, so it must keep the job running (not re-trigger the
// false-failure) and must not surface a negative last_activity_seconds.
func TestStatus_WriteFutureMtimeStaysRunningAndClamps(t *testing.T) {
	now := mustTime("2026-06-08T12:00:00Z")
	staleUpd := now.Add(-(config.StaleThreshold + time.Minute))

	run := buildRun()
	run.Status = "paused"
	run.Result = nil
	run.TokenUsage = nil
	run.UpdatedAt = &staleUpd

	j := newFakeJobs()
	j.lookup["job-fm"] = model.JobRecord{
		JobID: "job-fm", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 4242,
		Status: model.JobRunning, WorktreePath: "/wt/job-fm", Branch: "b", StartedAt: now.Add(-15 * time.Minute),
	}
	// worktree mtime 2s in the FUTURE relative to the host clock (skew).
	j.activity["job-fm"] = wtActivity{files: 10, lastModified: now.Add(2 * time.Second), ok: true}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	srv.now = func() time.Time { return now }

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-fm"})
	if out.Status != "running" {
		t.Fatalf("future-mtime (clock skew) write job must stay running, got %q", out.Status)
	}
	if out.Progress == nil || out.Progress.LastActivitySeconds == nil {
		t.Fatalf("expected progress with last_activity")
	}
	if *out.Progress.LastActivitySeconds != 0 {
		t.Fatalf("last_activity_seconds must clamp to >=0, got %d", *out.Progress.LastActivitySeconds)
	}
}

// A far-future worktree mtime (corrupt timestamp / a restored file dated years
// ahead) is NOT plausible clock skew and must not be trusted as liveness — else a
// genuinely wedged job (stale run file) would read "running" forever. Only a mtime
// within +/- StaleThreshold of now counts as activity.
func TestStatus_WriteFarFutureMtimeDoesNotMaskWedge(t *testing.T) {
	now := mustTime("2026-06-08T12:00:00Z")
	staleUpd := now.Add(-(config.StaleThreshold + time.Minute))

	run := buildRun()
	run.Status = "running"
	run.Result = nil
	run.TokenUsage = nil
	run.UpdatedAt = &staleUpd

	j := newFakeJobs()
	j.lookup["job-ff"] = model.JobRecord{
		JobID: "job-ff", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 1,
		Status: model.JobRunning, WorktreePath: "/wt/job-ff", Branch: "b", StartedAt: now.Add(-20 * time.Minute),
	}
	j.activity["job-ff"] = wtActivity{files: 5, lastModified: now.Add(48 * time.Hour), ok: true} // absurd future
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	srv.now = func() time.Time { return now }

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-ff"})
	if out.Status != "failed" {
		t.Fatalf("far-future mtime must not mask a wedged job, got %q", out.Status)
	}
}

func TestStatus_WriteStaleWorktreeStillFails(t *testing.T) {
	// A write job whose run file is stale AND whose worktree is NOT changing is
	// genuinely wedged/dead -> staleness still applies (no false liveness).
	now := mustTime("2026-06-08T12:00:00Z")
	staleUpd := now.Add(-(config.StaleThreshold + time.Minute))

	run := buildRun()
	run.Status = "running"
	run.Result = nil
	run.TokenUsage = nil
	run.UpdatedAt = &staleUpd

	j := newFakeJobs()
	j.lookup["job-ws"] = model.JobRecord{
		JobID: "job-ws", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 1,
		Status: model.JobRunning, WorktreePath: "/wt/job-ws", Branch: "b", StartedAt: now.Add(-10 * time.Minute),
	}
	// worktree last modified long ago -> not active
	j.activity["job-ws"] = wtActivity{files: 3, lastModified: staleUpd, ok: true}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)
	srv.now = func() time.Time { return now }

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-ws"})
	if out.Status != "failed" {
		t.Fatalf("stale run file + inactive worktree must fail, got %q", out.Status)
	}
}

func TestStatus_BlindWindowHasElapsedHeartbeat(t *testing.T) {
	now := mustTime("2026-06-08T12:00:00Z")
	j := newFakeJobs()
	j.lookup["job-bh"] = model.JobRecord{
		JobID: "job-bh", RunsDir: "/runs", RunID: "", Status: model.JobRunning, PID: 4242,
		StartedAt: now.Add(-9 * time.Minute), // long authoring blind window
	}
	store := newFakeStore() // empty -> blind window
	srv := New(j, store)
	srv.now = func() time.Time { return now }

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-bh"})
	if out.Status != "running" || !out.BlindWindow {
		t.Fatalf("want running/blind, got %+v", out)
	}
	if out.Progress == nil || out.Progress.ElapsedSeconds != 540 {
		t.Fatalf("blind window must carry an elapsed heartbeat (540s), got %+v", out.Progress)
	}
}

// --- Fix #3: pi_status must not stage/diff a still-running write worktree ---

func TestStatus_WriteRunningDoesNotStage(t *testing.T) {
	run := buildRun()
	run.Status = "running" // disk status running -> process still writing
	run.Result = nil
	run.TokenUsage = nil
	j := newFakeJobs()
	j.lookup["job-wr"] = model.JobRecord{
		JobID: "job-wr", RunsDir: "/runs", RunID: run.RunID, Mode: model.ModeWrite, PID: 1, Status: model.JobRunning,
		WorktreePath: "/wt/job-wr", Branch: "pi-mcp/job-job-wr",
	}
	// WriteInfoFor is wired but must NOT be consulted while running (it stages git add -A).
	j.writeInfo["job-wr"] = model.WriteInfo{
		Branch: "pi-mcp/job-job-wr", WorktreePath: "/wt/job-wr",
		DiffStat: "SHOULD-NOT-APPEAR", FilesChanged: []string{"x.go"},
	}
	store := newFakeStore()
	store.runs["/runs/"+run.RunID] = run
	srv := New(j, store)

	_, out, _ := srv.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "job-wr"})
	if out.Status != "running" {
		t.Fatalf("want running, got %q", out.Status)
	}
	if out.Write == nil {
		t.Fatalf("running write job should still surface branch/worktree")
	}
	if out.Write.Branch != "pi-mcp/job-job-wr" || out.Write.WorktreePath != "/wt/job-wr" {
		t.Fatalf("branch/worktree wrong: %+v", out.Write)
	}
	if out.Write.DiffStat != "" || len(out.Write.FilesChanged) != 0 {
		t.Fatalf("running write job must NOT be staged/diffed: %+v", out.Write)
	}
}
