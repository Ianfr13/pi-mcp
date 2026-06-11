# Event-Driven Updates + Delta-Based pi_status — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `pi_status` return compact deltas (only agents finished since the caller's previous call) instead of re-dumping all intermediates, and replace the three disk-polling layers with fsnotify events.

**Architecture:** Two independently deployable phases. Phase 1 (Tasks 1–9) is the delta protocol: new `events[]` output with server-side per-jobId delivery tracking, `stalled` status, early-inactivity warning, configurable 5-min WaitCap, transient parse-error grace. No new dependency. Phase 2 (Tasks 10–14) layers fsnotify wakes under the long-poll, the correlate loop, and the dashboard poller, plus a run-file parse cache. Spec: `docs/superpowers/specs/2026-06-11-event-driven-status-delta-design.md`.

**Tech Stack:** Go 1.26 (module `pi-mcp`), `github.com/modelcontextprotocol/go-sdk/mcp`, `github.com/fsnotify/fsnotify` (Phase 2 only), stdlib testing.

**Conventions for this repo:**
- Run all tests with `go test -race -count=1 ./...` from `/root/pi-mcp`. The e2e is gated: it skips unless `PI_MCP_E2E=1`.
- Tests live next to the code (`*_test.go`, same package, no testify — plain `t.Fatalf`).
- mcpserver tests use the fakes in `internal/mcpserver/fakes_test.go` (`newFakeJobs()`, `newFakeStore()`, `strptr`, `i64ptr`, `mustTime`).
- Comments explain WHY and contracts, matching existing density.
- Commit after every task. Commit trailer: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

---

## Phase 1 — Delta protocol (ships alone)

### Task 1: Wire types — `StatusEvent`, input flags, output delta fields

**Files:**
- Modify: `internal/model/model.go` (StatusInput ~line 164, StatusOutput ~line 212, new type after IntermediateResult)

No test-first here (pure type declarations; the compiler is the test). Behavior tests come in Tasks 2–5.

- [ ] **Step 1: Add `StatusEvent` and extend `StatusInput`/`StatusOutput`**

In `internal/model/model.go`, change `StatusInput` to:

```go
type StatusInput struct {
	JobID string `json:"jobId,omitempty" jsonschema:"job id from pi_workflow"`
	RunID string `json:"runId,omitempty" jsonschema:"alternative: external/post-restart run id (requires cwd)"`
	CWD   string `json:"cwd,omitempty" jsonschema:"required when querying by runId; resolves the runs dir"`
	Wait  bool   `json:"wait,omitempty" jsonschema:"long-poll until a terminal/new-agent/new-phase/inactivity change (cap 5min, env PI_MCP_WAIT_CAP)"`
	// FromStart resets the server-side delta position: re-deliver every event
	// from journal position 0. There is NO client-managed cursor — the server
	// tracks delivery per jobId (stdio server == one process per session).
	FromStart bool `json:"from_start,omitempty" jsonschema:"re-deliver all events from the beginning (resets the server-side delta position)"`
	// IncludeResults attaches each NEW event's full result (16KB truncation)
	// to this response. Default events carry label/model/phase/status only.
	IncludeResults bool `json:"include_results,omitempty" jsonschema:"attach the full result of each NEW event in this response (truncated at 16KB); default is label+status only"`
}
```

After the `IntermediateResult` type, add:

```go
// StatusEvent is one delta row: an agent whose journal entry landed since the
// caller's previous pi_status call. Result/Preview are attached only when
// include_results=true (Result is `any`, not json.RawMessage, for the same
// go-sdk output-schema reason as IntermediateResult.Result).
type StatusEvent struct {
	Label     string `json:"label"`
	Model     string `json:"model"`
	Phase     string `json:"phase"`
	Status    string `json:"status"` // ok|error
	Error     string `json:"error,omitempty"`
	Result    any    `json:"result,omitempty" jsonschema:"full agent result as arbitrary JSON; only when include_results=true"`
	Preview   string `json:"resultPreview,omitempty"` // set instead of Result when truncated
	Truncated bool   `json:"truncated,omitempty"`
}
```

In `StatusOutput`: ADD the three new fields and KEEP `Intermediate` for now — it is deleted in Task 4 together with the handler wiring and every test that references it, so the tree BUILDS AT EVERY COMMIT (review finding: no broken intermediate commits). Update the `Status` comment vocabulary:

```go
type StatusOutput struct {
	JobID       string  `json:"jobId"`
	RunID       *string `json:"runId"`  // null during blind window
	Status      string  `json:"status"` // queued|running|stalled|completed|failed|aborted (mapped)
	Phase       *string `json:"phase"`  // == run.currentPhase; null if unknown
	BlindWindow bool    `json:"blind_window"` // true while run file does not yet exist
	// Events is the DELTA: agents finished since the previous pi_status call
	// for this job (server-side tracking; see StatusInput.FromStart). Replaces
	// the always-full intermediate[] list, which re-injected every result
	// into the caller's context on every poll.
	Events      []StatusEvent        `json:"events"`
	AgentsDone  int                  `json:"agentsDone"`  // agents with a journal entry (errored counts as done); 0 in blind window
	AgentsTotal int                  `json:"agentsTotal"` // len(run.agents); 0 in blind window
	Intermediate []IntermediateResult `json:"intermediate"` // DEPRECATED: removed in Task 4 (delta replaces it)
	Result      any             `json:"result,omitempty" jsonschema:"the synthesized workflow result as arbitrary JSON, coerced to the §5.4 contract object when completed"`
	Metadata    *StatusMetadata `json:"metadata,omitempty"`
	Write       *WriteInfo      `json:"write,omitempty"`
	Progress    *Progress       `json:"progress,omitempty"`
	Authoring   *AuthoringInfo  `json:"authoring,omitempty"`
	Error       string          `json:"error,omitempty"`
}
```

Keep `IntermediateResult` itself permanently — the dashboard's `JobDetail.Intermediate` still uses it.

- [ ] **Step 2: Verify the tree builds and tests pass (additive change only)**

Run: `go build ./... && go test -race -count=1 ./internal/model/ ./internal/mcpserver/`
Expected: PASS — nothing was removed yet.

- [ ] **Step 3: Commit**

```bash
git add internal/model/model.go
git commit -m "feat(model): StatusEvent delta types, from_start/include_results inputs"
```

### Task 2: `runstore.EventsSince` — journal→agent delta join

**Files:**
- Create: `internal/runstore/events.go`
- Test: `internal/runstore/events_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/runstore/events_test.go`:

```go
package runstore

import (
	"encoding/json"
	"testing"

	"pi-mcp/internal/model"
)

// eventsRun: journal in completion order 0,2,1; agent callIndex 1 errored.
func eventsRun() *model.Run {
	errMsg := "boom"
	return &model.Run{
		Agents: []model.Agent{
			{ID: 1, CallIndex: 0, Label: "scan", Model: "haiku", Phase: "Scan", Status: "done"},
			{ID: 2, CallIndex: 1, Label: "fix", Model: "sonnet", Phase: "Fix", Status: "error", Error: &errMsg},
			{ID: 3, CallIndex: 2, Label: "verify", Model: "opus", Phase: "Verify", Status: "done"},
		},
		Journal: []model.JournalEntry{
			{Index: 0, Result: json.RawMessage(`{"ok":1}`)},
			{Index: 2, Result: json.RawMessage(`{"ok":2}`)},
			{Index: 1, Result: json.RawMessage(`{"ok":3}`)},
		},
	}
}

func TestEventsSince_DeltaAndJoin(t *testing.T) {
	r := eventsRun()

	all := EventsSince(r, 0, false, 1024)
	if len(all) != 3 {
		t.Fatalf("from 0: want 3 events, got %d", len(all))
	}
	// journal completion order: scan, verify, fix
	if all[0].Label != "scan" || all[1].Label != "verify" || all[2].Label != "fix" {
		t.Fatalf("join/order wrong: %+v", all)
	}
	if all[2].Status != "error" || all[2].Error != "boom" {
		t.Fatalf("errored agent must surface status=error + message: %+v", all[2])
	}
	if all[0].Status != "ok" || all[0].Result != nil {
		t.Fatalf("default rows carry no result body: %+v", all[0])
	}

	tail := EventsSince(r, 2, false, 1024)
	if len(tail) != 1 || tail[0].Label != "fix" {
		t.Fatalf("from 2: want only the last journal entry, got %+v", tail)
	}

	if got := EventsSince(r, 3, false, 1024); len(got) != 0 {
		t.Fatalf("from == len(journal): want empty, got %+v", got)
	}
	if got := EventsSince(r, 99, false, 1024); len(got) != 0 {
		t.Fatalf("from beyond journal: want empty (clamped), got %+v", got)
	}
}

func TestEventsSince_IncludeResultsAndTruncation(t *testing.T) {
	r := eventsRun()

	with := EventsSince(r, 0, true, 1024)
	if with[0].Result == nil || with[0].Truncated {
		t.Fatalf("include_results must attach the full result: %+v", with[0])
	}

	// 4-byte cap: every result truncates to a preview.
	small := EventsSince(r, 0, true, 4)
	if small[0].Result != nil || !small[0].Truncated || small[0].Preview == "" {
		t.Fatalf("oversized result must become preview+truncated: %+v", small[0])
	}
}

func TestEventsSince_SkipsJournalWithoutAgent(t *testing.T) {
	r := eventsRun()
	r.Journal = append(r.Journal, model.JournalEntry{Index: 99, Result: json.RawMessage(`1`)})
	got := EventsSince(r, 3, false, 1024)
	if len(got) != 0 {
		t.Fatalf("journal entry with no agent is skipped: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test -race -count=1 ./internal/runstore/ -run TestEventsSince -v`
Expected: FAIL — `undefined: EventsSince`

- [ ] **Step 3: Implement**

Create `internal/runstore/events.go`:

```go
package runstore

import "pi-mcp/internal/model"

// EventsSince builds the pi_status delta: one StatusEvent per journal entry at
// POSITION >= from (journal is completion order — positions, not indexes),
// joined to its agent via journal[].Index == agents[].CallIndex (same join
// rule as Intermediates: NEVER by array position, NEVER by agent id). Entries
// with no matching agent are skipped but still consume a position, so the
// caller's "delivered = len(journal)" bookkeeping stays consistent.
//
// includeResults attaches the COMPLETE journal result, truncated to maxBytes
// (preview+truncated beyond that). Default rows carry identity+status only —
// that is the context-frugality contract.
func EventsSince(r *model.Run, from int, includeResults bool, maxBytes int) []model.StatusEvent {
	if from < 0 {
		from = 0
	}
	if from > len(r.Journal) {
		from = len(r.Journal)
	}
	byCallIndex := make(map[int]*model.Agent, len(r.Agents))
	for i := range r.Agents {
		byCallIndex[r.Agents[i].CallIndex] = &r.Agents[i]
	}
	out := make([]model.StatusEvent, 0, len(r.Journal)-from)
	for i := from; i < len(r.Journal); i++ {
		j := &r.Journal[i]
		ag, ok := byCallIndex[j.Index]
		if !ok {
			continue
		}
		ev := model.StatusEvent{Label: ag.Label, Model: ag.Model, Phase: ag.Phase, Status: "ok"}
		if ag.Status == "error" {
			ev.Status = "error"
			if ag.Error != nil {
				ev.Error = *ag.Error
			}
		}
		if includeResults {
			if maxBytes > 0 && len(j.Result) > maxBytes {
				ev.Truncated = true
				ev.Preview = truncatePreview(string(j.Result), maxBytes)
			} else {
				ev.Result = RawToAny(j.Result)
			}
		}
		out = append(out, ev)
	}
	return out
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test -race -count=1 ./internal/runstore/ -v -run TestEventsSince`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/runstore/events.go internal/runstore/events_test.go
git commit -m "feat(runstore): EventsSince journal-delta join for pi_status events"
```

### Task 3: `livestatus` — stale non-terminal becomes `stalled`, not `failed`

**Files:**
- Modify: `internal/livestatus/livestatus.go` (Derive, ~line 41)
- Test: `internal/livestatus/livestatus_test.go`
- Modify (ripple): `internal/mcpserver/handler_status_test.go` (`TestStatus_StaleRunningBecomesFailed` ~line 179, `TestStatus_WriteStaleWorktreeStillFails` ~line 571), `internal/dashboard/state_test.go` (any case asserting stale→"failed" via Derive)

- [ ] **Step 1: Write the failing test**

In `internal/livestatus/livestatus_test.go`, add:

```go
func TestDerive_StaleAliveBecomesStalledNotFailed(t *testing.T) {
	now := time.Now()
	old := now.Add(-(config.StaleThreshold + time.Minute))

	// pid alive (or unknown) + stale run file -> stalled (non-terminal, may resume)
	if got := Derive("running", &old, now, true, false); got != "stalled" {
		t.Fatalf("stale+alive: want stalled, got %q", got)
	}
	if IsTerminal("stalled") {
		t.Fatalf("stalled must be NON-terminal")
	}
	// confirmed-dead pid still wins -> failed
	if got := Derive("running", &old, now, false, false); got != "failed" {
		t.Fatalf("dead pid: want failed, got %q", got)
	}
	// active worktree still overrides staleness -> running
	if got := Derive("running", &old, now, true, true); got != "running" {
		t.Fatalf("active worktree: want running, got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test -race -count=1 ./internal/livestatus/ -run TestDerive_StaleAlive -v`
Expected: FAIL — got "failed", want "stalled"

- [ ] **Step 3: Implement**

In `internal/livestatus/livestatus.go`, change the stale branch of `Derive` and its doc:

```go
// Derive applies the liveness override to a disk status. A non-terminal status
// whose process is confirmed dead becomes "failed". A non-terminal status whose
// updatedAt is older than config.StaleThreshold (and whose worktree is idle)
// becomes "stalled" — NON-terminal: the run may resume, and callers (pi_status
// waits, dashboard) treat it as a wake/display signal, not an exit. A
// recently-active worktree overrides run-file staleness. pidAlive==true means
// "alive or unknown".
func Derive(disk string, updatedAt *time.Time, now time.Time, pidAlive, worktreeActive bool) string {
	mapped := MapDisk(disk)
	if IsTerminal(mapped) {
		return mapped
	}
	if !pidAlive {
		return "failed"
	}
	if worktreeActive {
		return mapped
	}
	if updatedAt != nil && now.Sub(*updatedAt) > config.StaleThreshold {
		return "stalled"
	}
	return mapped
}
```

`MapDisk` and `IsTerminal` need no change (`stalled` falls to the non-terminal default).

- [ ] **Step 4: Fix ripple tests**

Run: `go test -race -count=1 ./... 2>&1 | grep -E "FAIL|ok " | head -20`

Update every test that asserted stale→"failed" through Derive to expect "stalled". Known ones:
- `internal/livestatus/livestatus_test.go`: existing stale case in the Derive table (if present) → "stalled".
- `internal/mcpserver/handler_status_test.go:179 TestStatus_StaleRunningBecomesFailed` → rename to `TestStatus_StaleRunningBecomesStalled`, expect `out.Status == "stalled"` and (important) NO `out.Error` — stalled is informational, not a failure. If the old test asserted an error message, delete that assertion.
- `internal/mcpserver/handler_status_test.go:571 TestStatus_WriteStaleWorktreeStillFails` → expect "stalled" (rename `...StillStalls`). The DEAD-pid variants (using `s.pidAlive=false`) still expect "failed" — do not touch those.
- `internal/dashboard/state_test.go`: cases where a run file is present and stale (Derive path) → "stalled". The NO-run-file blind path (`summarize` rec-only branch, `ErrServerRestarted`) keeps "failed" — that is process-gone logic, not Derive. Do not change `summarize`.

Also check `buildStatus` (`internal/mcpserver/handler_status.go` ~line 207): `if out.Status == "failed" || out.Status == "aborted"` sets Error — "stalled" is intentionally NOT in that list; verify no other `== "failed"` comparisons need a stalled case (grep: `grep -rn '"failed"' internal/mcpserver internal/dashboard`).

Dashboard counts (review finding #16, decided explicitly): add a `Stalled int \`json:"stalled"\`` field to `dashboard.Counts` and a `case "stalled": st.Counts.Stalled++` arm in `BuildState`'s switch — otherwise stalled rows silently count only toward Total. The web UI ignores unknown JSON fields, so app.js needs no change (it can render the new bucket later).

Expected after fixes: `go test -race -count=1 ./...` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/livestatus/ internal/mcpserver/handler_status_test.go internal/dashboard/state_test.go
git commit -m "feat(livestatus): stale-but-alive runs derive stalled (non-terminal), not failed"
```

### Task 4: Delta tracker + buildStatus wiring (events, counts)

**Files:**
- Create: `internal/mcpserver/delta.go`
- Test: `internal/mcpserver/delta_test.go`
- Modify: `internal/mcpserver/handler_status.go` (handleStatus ~line 114, buildStatus ~line 129)
- Modify: `internal/mcpserver/server.go` (Server struct + New)

- [ ] **Step 1: Write the failing tracker test**

Create `internal/mcpserver/delta_test.go`:

```go
package mcpserver

import (
	"testing"
	"time"
)

func TestDeltaTracker_TakeAdvancesPerKey(t *testing.T) {
	d := newDeltaTracker()

	if got := d.take("job-a", 3, false); got != 0 {
		t.Fatalf("first call delivers from 0, got %d", got)
	}
	if got := d.take("job-a", 5, false); got != 3 {
		t.Fatalf("second call delivers from 3, got %d", got)
	}
	if got := d.take("job-b", 2, false); got != 0 {
		t.Fatalf("independent key starts at 0, got %d", got)
	}
	// from_start resets to 0 but still advances to the new length
	if got := d.take("job-a", 5, true); got != 0 {
		t.Fatalf("from_start re-delivers from 0, got %d", got)
	}
	if got := d.take("job-a", 5, false); got != 5 {
		t.Fatalf("after from_start, position is len(journal), got %d", got)
	}
	// journal shrank (authoring retry rewrote the run): start over, never panic
	if got := d.take("job-a", 1, false); got != 0 {
		t.Fatalf("shrunken journal resets to 0, got %d", got)
	}
}

func TestDeltaTracker_WarnOncePerActivityEpoch(t *testing.T) {
	d := newDeltaTracker()
	t0 := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	if !d.shouldWarn("job-a", t0) {
		t.Fatalf("first crossing must warn")
	}
	if d.shouldWarn("job-a", t0) {
		t.Fatalf("same activity epoch must NOT re-warn")
	}
	if !d.shouldWarn("job-a", t0.Add(time.Minute)) {
		t.Fatalf("advanced activity epoch re-arms the warning")
	}
	if d.shouldWarn("job-a", t0) {
		t.Fatalf("older epoch never re-warns")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test -race -count=1 ./internal/mcpserver/ -run TestDeltaTracker -v`
Expected: FAIL — `undefined: newDeltaTracker`

- [ ] **Step 3: Implement the tracker**

Create `internal/mcpserver/delta.go`:

```go
package mcpserver

import (
	"sync"
	"time"
)

// deltaTracker remembers, per delta key, (a) how many journal positions have
// already been delivered to a pi_status caller and (b) the activity epoch of
// the last early-inactivity warning. In-memory ONLY and deliberately so:
// pi-mcp is a stdio server — one process per Claude Code session — so this is
// naturally per-session state. A server restart loses it and the next call
// re-delivers all events once (accepted in the spec). There is no
// client-managed cursor.
type deltaTracker struct {
	mu        sync.Mutex
	delivered map[string]int       // key -> journal positions already delivered
	warned    map[string]time.Time // key -> lastActivity epoch at warn time
}

func newDeltaTracker() *deltaTracker {
	return &deltaTracker{delivered: map[string]int{}, warned: map[string]time.Time{}}
}

// take returns the journal position to deliver from and advances the position
// to journalLen. fromStart resets to 0 first. A position beyond journalLen
// (journal shrank — e.g. an authoring retry rewrote the run) resets to 0
// rather than erroring: re-delivery is always safe, silence is not.
func (d *deltaTracker) take(key string, journalLen int, fromStart bool) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	from := d.delivered[key]
	if fromStart || from > journalLen {
		from = 0
	}
	d.delivered[key] = journalLen
	return from
}

// shouldWarn reports whether the early-inactivity warning should fire for this
// activity epoch, arming it as a side effect. It re-arms only when observed
// activity ADVANCES past the previously warned epoch, so each quiet stretch
// warns exactly once (per server lifetime; a restart may repeat one warning).
func (d *deltaTracker) shouldWarn(key string, lastActivity time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if prev, ok := d.warned[key]; ok && !lastActivity.After(prev) {
		return false
	}
	d.warned[key] = lastActivity
	return true
}

// deltaKey identifies the tracked job: jobID when owned, else runsDir+runID
// (the runId+cwd query path for external runs).
func deltaKey(tgt resolved) string {
	if tgt.jobID != "" {
		return tgt.jobID
	}
	return tgt.runsDir + "/" + tgt.runID
}
```

Run: `go test -race -count=1 ./internal/mcpserver/ -run TestDeltaTracker -v`
Expected: PASS

- [ ] **Step 4: Write the failing handler test**

Append to `internal/mcpserver/handler_status_test.go` (uses the existing `buildRun()` fixture — it has agents+journal; check `runfixture_test.go` for its exact shape and adapt label assertions to it if they differ):

```go
func TestStatus_EventsAreDeltaAcrossCalls(t *testing.T) {
	jobs := newFakeJobs()
	store := newFakeStore()
	s := New(jobs, store)

	rec := model.JobRecord{JobID: "j1", RunID: "r1", RunsDir: "/runs", Mode: model.ModeRead,
		Status: model.JobRunning, StartedAt: mustTime("2026-06-11T10:00:00Z")}
	jobs.lookup["j1"] = rec
	run := buildRun() // fixture: agents + journal populated, status running
	store.runs["/runs/r1"] = run

	_, out1, err := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1"})
	if err != nil {
		t.Fatalf("status 1: %v", err)
	}
	if len(out1.Events) != len(run.Journal) {
		t.Fatalf("first call delivers all %d events, got %d", len(run.Journal), len(out1.Events))
	}
	if out1.AgentsTotal != len(run.Agents) {
		t.Fatalf("agentsTotal: want %d got %d", len(run.Agents), out1.AgentsTotal)
	}
	if out1.Events[0].Result != nil {
		t.Fatalf("minimal mode: no result bodies in events")
	}

	_, out2, _ := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1"})
	if len(out2.Events) != 0 {
		t.Fatalf("second call with no new journal entries delivers nothing, got %d", len(out2.Events))
	}

	_, out3, _ := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1", FromStart: true})
	if len(out3.Events) != len(run.Journal) {
		t.Fatalf("from_start re-delivers all, got %d", len(out3.Events))
	}

	_, out4, _ := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1", FromStart: true, IncludeResults: true})
	if len(out4.Events) == 0 || (out4.Events[0].Result == nil && !out4.Events[0].Truncated) {
		t.Fatalf("include_results attaches bodies: %+v", out4.Events)
	}
}
```

Run: `go test -race -count=1 ./internal/mcpserver/ -run TestStatus_EventsAreDelta -v`
Expected: FAIL (Events never populated / compile error if Step 1 of Task 1 left the dangling line)

- [ ] **Step 5: Wire into Server + buildStatus**

`internal/mcpserver/server.go` — add the field and init:

```go
type Server struct {
	jobs  JobsService
	store RunStore

	now          func() time.Time
	waitCap      time.Duration
	pollInterval time.Duration
	delta        *deltaTracker // per-job delivery + early-warning state (delta protocol)

	pidAlive func(pid int) bool
}
```

In `New(...)` add `delta: newDeltaTracker(),`.

`internal/mcpserver/handler_status.go`:

1. `handleStatus` passes the input through. `from_start` SKIPS the wait — the
caller asked for an immediate replay; waiting first could block the redelivery
for the whole cap (review finding #4):

```go
func (s *Server) handleStatus(ctx context.Context, _ *mcp.CallToolRequest, in model.StatusInput) (*mcp.CallToolResult, model.StatusOutput, error) {
	tgt, err := s.resolveTarget(in)
	if err != nil {
		return nil, model.StatusOutput{}, err
	}

	// from_start is a replay request: deliver immediately, never wait first.
	if in.Wait && !in.FromStart {
		s.waitForChange(ctx, tgt)
	}

	out := s.buildStatus(tgt, in)
	return nil, out, nil
}
```

2. `buildStatus` gains the input param: `func (s *Server) buildStatus(tgt resolved, in model.StatusInput) model.StatusOutput`. Its FIRST line initializes Events so it serializes as `[]`, never `null`, on EVERY path (blind window, queued, parse error, terminal-without-run — review finding #5):

```go
	out := model.StatusOutput{JobID: tgt.jobID, Events: []model.StatusEvent{}}
```

Where the old `out.Intermediate = runstore.Intermediates(...)` line is (right after `out.Status = liveStatus(...)`), replace it with:

```go
	// Delta events: only journal positions not yet delivered to this session.
	// take() advances even when the caller is about to see a terminal status —
	// the final result arrives via out.Result, not via re-played events.
	from := s.delta.take(deltaKey(tgt), len(run.Journal), in.FromStart)
	out.Events = runstore.EventsSince(run, from, in.IncludeResults, config.MaxInlineResultBytes)
	// agentsDone = agents WITH A JOURNAL ENTRY (spec wording; errored agents
	// have one too). NOT agent.Status counting — a done-status agent whose
	// journal entry has not landed yet is not "done" for delta purposes.
	out.AgentsTotal = len(run.Agents)
	byCall := make(map[int]bool, len(run.Agents))
	for i := range run.Agents {
		byCall[run.Agents[i].CallIndex] = true
	}
	for i := range run.Journal {
		if byCall[run.Journal[i].Index] {
			out.AgentsDone++
		}
	}
```

3. NOW delete the deprecated `Intermediate` field from `model.StatusOutput` (added-then-kept in Task 1) — this commit removes the field, the handler line, and every test reference together, so the tree still builds.

4. Every other `s.buildStatus(tgt)` caller (grep `buildStatus(`) passes `in` — only handleStatus calls it.

5. Fix remaining test references to `out.Intermediate` in `handler_status_test.go` (e.g. `TestStatus_CompletedReadResultAndIntermediate` ~line 135): assert on `out.Events` instead — rename to `TestStatus_CompletedReadResultAndEvents`, expect `len(out.Events) == len(run.Journal)` on the FIRST call. Each test constructs a fresh `Server` via `New`, so tracker state never leaks between tests. Add one assertion somewhere convenient (e.g. the blind-window test): `if out.Events == nil { t.Fatalf("events must be [] on every path, never null") }`.

- [ ] **Step 6: Run the package tests**

Run: `go test -race -count=1 ./internal/mcpserver/ 2>&1 | tail -5`
Expected: any `outputschema_test.go` reference to `Intermediate` fails to compile — adapt it in THIS task (the field is gone): populate `Events: []model.StatusEvent{{Label: "a", Model: "m", Phase: "p", Status: "ok"}}` in its fixture (the test validates StatusOutput against the reflected go-sdk schema). Everything must PASS before the commit.

- [ ] **Step 7: Full build + tests, then commit**

Run: `go build ./... && go test -race -count=1 ./... 2>&1 | grep -E "FAIL|ok " | head`
Expected: all `ok`

```bash
git add internal/mcpserver/ internal/model/
git commit -m "feat(mcpserver): pi_status delta events with server-side per-job tracking"
```

### Task 5: Configurable WaitCap (5min default) + tool description

**Files:**
- Modify: `internal/config/config.go` (WaitCap ~line 44)
- Modify: `internal/mcpserver/handler_status.go` (defaultWaitCap const ~line 16)
- Modify: `internal/mcpserver/server.go` (descStatus ~line 38)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestWaitCap_EnvOverride(t *testing.T) {
	t.Setenv("PI_MCP_WAIT_CAP", "")
	if got := WaitCap(); got != WaitCapDefault {
		t.Fatalf("unset env: want default %v, got %v", WaitCapDefault, got)
	}
	t.Setenv("PI_MCP_WAIT_CAP", "90s")
	if got := WaitCap(); got != 90*time.Second {
		t.Fatalf("90s override: got %v", got)
	}
	t.Setenv("PI_MCP_WAIT_CAP", "garbage")
	if got := WaitCap(); got != WaitCapDefault {
		t.Fatalf("invalid env falls back to default, got %v", got)
	}
	t.Setenv("PI_MCP_WAIT_CAP", "-5s")
	if got := WaitCap(); got != WaitCapDefault {
		t.Fatalf("non-positive env falls back to default, got %v", got)
	}
}
```

Run: `go test -race -count=1 ./internal/config/ -run TestWaitCap -v`
Expected: FAIL — `WaitCap` is currently a const (compile error: cannot call)

- [ ] **Step 2: Implement**

In `internal/config/config.go`, replace the `WaitCap` const with:

```go
// WaitCapDefault bounds the pi_status long-poll when PI_MCP_WAIT_CAP is unset.
// 5min (raised from 60s, spec 2026-06-11): with delta responses a quiet run
// costs one tiny round-trip per cap window, and event wakes (Phase 2) end the
// wait early on real change. DEPLOY PREREQUISITE: the MCP client's tool-call
// timeout must exceed this (Claude Code: MCP_TOOL_TIMEOUT) — verify before
// shipping; lower via env, never by rebuild.
const WaitCapDefault = 5 * time.Minute

// WaitCap returns the long-poll cap: PI_MCP_WAIT_CAP as a Go duration
// (e.g. "60s", "2m") when set and positive, else WaitCapDefault.
func WaitCap() time.Duration {
	if v := os.Getenv("PI_MCP_WAIT_CAP"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return WaitCapDefault
}
```

In `internal/mcpserver/handler_status.go` change `defaultWaitCap = config.WaitCap` to a call site: delete the `defaultWaitCap` const and in `server.go` `New()` use `waitCap: config.WaitCap(),`. Keep `defaultPollInterval` as-is for now (Phase 2 changes it). Grep for other users: `grep -rn "config.WaitCap\|defaultWaitCap" --include="*.go" .` and fix each (tests that referenced `config.WaitCap` as a value use `config.WaitCapDefault`).

In `internal/mcpserver/server.go`, replace `descStatus`:

```go
	descStatus = "Get job progress as a compact DELTA: each call returns only the agents that finished since your previous call for this job (events[]), plus status/phase/agentsDone/agentsTotal/heartbeat. The full synthesized result arrives ONCE at status=completed. wait=true long-polls until something changes (cap 5min; env PI_MCP_WAIT_CAP). from_start=true re-delivers all events; include_results=true attaches the new events' full results (16KB cap). Query by jobId, or runId+cwd. status=stalled means no activity past the stale threshold (non-terminal; consider pi_cancel)."
```

- [ ] **Step 3: Run tests**

Run: `go test -race -count=1 ./... 2>&1 | grep -E "FAIL|ok " | head`
Expected: all `ok`. Note: `TestStatus_WaitCapReturnsWithoutChange` (~line 336) drives `s.waitCap` directly with a fake clock — confirm it sets its own small cap; if it relied on the 60s default, set `s.waitCap = time.Second` explicitly in the test.

- [ ] **Step 4: Commit**

```bash
git add internal/config/ internal/mcpserver/
git commit -m "feat(config): PI_MCP_WAIT_CAP env override, 5min default long-poll cap"
```

### Task 6: Early-inactivity warning + stalled wake in waitForChange

**Files:**
- Modify: `internal/config/config.go` (new const)
- Modify: `internal/mcpserver/handler_status.go` (waitForChange ~line 314)
- Test: `internal/mcpserver/handler_status_test.go`

- [ ] **Step 1: Add the threshold const**

In `internal/config/config.go` after `StaleThreshold`:

```go
// EarlyInactivityWarn: a non-terminal run with no observed activity (run-file
// updatedAt, write-job worktree mtime) for this long wakes a pi_status wait
// ONCE per activity epoch, carrying the progress heartbeat — informational,
// the status stays running. Only StaleThreshold (~30min) flips the status to
// stalled. Must stay well under StaleThreshold and over typical agent bursts.
const EarlyInactivityWarn = 5 * time.Minute
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/mcpserver/handler_status_test.go`. Pattern note: the existing wait tests (`TestStatus_WaitWakesOnJournalGrowth` ~line 301) drive `s.now` with a fake clock advanced by the store's `seq` Load calls — follow that pattern:

```go
// waitClock advances a fake clock by step on every call, letting wait loops
// progress deterministically without real sleeps.
func waitClock(start time.Time, step time.Duration) func() time.Time {
	cur := start
	return func() time.Time {
		cur = cur.Add(step)
		return cur
	}
}

func TestWait_EarlyInactivityWarningWakesOnce(t *testing.T) {
	jobs := newFakeJobs()
	store := newFakeStore()
	s := New(jobs, store)
	s.pollInterval = time.Millisecond
	s.waitCap = time.Hour // never the cap: the warning must end the wait

	start := mustTime("2026-06-11T10:00:00Z")
	stale := start.Add(-(config.EarlyInactivityWarn + time.Minute))
	run := buildRun()
	run.Status = "running"
	run.UpdatedAt = &stale
	store.runs["/runs/r1"] = run

	rec := model.JobRecord{JobID: "j1", RunID: "r1", RunsDir: "/runs", Mode: model.ModeRead,
		Status: model.JobRunning, StartedAt: stale}
	jobs.lookup["j1"] = rec

	s.now = waitClock(start, time.Second)
	done := make(chan struct{})
	go func() {
		_, _, _ = s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1", Wait: true})
		close(done)
	}()
	select {
	case <-done: // woke on the warning, not the 1h cap
	case <-time.After(5 * time.Second):
		t.Fatalf("inactivity warning did not wake the wait")
	}

	// Same epoch again: must NOT wake early — it runs to the (now short) cap.
	s.waitCap = 50 * time.Millisecond
	s.now = waitClock(start, time.Millisecond)
	_, out, _ := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1", Wait: true})
	if out.Status != "running" {
		t.Fatalf("5min-quiet run is still running (not stalled), got %q", out.Status)
	}
}

func TestWait_StalledTransitionWakes(t *testing.T) {
	jobs := newFakeJobs()
	store := newFakeStore()
	s := New(jobs, store)
	s.pollInterval = time.Millisecond
	s.waitCap = time.Hour

	start := mustTime("2026-06-11T10:00:00Z")
	fresh := start.Add(-time.Minute)
	overStale := start.Add(-(config.StaleThreshold + time.Minute))

	runFresh := buildRun()
	runFresh.Status = "running"
	runFresh.UpdatedAt = &fresh
	runStale := buildRun()
	runStale.Status = "running"
	runStale.UpdatedAt = &overStale
	// seq: first Load sees fresh (base), later Loads see the stale snapshot.
	store.seq = []*model.Run{runFresh, runStale}

	rec := model.JobRecord{JobID: "j1", RunID: "r1", RunsDir: "/runs", Mode: model.ModeRead,
		Status: model.JobRunning, StartedAt: overStale}
	jobs.lookup["j1"] = rec

	s.now = waitClock(start, time.Second)
	done := make(chan struct{})
	var out model.StatusOutput
	go func() {
		_, out, _ = s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1", Wait: true})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("stalled transition did not wake the wait")
	}
	if out.Status != "stalled" {
		t.Fatalf("want stalled after wake, got %q", out.Status)
	}
}
```

NOTE: the first test's warning path also crosses into the second's territory if `stale` exceeds StaleThreshold — it does not (6min < ~30min). The stalled test reuses the early-warning wake for its first wake — that is fine: the assertion is on the RESULTING status. If the wake fires via the warning before the stale snapshot is served by `seq`, the final `buildStatus` Load returns the LAST seq element (stale) anyway — `fakeStore.seq` sticks on the last value.

Run: `go test -race -count=1 ./internal/mcpserver/ -run "TestWait_" -v`
Expected: FAIL (waits run to cap / status not stalled)

- [ ] **Step 3: Implement**

Rewrite `waitForChange` in `internal/mcpserver/handler_status.go`:

```go
// waitForChange long-polls until: the wake predicate fires (terminal, journal
// growth, agents growth, phase change), the run transitions INTO stalled, the
// early-inactivity warning fires (once per activity epoch — see
// deltaTracker.shouldWarn), or the wait cap elapses. A wait STARTED while
// already stalled does not return immediately — it waits for change/cap, so a
// caller seeing "stalled" and re-waiting does not busy-spin.
func (s *Server) waitForChange(ctx context.Context, tgt resolved) {
	deadline := s.now().Add(s.waitCap)
	var base snapshot
	haveBase := false
	baseStalled := false

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		now := s.now()
		run, err := s.store.Load(tgt.runsDir, tgt.runID)
		if err == nil {
			cur := snapshotOf(run)
			_, wtLast, wtOK := s.worktreeLast(tgt)
			worktreeActive := wtOK && now.Sub(wtLast).Abs() <= config.StaleThreshold
			stalledNow := liveStatus(run.Status, run.UpdatedAt, now, s.pidIsAlive(tgt), worktreeActive) == "stalled"
			if !haveBase {
				base, haveBase = cur, true
				baseStalled = stalledNow
				if cur.terminal {
					return
				}
			} else {
				if wakeChanged(base, cur) {
					return
				}
				if stalledNow && !baseStalled {
					return // transitioned into stalled during this wait
				}
			}
			// Early-inactivity warning: newest of run-file update / worktree write.
			if la, ok := lastActivity(run, wtLast, wtOK); ok && now.Sub(la) > config.EarlyInactivityWarn {
				if s.delta.shouldWarn(deltaKey(tgt), la) {
					return
				}
			}
		}
		if !now.Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// worktreeLast fetches write-job worktree activity (zero values for read jobs).
func (s *Server) worktreeLast(tgt resolved) (int, time.Time, bool) {
	if tgt.mode != model.ModeWrite || !tgt.hasJob {
		return 0, time.Time{}, false
	}
	return s.jobs.WorktreeActivity(tgt.jobID)
}
```

ALSO (review finding #3): the warning/stalled response must CARRY
`progress.lastActivitySeconds` for READ jobs too — today `progressBlock` only
sets it when `wtOK` (write jobs). Change `progressBlock` to take the run's
updatedAt and use the NEWEST activity signal:

```go
// progressBlock builds the elapsed-time heartbeat plus the age of the newest
// observed activity: run-file updatedAt (all modes) vs worktree mtime (write).
// This is what makes the early-inactivity warning legible to the caller
// ("no activity for Xmin") even for read jobs. Durations clamp at zero.
func progressBlock(tgt resolved, now time.Time, runUpdated *time.Time, wtFiles int, wtLast time.Time, wtOK bool) *model.Progress {
	if !tgt.hasJob || tgt.startedAt.IsZero() {
		return nil
	}
	p := &model.Progress{ElapsedSeconds: clampSeconds(now.Sub(tgt.startedAt))}
	if wtOK {
		p.WorktreeFiles = wtFiles
	}
	var last time.Time
	if runUpdated != nil {
		last = *runUpdated
	}
	if wtOK && wtLast.After(last) {
		last = wtLast
	}
	if !last.IsZero() {
		la := clampSeconds(now.Sub(last))
		p.LastActivitySeconds = &la
	}
	return p
}
```

Update both call sites in `buildStatus`: the blind-window one passes `nil`
(no run yet), the post-Load one passes `run.UpdatedAt`. Add an assertion in
the early-warning test: the returned `out.Progress.LastActivitySeconds` is
non-nil and ≥ 300 for a read-mode job quiet past EarlyInactivityWarn.

```go
// (continuation of handler_status.go shown above)

// lastActivity is the newest observed activity: run.updatedAt vs the worktree
// mtime. ok=false when neither signal exists (never warn on missing data).
func lastActivity(run *model.Run, wtLast time.Time, wtOK bool) (time.Time, bool) {
	var la time.Time
	ok := false
	if run.UpdatedAt != nil {
		la, ok = *run.UpdatedAt, true
	}
	if wtOK && wtLast.After(la) {
		la, ok = wtLast, true
	}
	return la, ok
}
```

Add `"pi-mcp/internal/model"` to handler_status.go imports if not present (it is — `model.StatusInput` already used).

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/mcpserver/ 2>&1 | tail -3`
Expected: PASS, including the two new tests and all existing wait tests (the new stalled/warn checks never fire for fresh `UpdatedAt` fixtures).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/mcpserver/
git commit -m "feat(mcpserver): early-inactivity warning + stalled-transition wake in long-poll"
```

### Task 7: Transient parse-error grace in buildStatus

**Files:**
- Modify: `internal/mcpserver/handler_status.go` (buildStatus Load call ~line 146)
- Modify: `internal/mcpserver/server.go` (sleep seam)
- Test: `internal/mcpserver/handler_status_test.go`

- [ ] **Step 1: Write the failing test**

`fakeStore.seq` already returns different results per Load call; it needs an error variant. Add to `internal/mcpserver/fakes_test.go`:

```go
// seqErr, when non-nil at index i, makes the i-th Load return this error
// (paired with seq; simulates a mid-write decode failure).
```

Concretely, change `fakeStore` to:

```go
type fakeStore struct {
	runs      map[string]*model.Run
	seq       []*model.Run
	seqErrs   []error // optional, parallel to seq: non-nil entry -> Load returns it
	calls     int
	list      []model.ListItem
	listErr   error
	authoring map[string]*model.AuthoringInfo
}
```

and in `Load`, inside the `if f.seq != nil` branch, after computing `i`:

```go
		if i < len(f.seqErrs) && f.seqErrs[i] != nil {
			return nil, f.seqErrs[i]
		}
```

Then append the test to `handler_status_test.go`:

```go
func TestStatus_TransientParseErrorRetriesBeforeFailing(t *testing.T) {
	jobs := newFakeJobs()
	store := newFakeStore()
	s := New(jobs, store)
	s.sleep = func(time.Duration) {} // no real waiting in tests

	run := buildRun()
	run.Status = "running"
	decodeErr := errors.New("unexpected end of JSON input") // NOT ErrRunNotFound
	store.seq = []*model.Run{nil, run}                      // 1st Load: error, 2nd: good
	store.seqErrs = []error{decodeErr, nil}

	rec := model.JobRecord{JobID: "j1", RunID: "r1", RunsDir: "/runs", Mode: model.ModeRead,
		Status: model.JobRunning, StartedAt: mustTime("2026-06-11T10:00:00Z")}
	jobs.lookup["j1"] = rec

	_, out, err := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1"})
	if err != nil {
		t.Fatalf("handleStatus: %v", err)
	}
	if out.Status == "failed" || out.Error == config.ErrPersistenceError {
		t.Fatalf("transient decode error must not surface failed: %+v", out)
	}
}

func TestStatus_PersistentParseErrorStillFails(t *testing.T) {
	jobs := newFakeJobs()
	store := newFakeStore()
	s := New(jobs, store)
	s.sleep = func(time.Duration) {}

	decodeErr := errors.New("unexpected end of JSON input")
	store.seq = []*model.Run{nil, nil, nil, nil, nil}
	store.seqErrs = []error{decodeErr, decodeErr, decodeErr, decodeErr, decodeErr}

	rec := model.JobRecord{JobID: "j1", RunID: "r1", RunsDir: "/runs", Mode: model.ModeRead,
		Status: model.JobRunning, StartedAt: mustTime("2026-06-11T10:00:00Z")}
	jobs.lookup["j1"] = rec

	_, out, _ := s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1"})
	if out.Status != "failed" || out.Error != config.ErrPersistenceError {
		t.Fatalf("persistent decode error still fails: %+v", out)
	}
}
```

(Add `"errors"` to the test file imports.)

Run: `go test -race -count=1 ./internal/mcpserver/ -run TestStatus_TransientParse -v`
Expected: FAIL — `s.sleep` undefined / transient error surfaces failed

- [ ] **Step 2: Implement**

`internal/mcpserver/server.go` — add to Server struct: `sleep func(time.Duration) // injectable (grace retries); tests use a no-op` and to `New`: `sleep: time.Sleep,`.

`internal/mcpserver/handler_status.go` — add consts next to `defaultPollInterval`:

```go
	// graceRetries x graceInterval bounds the transient parse-error grace (~2s):
	// a wake/tick can catch the run file mid-write (this Load path has no .bak
	// fallback). A still-running job retries; a persistent error still fails.
	graceRetries  = 4
	graceInterval = 500 * time.Millisecond
```

Add the method and switch buildStatus's Load:

```go
// loadWithGrace wraps store.Load with the transient-decode grace: ErrRunNotFound
// passes through untouched (the blind window has its own handling), terminal
// jobs never wait (nothing is mid-write), any other error retries briefly.
func (s *Server) loadWithGrace(tgt resolved) (*model.Run, error) {
	run, err := s.store.Load(tgt.runsDir, tgt.runID)
	if err == nil || errors.Is(err, ErrRunNotFound) || isTerminal(string(tgt.jobStatus)) {
		return run, err
	}
	for i := 0; i < graceRetries; i++ {
		s.sleep(graceInterval)
		run, err = s.store.Load(tgt.runsDir, tgt.runID)
		if err == nil || errors.Is(err, ErrRunNotFound) {
			return run, err
		}
	}
	return run, err
}
```

In `buildStatus`, change `run, err := s.store.Load(tgt.runsDir, tgt.runID)` → `run, err := s.loadWithGrace(tgt)`.

- [ ] **Step 3: Run tests + commit**

Run: `go test -race -count=1 ./... 2>&1 | grep -E "FAIL|ok " | head`
Expected: all `ok`

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): transient run-file parse errors retry briefly instead of failing"
```

### Task 8: Output-schema test + Phase 1 sweep

**Files:**
- Modify: `internal/mcpserver/outputschema_test.go` (if not already adapted in Task 4)
- Verify: whole tree

- [ ] **Step 1: Confirm the schema test exercises the new fields**

Open `internal/mcpserver/outputschema_test.go` (`TestStatusOutputPassesGoSDKSchemaValidation`, line 27). Ensure its StatusOutput fixture populates the new fields so reflection+validation covers them:

```go
	Events: []model.StatusEvent{
		{Label: "scan", Model: "haiku", Phase: "Scan", Status: "ok"},
		{Label: "fix", Model: "sonnet", Phase: "Fix", Status: "error", Error: "boom"},
		{Label: "big", Model: "opus", Phase: "Verify", Status: "ok", Result: map[string]any{"k": "v"}},
	},
	AgentsDone:  2,
	AgentsTotal: 3,
```

and that no `Intermediate:` field remains. The `Result any` event field must pass for object/array/scalar — if validation rejects, mirror the jsonschema-tag approach already proven on `IntermediateResult.Result` (the tag is already in Task 1's type).

- [ ] **Step 2: Whole-tree verification**

Run: `go vet ./... && go test -race -count=1 ./... 2>&1 | grep -E "FAIL|ok "`
Expected: every package `ok`, no vet findings.
Also: `grep -rn "Intermediate" internal/mcpserver/ | grep -v _test.go` — expected: no remaining references (runstore + dashboard keep theirs).

- [ ] **Step 3: Commit**

```bash
git add internal/mcpserver/outputschema_test.go
git commit -m "test(mcpserver): output schema covers events/agents delta fields"
```

### Task 9: Phase 1 e2e + production deploy

**Files:**
- Modify (read first): `test/e2e/e2e_smoke_test.go` — it asserts on StatusOutput; update any `Intermediate` references to `Events` and assert the delta property (a second status call after completion returns 0 new events).

- [ ] **Step 1: Update the e2e assertions**

Read `test/e2e/e2e_smoke_test.go`. Where it polls `pi_status` to completion, add after the terminal status is reached:

```go
	// Delta contract: the events were consumed during polling; a fresh call
	// delivers nothing new, and from_start re-delivers everything.
	// (Adapt variable names to the file's existing client helpers.)
```

— concretely, one extra `pi_status{jobId}` call asserting `len(out.Events) == 0`, then one with `from_start:true` asserting `len(out.Events) > 0`. Follow the file's existing call pattern (it drives the real MCP server over stdio).

- [ ] **Step 2: Run the FULL e2e and show COMPLETE output (user requirement — never grep it down)**

Run: `PI_MCP_E2E=1 go test ./test/e2e/ -run TestE2ESmoke -v -timeout 10m`
Expected: PASS, with the full log shown to the user verbatim (~60s, costs ~$0.13).

- [ ] **Step 3: Verify the Claude Code MCP tool timeout EMPIRICALLY (spec deploy prerequisite)**

Config greps are not proof (review finding #13) — run a controlled long wait through the REAL client path:

1. Deploy the Phase 1 server (Step 4) with a temporary `PI_MCP_WAIT_CAP=90s` in the pi-mcp server env.
2. From a fresh Claude Code session, start a `pi_workflow` and immediately call `pi_status{jobId, wait:true}` during a quiet stretch (the blind window works: ~20s of guaranteed silence, so pick a moment with >90s of expected quiet or use a long-running task).
3. Observe: the tool call must RETURN normally after ~90s, not be killed by the client at ~60s.
4. If the client kills it: export `MCP_TOOL_TIMEOUT=360000` (ms) in the exact environment that launches Claude Code (check the shell profile / launcher actually used on this box), re-test, and only then raise to the 5min default. If it cannot be raised, set `PI_MCP_WAIT_CAP=60s` permanently in the server env.
5. Record the observed timeout and the chosen setting in the deploy commit message.

Do not skip: a client timeout below WaitCap strands every wait.

- [ ] **Step 4: Deploy to prod (user ships verified changes straight to prod)**

```bash
cd /root/pi-mcp && go build ./... && go test -race -count=1 ./...
# MCP runtime: rebuild/repin per installer flow
ls installer/   # follow the pinned-runtime update flow used by recent commits (c2ba656)
# New Claude Code sessions pick up the new pi-mcp binary via the installer pin.
```

- [ ] **Step 5: Commit + verify live**

```bash
git add test/e2e/
git commit -m "feat: phase 1 delta pi_status live — e2e updated for events contract"
```

Then start a real `pi_workflow` from a Claude Code session and watch: first `pi_status` shows events, second shows none, failure/stall lines are one-liners.

---

## Phase 2 — fsnotify events (layered under Phase 1)

### Task 10: Add the fsnotify dependency

- [ ] **Step 1:**

```bash
cd /root/pi-mcp && go get github.com/fsnotify/fsnotify@latest && go mod tidy && go build ./...
git add go.mod go.sum
git commit -m "chore: add fsnotify dependency"
```

### Task 11: `internal/watch` package

**Files:**
- Create: `internal/watch/watch.go`
- Test: `internal/watch/watch_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/watch/watch_test.go`:

```go
package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// expectSignal waits up to 3s for a notification (debounce is 50ms).
func expectSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("no notification for %s", what)
	}
}

func TestSubscribe_WakesOnCreateAndWrite(t *testing.T) {
	dir := t.TempDir()
	ch, cancel, err := Subscribe(dir)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	p := filepath.Join(dir, "run.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectSignal(t, ch, "file create")

	if err := os.WriteFile(p, []byte(`{"a":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectSignal(t, ch, "file write")
}

func TestSubscribe_CoalescesBursts(t *testing.T) {
	dir := t.TempDir()
	ch, cancel, err := Subscribe(dir)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	p := filepath.Join(dir, "run.json")
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(p, []byte{byte(i)}, 0o644)
	}
	expectSignal(t, ch, "burst")
	// drain whatever coalesced frames exist, then assert quiet
	deadline := time.After(300 * time.Millisecond)
	n := 0
	for {
		select {
		case <-ch:
			n++
			if n > 5 {
				t.Fatalf("burst not coalesced: %d notifications", n)
			}
		case <-deadline:
			return
		}
	}
}

func TestSubscribe_LateBornTargetDir(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "a", "b", "runs")

	ch, cancel, err := Subscribe(target) // target does NOT exist yet
	if err != nil {
		t.Fatalf("subscribe on missing dir must fall back to ancestor: %v", err)
	}
	defer cancel()

	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "r1.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectSignal(t, ch, "file inside late-born target")
}

func TestSubscribe_CancelStops(t *testing.T) {
	dir := t.TempDir()
	ch, cancel, err := Subscribe(dir)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cancel()
	cancel() // idempotent
	_ = os.WriteFile(filepath.Join(dir, "x.json"), []byte(`1`), 0o644)
	select {
	case _, open := <-ch:
		if open {
			// a buffered pre-cancel frame is acceptable; a SECOND would not be
			select {
			case _, open2 := <-ch:
				if open2 {
					t.Fatalf("notifications after cancel")
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
}
```

Run: `go test -race -count=1 ./internal/watch/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 2: Implement**

Create `internal/watch/watch.go`:

```go
// Package watch is a thin fsnotify wrapper: subscribe to a directory, receive
// coalesced change notifications. INVARIANT (spec 2026-06-11): events are
// HINTS, never correctness — every consumer re-reads authoritative state on
// wake and keeps a fallback ticker, so a dropped/missed event costs one
// fallback interval of latency, never a wrong answer.
package watch

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce coalesces bursts (pi rewrites the run file frequently) into one
// notification per quiet gap.
const debounce = 50 * time.Millisecond

// Subscribe watches dir and returns a channel signaled after any
// create/write/rename/remove at or under it. When dir does not exist yet
// (late-born runsDir: pi creates it after launch), the nearest existing
// ancestor is watched and the precise watch is armed when dir appears.
// cancel is idempotent. A non-nil error means nothing could be watched at
// all; the caller stays on its fallback ticker.
func Subscribe(dir string) (<-chan struct{}, func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	target := filepath.Clean(dir)
	if err := addNearest(w, target); err != nil {
		_ = w.Close()
		return nil, nil, err
	}

	ch := make(chan struct{}, 1)
	done := make(chan struct{})
	go run(w, target, ch, done)

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			_ = w.Close()
		})
	}
	return ch, cancel, nil
}

// run owns the watcher goroutine: filter to the target subtree, debounce,
// re-arm the precise watch when an ancestor event creates the target.
func run(w *fsnotify.Watcher, target string, ch chan struct{}, done chan struct{}) {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-done:
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// Re-arm on EVERY create: fsnotify is NOT recursive, so when the
			// target is late-born behind nested missing dirs (parent/a/b/runs),
			// each intermediate mkdir is visible only on the currently-watched
			// ancestor. addNearest walks the watch as deep as currently
			// possible — by the time the target exists it is watched directly.
			// Cheap no-op once armed; the consumer's fallback ticker covers
			// any residual race window (events-are-hints invariant).
			if ev.Op.Has(fsnotify.Create) {
				_ = addNearest(w, target)
				// A create on the path TOWARD the target is itself a useful
				// hint (e.g. the runs dir appearing): fall through to within().
			}
			if !within(target, ev.Name) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			}
		case <-timerC:
			timer, timerC = nil, nil
			notify(ch)
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
			// Overflow/transient errors are survivable: emit a hint so the
			// consumer reconciles immediately instead of waiting for fallback.
			notify(ch)
		}
	}
}

// notify is the non-blocking buffered(1) send: a pending hint is enough.
func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// addNearest watches target or, when absent, its nearest existing ancestor.
func addNearest(w *fsnotify.Watcher, target string) error {
	p := target
	for {
		if err := w.Add(p); err == nil {
			return nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return w.Add(p) // hit the root and even that failed: report it
		}
		p = parent
	}
}

// within reports whether name is target itself or inside its subtree.
func within(target, name string) bool {
	n := filepath.Clean(name)
	if n == target {
		return true
	}
	rel, err := filepath.Rel(target, n)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [ ] **Step 3: Run tests**

Run: `go test -race -count=1 ./internal/watch/ -v`
Expected: PASS (4 tests). The late-born test (nested `a/b/runs` below the
watched ancestor) is the critical one: it exercises the `addNearest` re-arm on
every Create, which walks the watch deeper as each intermediate dir appears
(fsnotify is not recursive — a single `w.Add(target)` retry would miss nested
creation). If it still flakes under `-race -count=10`, the consumer-side
fallback ticker is the spec-mandated safety net, but the test must pass
reliably before commit — debug the within()/re-arm interaction, do not loosen
the test.

API-shape note (adversarial review finding, accepted as-is): the spec sketch
says `Watcher.Subscribe(dir)`; a package-level `Subscribe(dir)` is the same
contract without an empty struct — consumers inject `func(dir string) (...)`
seams anyway, which fakes implement directly. Documented here so the deviation
is deliberate.

- [ ] **Step 4: Commit**

```bash
git add internal/watch/
git commit -m "feat(watch): fsnotify subscribe with debounce + late-born-dir re-arm"
```

### Task 12: Event wake in pi_status long-poll

**Files:**
- Modify: `internal/mcpserver/server.go` (subscribe seam)
- Modify: `internal/mcpserver/handler_status.go` (waitForChange select + defaultPollInterval)
- Test: `internal/mcpserver/handler_status_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestWait_EventWakeBeatsSlowTicker(t *testing.T) {
	jobs := newFakeJobs()
	store := newFakeStore()
	s := New(jobs, store)
	s.pollInterval = time.Hour // ticker can never fire: only the event can wake
	s.waitCap = time.Hour
	s.now = time.Now

	wake := make(chan struct{}, 1)
	s.subscribe = func(dir string) (<-chan struct{}, func(), error) {
		return wake, func() {}, nil
	}

	r1 := buildRun()
	r1.Status = "running"
	r2 := buildRun()
	r2.Status = "running"
	r2.Journal = append(r2.Journal, model.JournalEntry{Index: 0, Result: json.RawMessage(`1`)})
	store.seq = []*model.Run{r1, r2}

	rec := model.JobRecord{JobID: "j1", RunID: "r1", RunsDir: "/runs", Mode: model.ModeRead,
		Status: model.JobRunning, StartedAt: time.Now()}
	jobs.lookup["j1"] = rec

	done := make(chan struct{})
	go func() {
		_, _, _ = s.handleStatus(ctxBG(), nil, model.StatusInput{JobID: "j1", Wait: true})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let the wait take its base snapshot
	wake <- struct{}{}                // fsnotify hint: journal grew
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("event wake did not end the wait (ticker is 1h)")
	}
}
```

(Add `"encoding/json"` to imports if missing.)

Run: `go test -race -count=1 ./internal/mcpserver/ -run TestWait_EventWake -v`
Expected: FAIL — `s.subscribe` undefined

- [ ] **Step 2: Implement**

`server.go` — Server struct gains:

```go
	// subscribe is the fsnotify seam (watch.Subscribe in production; nil in
	// most tests -> pure ticker). Events are hints: the wait re-reads and
	// re-evaluates its predicate on every wake regardless of source.
	subscribe func(dir string) (<-chan struct{}, func(), error)
```

`New(...)` sets `subscribe: watch.Subscribe,` (import `"pi-mcp/internal/watch"`).

`handler_status.go` — change `defaultPollInterval = 250 * time.Millisecond` to `defaultPollInterval = 2 * time.Second` with the comment `// fallback only: fsnotify wakes carry the latency; the ticker reconciles drops`. In `waitForChange`, before the loop:

```go
	var wake <-chan struct{}
	if s.subscribe != nil {
		if ch, cancel, err := s.subscribe(tgt.runsDir); err == nil {
			wake = ch
			defer cancel()
		}
	}
```

and the select becomes:

```go
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake: // nil channel when unsubscribed: blocks forever, ticker rules
		}
```

- [ ] **Step 3: Tests + commit**

Run: `go test -race -count=1 ./internal/mcpserver/ 2>&1 | tail -3`
Expected: PASS. CHECK: existing wait tests drive fake clocks with 1ms pollIntervals — they have `s.subscribe == nil`? No: tests build via `New`, which now sets the real `watch.Subscribe` against fake paths like `/runs` — Subscribe on a nonexistent unwritable path falls back to ancestor `/` (watchable!) and never fires, which is harmless, BUT to keep tests hermetic set `s.subscribe = nil` in the test helper used by wait tests, or better: in each wait test added/touched, add `s.subscribe = nil` right after `New(...)`. Sweep: `grep -n "Wait: true" internal/mcpserver/handler_status_test.go` and add the nil line to each test func that drives waits without wanting events.

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): fsnotify wake in pi_status long-poll, 2s fallback ticker"
```

### Task 13: Event wake in jobs.correlate

**Files:**
- Modify: `internal/jobs/registry.go` (Config ~line 21, correlate ~line 328)
- Test: `internal/jobs/registry_test.go` area (find the correlate tests: `grep -n "correlate" internal/jobs/*_test.go`)

- [ ] **Step 1: Implement (keep the poll, add the wake — smallest correct change)**

`Config` gains:

```go
	// Subscribe is the fsnotify seam for correlate's runsDir wake (nil -> pure
	// polling; tests). Events are hints — the readdir re-check stays the truth.
	Subscribe func(dir string) (<-chan struct{}, func(), error)
```

`Registry` gains field `subscribe func(dir string) (<-chan struct{}, func(), error)`, set from cfg in `NewRegistry`. In `correlate`, replace the poll loop tail with:

```go
	var wake <-chan struct{}
	if r.subscribe != nil {
		if ch, cancel, err := r.subscribe(j.Record.RunsDir); err == nil {
			wake = ch
			defer cancel()
		}
	}

	ticker := time.NewTicker(correlatePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		r.mu.Lock()
		resolved := r.tryResolveRunUnlocked(j, sid)
		if resolved {
			j.updatedAt = r.now()
			_ = r.flushUnlocked()
		}
		r.mu.Unlock()
		if resolved {
			return
		}
	}
```

(Note the restructure: the resolve-check moves below the select so both wake sources share it.) Change the fallback interval per spec (Consumer 2: "fallback 2s" — the watcher carries the latency now):

```go
// correlatePollInterval is the FALLBACK cadence for correlate's run-file
// re-scan. With the fsnotify wake armed it only reconciles dropped events;
// without one (subscribe nil/failed) it is the sole resolution path.
var correlatePollInterval = 2 * time.Second
```

The two existing correlate tests already override this var (2ms/1ms) and are unaffected.

- [ ] **Step 2: Write the wake test**

Append to `internal/jobs/correlate_test.go` (uses that file's existing `lateCorrelator` + `newFakeLauncher` + `mustRegistry` harness):

```go
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
```

Run: `go test -race -count=1 ./internal/jobs/ -run TestCorrelate_EventWake -v`
Expected: FAIL first (`Config` has no `Subscribe`), then PASS after Step 1's implementation.

- [ ] **Step 3: Production wiring (concrete, not a note)**

Modify `internal/app/app.go` — in `buildRegistryReal` (~line 127), add the field to the literal:

```go
	return jobs.NewRegistry(
		jobs.Config{
			Cap:          config.DefaultConcurrencyCap,
			PersistPath:  persist,
			WorktreeRoot: wtRoot,
			SnapshotRun:  snapshotRunFile,
			Subscribe:    watch.Subscribe,
		},
```

with import `"pi-mcp/internal/watch"`.

- [ ] **Step 4: Tests + commit**

Run: `go test -race -count=1 ./internal/jobs/ ./internal/app/ 2>&1 | tail -4`
Expected: PASS

```bash
git add internal/jobs/ internal/app/
git commit -m "feat(jobs): fsnotify wake resolves correlate, 2s fallback, app wiring"
```

### Task 14: Dashboard — run-file parse cache + event-driven poller

**Files:**
- Create: `internal/dashboard/runcache.go`
- Test: `internal/dashboard/runcache_test.go`
- Modify: `internal/dashboard/state.go` (readRun var ~line 105)
- Modify: `internal/dashboard/poller.go` (Run loop, interval, watch set)
- Test: `internal/dashboard/poller_test.go`

- [ ] **Step 1: Write the failing cache test**

Create `internal/dashboard/runcache_test.go`:

```go
package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunCache_ParsesOnceUntilFileChanges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r1.json")
	if err := os.WriteFile(p, []byte(`{"runId":"r1","status":"running","agents":[],"journal":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newRunCache()
	parses := 0
	c.parse = func(path string) (run *parsedRun, err error) {
		parses++
		return defaultParse(path)
	}
	// NOTE: if the implementation wraps runstore.ReadRun directly without a
	// parse seam, count via a package-level test hook instead — the assertion
	// that matters is the parse COUNT, adapt the seam to the implementation.

	r1, err := c.read(dir, "r1")
	if err != nil || r1 == nil || r1.RunID != "r1" {
		t.Fatalf("first read: %v %+v", err, r1)
	}
	r2, _ := c.read(dir, "r1")
	if r2 == nil || parses != 1 {
		t.Fatalf("unchanged file must be served from cache: parses=%d", parses)
	}

	// rewrite with different size -> reparse
	if err := os.WriteFile(p, []byte(`{"runId":"r1","status":"completed","agents":[],"journal":[],"durationMs":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r3, _ := c.read(dir, "r1")
	if r3 == nil || r3.Status != "completed" || parses != 2 {
		t.Fatalf("changed file must reparse: parses=%d run=%+v", parses, r3)
	}
}
```

- [ ] **Step 2: Implement the cache**

Create `internal/dashboard/runcache.go`. Design constraints from the spec: key by (mtime, size) via `os.Stat` of the `.json` (ns mtime granularity on this box's ext4); a stat miss falls through to the full `readRun` (which has the `.bak` fallback) WITHOUT caching; the stat is per-tick but the PARSE (the expensive part) is skipped. Terminal-vs-active needs no special casing — an unchanged stat key never reparses, a changed key always does (covers `.bak` repair / rename-replacement).

```go
package dashboard

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"pi-mcp/internal/model"
)

// parsedRun aliases model.Run for the cache seam.
type parsedRun = model.Run

// runCache memoizes parsed run files by (mtime, size). The dashboard poller
// re-reads EVERY job's run file on every tick; almost all of them (terminal
// jobs, idle actives) have not changed. A stat is ~1µs; the JSON parse it
// replaces is the actual cost. Stat-miss (file gone -> .bak fallback path)
// bypasses the cache entirely so the recovery semantics of readRun stay
// untouched.
type runCache struct {
	mu    sync.Mutex
	m     map[string]runCacheEntry
	parse func(path string) (*parsedRun, error) // seam (tests count parses)
}

type runCacheEntry struct {
	mtime time.Time
	size  int64
	run   *parsedRun
}

func newRunCache() *runCache {
	return &runCache{m: map[string]runCacheEntry{}, parse: defaultParse}
}

// defaultParse is the uncached single-file loader (no .bak handling here —
// the cache only fronts the stat-hit fast path).
func defaultParse(path string) (*parsedRun, error) {
	return readRunFile(path)
}

// read returns the parsed run for <runsDir>/<runID>.json, reparsing only when
// the stat key changed. A missing primary file returns (nil, fs.ErrNotExist)
// so the caller falls back to the full readRun (.bak recovery).
func (c *runCache) read(runsDir, runID string) (*parsedRun, error) {
	if runID == "" {
		return nil, os.ErrNotExist
	}
	path := filepath.Join(runsDir, runID+".json")
	st, err := os.Stat(path)
	if err != nil {
		// File gone: DROP the entry so a later reappearance with a
		// coincidentally-equal stat key can never serve the old parse
		// (review finding #10).
		c.mu.Lock()
		delete(c.m, path)
		c.mu.Unlock()
		return nil, err
	}
	key := path
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && e.mtime.Equal(st.ModTime()) && e.size == st.Size() {
		return e.run, nil
	}
	run, err := c.parse(path)
	if err != nil {
		return nil, err // do not cache failures (mid-write); next tick retries
	}
	c.mu.Lock()
	c.m[key] = runCacheEntry{mtime: st.ModTime(), size: st.Size(), run: run}
	c.mu.Unlock()
	return run, nil
}
```

In `internal/dashboard/state.go`: extract the inner loader so both paths share decode rules —

```go
// readRunFile decodes one run file via runstore.ReadRun (.bak fallback inside).
func readRunFile(path string) (*model.Run, error) { return runstore.ReadRun(path) }
```

and change the `readRun` package var to route through a package-level cache:

```go
var runFiles = newRunCache()

var readRun = func(runsDir, runID string) (*model.Run, error) {
	if runID == "" {
		return nil, fs.ErrNotExist
	}
	if run, err := runFiles.read(runsDir, runID); err == nil {
		return run, nil
	}
	// stat-miss / parse error: the full ReadRun path (.bak recovery). Never
	// cached — recovery must re-evaluate every time.
	return runstore.ReadRun(filepath.Join(runsDir, runID+".json"))
}
```

(Existing tests that override `readRun` keep working — the var is unchanged in shape.)

- [ ] **Step 3: Run cache tests**

Run: `go test -race -count=1 ./internal/dashboard/ -run TestRunCache -v`
Expected: PASS — and the rest of the dashboard package still PASS (`go test -race -count=1 ./internal/dashboard/`).

- [ ] **Step 4: Event-driven poller**

Modify `internal/dashboard/poller.go`:

```go
// Poller fields gain:
	subscribe func(dir string) (<-chan struct{}, func(), error) // watch seam; nil -> pure ticker

	wmu  sync.Mutex        // guards subs (Tick is public: tests call it while Run is live)
	subs map[string]func() // watched dir -> cancel
	wake chan struct{}     // fan-in of all subscriptions
```

`NewPoller` sets `interval: 5 * time.Second` (was 1s), `subscribe: watch.Subscribe`, `subs: map[string]func(){}`, `wake: make(chan struct{}, 1)`.

`Run` becomes:

```go
// Run ticks until ctx is done. fsnotify wakes (registry DB dir + active jobs'
// runs dirs) carry the latency; the 5s ticker reconciles dropped events. The
// first tick happens immediately.
func (p *Poller) Run(ctx context.Context) {
	p.Tick()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			p.refreshWatches(nil) // cancel everything
			return
		case <-t.C:
		case <-p.wake:
		}
		p.Tick()
	}
}

// refreshWatches reconciles the subscription set to EXACTLY want (plus the
// registry DB dir, always wanted while running): new dirs subscribe, dropped
// dirs cancel — a long-lived dashboard never accumulates stale watches
// (review finding #12). nil want cancels everything (shutdown). Guarded by
// wmu: Tick is public and tests call it while Run is live.
func (p *Poller) refreshWatches(want map[string]bool) {
	if p.subscribe == nil {
		return
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	for dir, cancel := range p.subs {
		if want == nil || !want[dir] {
			cancel()
			delete(p.subs, dir)
		}
	}
	if want == nil {
		return
	}
	for dir := range want {
		if dir == "" {
			continue
		}
		if _, ok := p.subs[dir]; ok {
			continue
		}
		ch, cancel, err := p.subscribe(dir)
		if err != nil {
			continue // fallback ticker covers it; retried on the next Tick
		}
		p.subs[dir] = cancel
		go func() {
			for range ch {
				select {
				case p.wake <- struct{}{}:
				default:
				}
			}
		}()
	}
}
```

In `Tick`, after `recs, err := p.readRegistry(...)` succeeds, compute the wanted set and reconcile:

```go
	want := map[string]bool{filepath.Dir(p.registryPath): true}
	for i := range recs {
		st := string(recs[i].Status)
		if st == "running" || st == "queued" {
			want[recs[i].RunsDir] = true
		}
	}
	p.refreshWatches(want)
```

- [ ] **Step 5: Poller wake test**

Append to `internal/dashboard/poller_test.go` (uses the file's existing `captureSink` + `recsForTest` + `nowFresh` helpers; add `"context"` to its imports):

```go
// TestPoller_EventWakeTicksWithoutInterval: interval is 1h, so only the
// injected fsnotify wake can produce the second broadcast.
func TestPoller_EventWakeTicksWithoutInterval(t *testing.T) {
	sink := &captureSink{}
	p := NewPoller("unused.db", "/state", sink)
	p.interval = time.Hour
	wake := make(chan struct{}, 1)
	p.subscribe = func(string) (<-chan struct{}, func(), error) { return wake, func() {}, nil }

	calls := 0
	p.readRegistry = func(string) ([]model.JobRecord, error) {
		calls++
		recs := recsForTest()
		if calls > 1 {
			recs = recs[:len(recs)-1] // shrink the fleet so the woken tick broadcasts
		}
		return recs, nil
	}
	p.now = func() time.Time { return nowFresh }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitCount := func(n int, what string) {
		deadline := time.Now().Add(2 * time.Second)
		for sink.count() < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if sink.count() < n {
			t.Fatalf("%s: broadcasts=%d want >=%d", what, sink.count(), n)
		}
	}
	waitCount(1, "initial tick")
	wake <- struct{}{}
	waitCount(2, "event wake (interval is 1h)")
}
```

Also add a race-coverage line to the run: this test plus the existing direct-`Tick()` tests under `-race` exercise the `wmu` guard.

- [ ] **Step 6: Tests + commit**

Run: `go test -race -count=1 ./internal/dashboard/ 2>&1 | tail -3`
Expected: PASS (existing poller tests construct Pollers via NewPoller — they now subscribe to real temp paths or fail-and-fallback; if any test hangs on the new wake path, set `p.subscribe = nil` in that test).

```bash
git add internal/dashboard/
git commit -m "feat(dashboard): run-file parse cache + fsnotify-driven poller (5s fallback)"
```

### Task 15: Phase 2 e2e + deploy

- [ ] **Step 1: Full suite**

Run: `go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`

- [ ] **Step 2: Full e2e, complete output shown (user requirement)**

Run: `PI_MCP_E2E=1 go test ./test/e2e/ -run TestE2ESmoke -v -timeout 10m`
Expected: PASS, full output relayed verbatim to the user.

- [ ] **Step 3: Deploy both binaries**

```bash
cd /root/pi-mcp && go build ./...
# dashboard: rebuild + restart the systemd unit (find the exact unit name first)
systemctl list-units | grep -i dashboard
go build -o $(which pi-dashboard 2>/dev/null || echo /usr/local/bin/pi-dashboard) ./cmd/pi-dashboard
sudo systemctl restart <unit-found-above>
# MCP runtime: repin via installer flow (see commit c2ba656 for the pattern)
```

- [ ] **Step 4: Live verification**

Start a real `pi_workflow`; confirm on the dashboard that updates appear sub-second during the run (vs the old 1s+ cadence), the open job detail streams intermediates progressively, and `pi_status wait=true` returns quickly after an agent finishes. Confirm idle: `top`-check the dashboard process at rest (no 1s parse churn).

- [ ] **Step 5: Final commit**

```bash
git add -A && git commit -m "feat: phase 2 event-driven updates live"
```

---

## Self-review notes (already applied)

- Spec coverage: delta protocol (T1–T4), stalled vocabulary + dashboard count (T3), WaitCap env + empirical client-timeout check (T5, T9), early warning + stall wake + read-job lastActivitySeconds (T6), parse grace (T7), schema/tests (T8), fsnotify invariant + nested late-born dir re-arm (T11), three consumers with concrete app wiring (T12–T13), poller watch-set reconcile + cache with stat-miss drop (T14), two-phase deploy (T9, T15).
- Adversarial plan review (2026-06-11) applied: every commit builds (Intermediate removed in T4, not T1); agentsDone counts journal entries joined to agents, not agent statuses; from_start skips the wait; Events serializes as [] on every path; correlate fallback is 2s per spec with `internal/app/app.go` wiring as a concrete step; late-born watch re-arms via addNearest on every Create (nested dirs); poller subs guarded by wmu and reconciled to the exact wanted set; cache drops entries on stat-miss; T13/T14 tests are complete code against the real harnesses (`lateCorrelator`/`mustRegistry`, `captureSink`/`recsForTest`).
- Accepted deviations (documented, spec amended to match): the run-file cache keys invalidation purely on the (mtime,size) stat key — no registry-terminal special case (one rule, same effect: unchanged files never re-parse); `internal/watch` exposes a package-level `Subscribe` instead of a `Watcher` type (consumers inject func seams either way).
