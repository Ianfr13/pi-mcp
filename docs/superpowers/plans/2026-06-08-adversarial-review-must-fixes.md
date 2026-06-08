# Adversarial-Review Must-Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 4 confirmed HIGH bugs the post-merge adversarial review found in the shipped SQLite-registry / dashboard / forcing-prompt code, plus the cheap hardening, without regressing the green suite or the cgo-free build.

**Architecture:** Each fix is an isolated, independently-committable change in one subsystem (config, dashboard cmd, jobs reconcile, runner spawn, jobs registry lifecycle, app wiring, jobs store). TDD throughout: every task adds a test that fails on current code and passes after the change. No fix depends on another, so tasks can run in any order; HIGHs (Tasks 1–5) come first.

**Tech Stack:** Go 1.26.4, `modernc.org/sqlite` (pure-Go — **build MUST stay `CGO_ENABLED=0`-clean**), the `go-sdk/mcp` server, stdlib `os/exec` + `syscall` for process groups.

---

## Source of truth

Adversarial review job `950262fd`, runId `mq5ngqik-etoxpu` (findings in `/root/pi-mcp/.pi/workflows/runs/mq5ngqik-etoxpu.json`), triaged in `docs/superpowers/HANDOFF-2026-06-08.md` §"OPEN — adversarial review must-fixes".

## File structure (what each task touches)

| Task | Bug | Files (code) | Files (test) |
|---|---|---|---|
| 1 | HIGH: staleness vs 20-min agent timeout | `internal/config/config.go` | `internal/config/config_test.go`, `internal/livestatus/livestatus_test.go`, `internal/dashboard/state_test.go` |
| 2 | HIGH: dashboard `--state-dir` builds `registry.json` | `internal/config/config.go`, `cmd/pi-dashboard/main.go` | `internal/config/config_test.go`, `cmd/pi-dashboard/main_test.go` |
| 3 | HIGH: reconcile prunes worktree of live orphan child | `internal/jobs/reconcile.go` | `internal/jobs/reconcile_test.go` |
| 4 | HIGH (root-cause of #3): pi children orphaned on kill | `internal/runner/runner.go` | `internal/runner/runner_test.go` |
| 5 | HIGH: `Registry.Close()` closes store before goroutines flush | `internal/jobs/registry.go` | `internal/jobs/registry_test.go` |
| 6 | hardening: reconcile only at boot | `internal/app/app.go` | `internal/app/app_test.go` |
| 7 | hardening: UPSERT can clobber a foreign live row | `internal/jobs/store.go` | `internal/jobs/store_test.go` |
| 8 | hardening: orchestrator can re-introduce 5-min kill | `internal/config/config.go` | `internal/config/config_test.go` |
| 9 | hardening: spec-promised pragmas dropped | `internal/jobs/store.go`, `internal/dashboard/registry.go` | `internal/jobs/store_test.go` |
| 10 | hardening: migration not atomic (corrupt JSON / races) | `internal/jobs/store.go` | `internal/jobs/store_test.go` |

**Explicitly DEFERRED (documented, not cheap):** the `ownerStartedAt` PID-reuse tiebreak via `/proc/<pid>` start-time (platform-specific, fragile — out of scope for this pass), and the dashboard "one persistent read-only `*sql.DB` handle for the poller lifetime" refactor (perf-only; the per-poll open is wasteful but not a correctness bug — Task 9 still adds the `query_only(1)` pragma to the existing per-poll open). Both remain in the handoff for a later pass.

## Conventions for every task

- Build gate (run from `/root/pi-mcp`): `CGO_ENABLED=0 go build ./...` must succeed.
- Format gate: `gofmt -w <changed files>` then `gofmt -l internal/ cmd/` prints nothing.
- Test runs use `-race -count=1` (no cache) — never trust a cached PASS.
- Commit messages use the repo's conventional-commit style (`fix(scope): …` / `feat(scope): …`).

---

## Task 1: Raise the staleness threshold above the injected agent timeout (HIGH)

**Bug:** `config.StaleThreshold` is `300s`, but the forcing prompt injects `agentTimeoutMs:1200000` (20 min). A single healthy agent can run up to 20 min with a quiet run file; `livestatus.Derive` (the hot path for both `pi_status` and the dashboard) marks any non-terminal run whose `updatedAt` is older than `StaleThreshold` as `failed` — so a healthy long-running agent flips to `failed` at 5 min. The dashboard is worst-hit: `internal/dashboard/state.go:164` passes `pidAlive=true` unconditionally, so staleness is the **only** thing that can fail a running job there.

**Fix:** Raise `StaleThreshold` to comfortably exceed the injected timeout (20 min + 10 min margin = 30 min), derived from a new `ForcedAgentTimeoutMs` const so the two cannot drift. Dead-owner jobs are still reaped promptly by the periodic reconcile (Task 6) and by the normal job lifecycle (a crashed pi child makes `wait()` return → `finish()` marks it failed), so a generous threshold does not delay real failure detection.

**Files:**
- Modify: `internal/config/config.go:19-26`
- Test: `internal/config/config_test.go:9-25`, `internal/livestatus/livestatus_test.go`, `internal/dashboard/state_test.go:1-14,113-125`

- [ ] **Step 1: Write the failing regression test (the real bug) in livestatus**

Append to `internal/livestatus/livestatus_test.go`:

```go
func TestDerive_LongRunningAgentNotStale(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	// A single agent can run up to the injected 20-min agentTimeoutMs with a quiet
	// run file. With a live (or unknown) pid and no worktree activity it MUST stay
	// running — not flip to "failed" — because StaleThreshold now exceeds 20 min.
	longRun := now.Add(-20 * time.Minute)
	if got := Derive("running", &longRun, now, true, false); got != "running" {
		t.Fatalf("20-min-old running agent -> %q want running (StaleThreshold must exceed the agent timeout)", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails on current code**

Run: `go test -race -count=1 ./internal/livestatus/ -run TestDerive_LongRunningAgentNotStale -v`
Expected: FAIL — `20-min-old running agent -> "failed" want running` (300s threshold).

- [ ] **Step 3: Update the config-constant assertions to the new value (also fails first)**

In `internal/config/config_test.go`, edit the `StaleThreshold` assertion in `TestConstants` (currently lines 13-15) and add the relationship assertion:

```go
	if StaleThreshold != 30*time.Minute {
		t.Errorf("StaleThreshold = %v, want 30m", StaleThreshold)
	}
	if ForcedAgentTimeoutMs != 1_200_000 {
		t.Errorf("ForcedAgentTimeoutMs = %d, want 1200000", ForcedAgentTimeoutMs)
	}
	if StaleThreshold <= time.Duration(ForcedAgentTimeoutMs)*time.Millisecond {
		t.Errorf("StaleThreshold (%v) must exceed the injected agent timeout (%dms)", StaleThreshold, ForcedAgentTimeoutMs)
	}
```

- [ ] **Step 4: Implement the constant change**

In `internal/config/config.go`, replace lines 19-26 (the `DefaultAgentTimeoutMs` const block tail and the `StaleThreshold` const) with:

```go
	// DefaultAgentTimeoutMs is the engine's per-agent timeout (~5min). pi-mcp
	// does NOT impose its own job timeout (cancel-only); this mirrors the engine.
	DefaultAgentTimeoutMs int64 = 300000
)

// ForcedAgentTimeoutMs is the per-agent timeout pi-mcp injects into the forcing
// prompt (20 min) so coding/TDD agents are not killed by the 5-min default. The
// forcing-prompt template (ForcingPromptTemplate) hardcodes this literal as
// "1200000"; keep the two in sync. StaleThreshold MUST exceed this value.
const ForcedAgentTimeoutMs int64 = 1_200_000

// StaleThreshold: a non-terminal job whose updatedAt is older than this is
// treated as crashed (liveness override in livestatus.Derive + the dashboard
// blind-window path + the write-job worktree-activity window). It MUST exceed
// ForcedAgentTimeoutMs — a single healthy agent can run that long with a quiet
// run file, and must NOT be reported failed. Genuinely-dead jobs are reaped
// promptly by the periodic reconcile and the normal job lifecycle, so a generous
// threshold does not delay real failure detection. 20-min timeout + 10-min margin.
const StaleThreshold = time.Duration(ForcedAgentTimeoutMs)*time.Millisecond + 10*time.Minute
```

- [ ] **Step 5: Fix the one test that hardcoded the old threshold**

`internal/dashboard/state_test.go` `TestBuildState_StaleBlindFails` (line 118) hardcodes `nowFresh.Add(-2 * 300 * time.Second)` (600s) — under a 30-min threshold that blind job is no longer stale, so the test would wrongly fail. Make it relative.

First add the `config` import to `internal/dashboard/state_test.go` (its import block, lines 3-10):

```go
import (
	"os"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
)
```

Then change line 118 from:

```go
			recs[i].StartedAt = nowFresh.Add(-2 * 300 * time.Second)
```

to:

```go
			recs[i].StartedAt = nowFresh.Add(-config.StaleThreshold - time.Minute)
```

- [ ] **Step 6: Run the affected suites to verify green**

Run: `go test -race -count=1 ./internal/config/ ./internal/livestatus/ ./internal/dashboard/ -v`
Expected: PASS (incl. the new `TestDerive_LongRunningAgentNotStale`, `TestConstants`, `TestBuildState_StaleBlindFails`).

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go internal/livestatus/livestatus_test.go internal/dashboard/state_test.go
git add internal/config/config.go internal/config/config_test.go internal/livestatus/livestatus_test.go internal/dashboard/state_test.go
git commit -m "fix(liveness): raise StaleThreshold above the 20-min agent timeout (no false 5-min failures)"
```

---

## Task 2: Dashboard `--state-dir` override must resolve `registry.db`, not `registry.json` (HIGH)

**Bug:** `cmd/pi-dashboard/main.go:39-40` builds an overridden registry path as `registry.json`, but the canonical registry is `registry.db`. A non-default `--state-dir` therefore points the dashboard at a non-existent JSON file → empty/split-brain dashboard. The help text (line 24) also says `registry.json`.

**Fix:** Add a `config.RegistryPathFor(stateDir)` helper (the single source of truth for "registry DB under an explicit state dir"), have `config.RegistryPath()` delegate to it, use it in `main.go`, fix the help text, and delete the now-dead `filepathJoin` helper.

**Files:**
- Modify: `internal/config/config.go:155-158`
- Modify: `cmd/pi-dashboard/main.go:23-24,37-41,99-109`
- Test: `internal/config/config_test.go`, `cmd/pi-dashboard/main_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestRegistryPathFor(t *testing.T) {
	want := "/custom/pi-mcp/registry.db"
	if got := RegistryPathFor("/custom"); got != want {
		t.Errorf("RegistryPathFor(/custom)=%q want %q", got, want)
	}
}
```

Replace the import block of `cmd/pi-dashboard/main_test.go` (line 3) and append a test:

```go
import (
	"strings"
	"testing"
)
```

```go
func TestRegistryPathFor_AlwaysDB(t *testing.T) {
	got := registryPathFor("/custom/state")
	want := "/custom/state/pi-mcp/registry.db"
	if got != want {
		t.Errorf("registryPathFor=%q want %q", got, want)
	}
	if strings.HasSuffix(got, ".json") {
		t.Errorf("registry path must be the canonical .db, not legacy .json: %q", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail (undefined symbols)**

Run: `go test -race -count=1 ./internal/config/ -run TestRegistryPathFor -v` → FAIL: `undefined: RegistryPathFor`.
Run: `go build ./cmd/pi-dashboard/` → FAIL: `undefined: registryPathFor` (test references it).

- [ ] **Step 3: Implement the config helper**

In `internal/config/config.go`, replace lines 155-158 (the `RegistryPath` func + its doc) with:

```go
// RegistryPathFor returns the SQLite registry DB path under an explicit state
// dir: <stateDir>/pi-mcp/registry.db. Any caller honoring a custom --state-dir
// MUST use this — never hand-build "registry.json" (the canonical registry is
// the .db; a .json path yields an empty/split-brain reader).
func RegistryPathFor(stateDir string) string {
	return filepath.Join(stateDir, "pi-mcp", "registry.db")
}

// RegistryPath is the SQLite job-registry DB under the default state dir.
func RegistryPath() string {
	return RegistryPathFor(StateDir())
}
```

- [ ] **Step 4: Implement the cmd changes**

In `cmd/pi-dashboard/main.go`:

(a) Fix the flag help text (line 24):

```go
	stateDir := flag.String("state-dir", config.StateDir(), "pi-mcp state dir (holds pi-mcp/registry.db)")
```

(b) Replace the registry-path block (lines 37-41) with a single call:

```go
	registryPath := registryPathFor(*stateDir)
```

(c) Replace the dead `filepathJoin` helper (lines 99-109) with the new testable wrapper:

```go
// registryPathFor resolves the registry DB path for a (possibly overridden)
// state dir. Always the canonical .db, never the legacy .json — a non-default
// --state-dir previously mis-built "registry.json" and split-brained the view.
func registryPathFor(stateDir string) string {
	return config.RegistryPathFor(stateDir)
}
```

- [ ] **Step 5: Run to verify green**

Run: `go test -race -count=1 ./internal/config/ ./cmd/pi-dashboard/ -v`
Expected: PASS (incl. `TestRegistryPathFor`, `TestRegistryPathFor_AlwaysDB`, and the existing `resolveAddr` tests).

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go cmd/pi-dashboard/main.go cmd/pi-dashboard/main_test.go
git add internal/config/config.go internal/config/config_test.go cmd/pi-dashboard/main.go cmd/pi-dashboard/main_test.go
git commit -m "fix(dashboard): --state-dir override resolves registry.db, not legacy registry.json"
```

---

## Task 3: Reconcile must not prune a worktree while an orphaned pi child is alive (HIGH)

**Bug:** `internal/jobs/reconcile.go:37-39` prunes the worktree of any dead-owner running write job **without checking whether the recorded child PID is still alive**. On abrupt server death the pi child is reparented to init and keeps writing the worktree; the next server's reconcile (dead owner) prunes it → data loss.

**Fix:** Before pruning, skip when `pidAlive(rec.PID)` — leave the worktree intact (the job is still claimed terminal in the DB). A later reconcile, once the orphan has exited, prunes it. (Task 4 reduces how often orphans exist at all.)

**Files:**
- Modify: `internal/jobs/reconcile.go:37-39`
- Test: `internal/jobs/reconcile_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/jobs/reconcile_test.go`:

```go
// TestReconcileSkipsPruneWhenOrphanChildAlive: a dead-OWNER running write job whose
// recorded child PID is still ALIVE (an orphaned pi process reparented after the
// owner died) is claimed terminal but its worktree is NOT pruned — pruning while a
// live process writes it would lose data. A later reconcile (child gone) prunes it.
func TestReconcileSkipsPruneWhenOrphanChildAlive(t *testing.T) {
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "registry.db")
	wt := writeWorktreeDir(t, dir, "orphan")
	clock := time.Unix(7_000_000, 0)
	prior := []model.JobRecord{
		{JobID: "orphan", Status: model.JobRunning, Mode: model.ModeWrite,
			WorktreePath: wt, Branch: "pi-mcp/job-orphan", PID: os.Getpid(), // live child
			StartedAt: clock.Add(-time.Second)},
	}
	seedDB(t, persistPath, prior, 0, "") // ownerPid=0 -> dead owner

	fp := &fakePruner{}
	r := mustRegistry(t, Config{Cap: 4, PersistPath: persistPath, WorktreeRoot: dir,
		Now: func() time.Time { return clock }}, newFakeLauncher("s"), &fakeCorrelator{}, fp)

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Claimed terminal (failed) in the DB...
	recs := readBackJobs(t, persistPath)
	if len(recs) != 1 || recs[0].Status != model.JobFailed {
		t.Fatalf("orphan job should be claimed failed, got %+v", recs)
	}
	// ...but the worktree must NOT be pruned while the orphan child is alive.
	if fp.prunedBranch("pi-mcp/job-orphan") {
		t.Errorf("worktree must NOT be pruned while orphan child alive; pruned=%v", fp.branches())
	}
}
```

- [ ] **Step 2: Run to verify it fails on current code**

Run: `go test -race -count=1 ./internal/jobs/ -run TestReconcileSkipsPruneWhenOrphanChildAlive -v`
Expected: FAIL — `worktree must NOT be pruned while orphan child alive` (current code prunes unconditionally).

- [ ] **Step 3: Implement the guard**

In `internal/jobs/reconcile.go`, replace the prune block (lines 37-39):

```go
		if rec.Mode == model.ModeWrite && rec.WorktreePath != "" {
			_ = r.pruner.Prune(rec.WorktreePath, rec.Branch)
		}
```

with:

```go
		if rec.Mode == model.ModeWrite && rec.WorktreePath != "" {
			if pidAlive(rec.PID) {
				// An orphaned pi child (reparented to init after the owning server
				// died abruptly) may still be writing this worktree. Pruning now would
				// lose data. The job is already claimed terminal; leave the worktree —
				// a later reconcile, once the child has exited, prunes it.
				continue
			}
			_ = r.pruner.Prune(rec.WorktreePath, rec.Branch)
		}
```

- [ ] **Step 4: Run to verify green (incl. the existing prune tests)**

Run: `go test -race -count=1 ./internal/jobs/ -run 'TestReconcile' -v`
Expected: PASS for all `TestReconcile*` — note `TestReconcileDeadOwnerWriteWorktreePruned` and `TestReconcile_OwnerScoped` seed `PID:0` (dead) so they still prune; only the new live-PID case is skipped.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/jobs/reconcile.go internal/jobs/reconcile_test.go
git add internal/jobs/reconcile.go internal/jobs/reconcile_test.go
git commit -m "fix(jobs): reconcile skips worktree prune while an orphaned pi child is still alive"
```

---

## Task 4: Spawn pi in its own process group and group-kill on cancel (HIGH — root cause of #3)

**Bug:** `internal/runner/runner.go:75` uses `exec.CommandContext` with no `Setpgid`, so ctx-cancel SIGKILLs only the direct pi process; the agent-fleet children pi spawned survive as orphans (which then keep writing a worktree — the data-loss vector Task 3 defends against).

**Fix:** Start pi as a new process-group leader (`Setpgid: true`) and override `cmd.Cancel` to SIGKILL the whole group (negative pid). Add a small `WaitDelay` so `Wait` never blocks forever on a pipe held by a killed grandchild. (Go 1.26 supports `cmd.Cancel`/`cmd.WaitDelay`.) This reaps the entire pi tree on `pi_cancel` and on `Registry.Close()`.

**Files:**
- Modify: `internal/runner/runner.go:1-9,75-77`
- Test: `internal/runner/runner_test.go:1-11`

- [ ] **Step 1: Write the failing test**

In `internal/runner/runner_test.go`, add `"syscall"` to the import block (lines 3-11):

```go
import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)
```

Append:

```go
func TestSpawnNewProcessGroup(t *testing.T) {
	installFakePi(t)
	workdir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// __HANG__ keeps the fake pi alive so we can inspect its process group.
	proc, err := Spawn(ctx, SpawnConfig{
		Prompt: "ignored __HANG__ token",
		CWD:    workdir,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	pid := proc.PID()
	if pid <= 0 {
		t.Fatalf("bad pid %d", pid)
	}
	// Setpgid makes the child its own process-group leader: pgid == pid. Without
	// it the child inherits the test runner's group (pgid != pid).
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(%d): %v", pid, err)
	}
	if pgid != pid {
		t.Errorf("child not in its own process group: pgid=%d pid=%d", pgid, pid)
	}

	// Cancel must group-kill and let Wait return promptly.
	cancel()
	go func() { _, _ = io.ReadAll(proc.Stdout) }()
	done := make(chan struct{})
	go func() { _ = proc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("group kill did not terminate the process within 10s")
	}
}
```

- [ ] **Step 2: Run to verify it fails on current code**

Run: `go test -race -count=1 ./internal/runner/ -run TestSpawnNewProcessGroup -v`
Expected: FAIL — `child not in its own process group: pgid=<runner group> pid=<child>`.

- [ ] **Step 3: Implement Setpgid + group-kill**

In `internal/runner/runner.go`, replace the import block (lines 3-9):

```go
import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)
```

Then, immediately after the `cmd.Stderr = cfg.Stderr` line (currently line 78), insert:

```go
	// Start pi as its own process-group leader so a cancel can SIGKILL the WHOLE
	// group — pi's agent-fleet children included — not just the pi process. An
	// orphaned grandchild that keeps writing a worktree is the data-loss vector
	// reconcile must otherwise defend against.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid => signal the process group led by the child.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// If a killed grandchild keeps the stdout pipe open, force-stop the wait after
	// a short grace period so Wait() never blocks forever.
	cmd.WaitDelay = 10 * time.Second
```

- [ ] **Step 4: Run to verify green (incl. the existing kill test)**

Run: `go test -race -count=1 ./internal/runner/ -v`
Expected: PASS — incl. `TestSpawnNewProcessGroup`, `TestSpawnContextKill`, `TestSpawnHappyPath`.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/runner/runner.go internal/runner/runner_test.go
git add internal/runner/runner.go internal/runner/runner_test.go
git commit -m "fix(runner): spawn pi in its own process group; cancel group-kills the whole pi tree"
```

---

## Task 5: `Registry.Close()` must wait for job goroutines before closing the store (HIGH)

**Bug:** `internal/jobs/registry.go:488-509` flushes, cancels each job's context, then immediately calls `r.store.Close()`. The per-job `runAttempts` goroutine, woken by the cancel, then calls `finish()` → `flushUnlocked()` → `UpsertJobs` on the now-closed `*sql.DB`; the terminal flush errors and is lost (only re-recovered by a later reconcile).

**Fix:** Track `runAttempts` goroutines with a `sync.WaitGroup`; `Close()` waits on it **after** cancelling and **before** `store.Close()`. Guard the `Add` and the queue-promotion under `r.mu`/`r.closed` so no new goroutine can start once Close has begun (a `WaitGroup.Add` racing `Wait` is itself a bug).

**Files:**
- Modify: `internal/jobs/registry.go:29-53,200-207,216-217,399-418,486-509`
- Test: `internal/jobs/registry_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/jobs/registry_test.go`:

```go
// TestClose_WaitsForTerminalFlush: Close() must not close the store until every
// job goroutine has flushed its terminal state. With the bug, store.Close races
// ahead of finish() and the terminal flush is lost — the persisted row is stuck at
// "running". After the fix the persisted row is terminal (failed, killed by Close).
func TestClose_WaitsForTerminalFlush(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "registry.db")
	fl := newFakeLauncher("sess-Z")
	r := mustRegistry(t, Config{Cap: 4, PersistPath: dbPath}, fl, &fakeCorrelator{}, &fakePruner{})

	rec, err := r.Submit(context.Background(), Spec{Mode: model.ModeRead, CWD: "/p", RunsDir: "/p/runs"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r.waitJob(rec.JobID)

	recs := readBackJobs(t, dbPath)
	if len(recs) != 1 {
		t.Fatalf("want 1 persisted row, got %d", len(recs))
	}
	if !isTerminal(recs[0].Status) {
		t.Fatalf("Close must let the job goroutine flush its terminal state before closing the store; persisted status=%q (want terminal)", recs[0].Status)
	}
}
```

- [ ] **Step 2: Run to verify it fails on current code**

Run: `go test -race -count=1 ./internal/jobs/ -run TestClose_WaitsForTerminalFlush -v`
Expected: FAIL — persisted status `"running"` (the terminal flush hit the closed store and was dropped).

- [ ] **Step 3: Add the WaitGroup field**

In `internal/jobs/registry.go`, in the `Registry` struct (lines 29-53), add a field right after `closed bool` (line 38):

```go
	closed       bool
	wg           sync.WaitGroup // tracks runAttempts goroutines; Close waits on it before store.Close
```

(`sync` is already imported.)

- [ ] **Step 4: Add under-lock + bail in start()**

In `start()`, replace the second locked block + goroutine launch (lines 200-206):

```go
	r.mu.Lock()
	j.Record.PID = pid
	j.updatedAt = r.now()
	_ = r.flushUnlocked()
	r.mu.Unlock()

	go r.runAttempts(j, ctx, cancel, spec, sessionCh, wait)
```

with:

```go
	r.mu.Lock()
	if r.closed {
		// Close() raced ahead: do not spawn a new job goroutine (its terminal flush
		// could hit a closed store). The job is already flushed by Close; cancel its
		// context so the just-launched pi process (group) is reaped.
		r.mu.Unlock()
		cancel()
		return
	}
	j.Record.PID = pid
	j.updatedAt = r.now()
	_ = r.flushUnlocked()
	r.wg.Add(1) // under mu, while !closed, so Close's Wait() observes every started goroutine
	r.mu.Unlock()

	go r.runAttempts(j, ctx, cancel, spec, sessionCh, wait)
```

- [ ] **Step 5: Signal Done from runAttempts**

In `runAttempts` (signature at line 216), add as the **first** statement of the function body:

```go
func (r *Registry) runAttempts(j *Job, ctx context.Context, cancel context.CancelFunc, spec Spec, sessionCh <-chan string, wait func() error) {
	defer r.wg.Done()
	for attempt := 0; ; attempt++ {
```

- [ ] **Step 6: Skip queue promotion once closed**

In `finish()`, replace the promotion block (lines 399-418):

```go
	var next *Job
	if wasRunningSlot {
		// Free the slot, then promote the next queued job (reuse the same slot).
		select {
		case <-r.slots:
		default:
		}
		next = r.dequeueUnlocked()
		if next != nil {
			select {
			case r.slots <- struct{}{}:
				r.startingUnlocked(next)
			default:
				// Should not happen (we just freed one); requeue defensively.
				next.markUnlocked(model.JobQueued, r.now())
				r.queue = append([]*Job{next}, r.queue...)
				next = nil
			}
		}
	}
```

with (only the `if wasRunningSlot` body gains a `!r.closed` guard around the promotion; the slot is still freed):

```go
	var next *Job
	if wasRunningSlot {
		// Free the slot.
		select {
		case <-r.slots:
		default:
		}
		// Do NOT promote a queued job once Close has begun: a goroutine started here
		// (via r.start(next) below) could flush after store.Close(). Queued jobs are
		// left for the next server's reconcile.
		if !r.closed {
			next = r.dequeueUnlocked()
			if next != nil {
				select {
				case r.slots <- struct{}{}:
					r.startingUnlocked(next)
				default:
					// Should not happen (we just freed one); requeue defensively.
					next.markUnlocked(model.JobQueued, r.now())
					r.queue = append([]*Job{next}, r.queue...)
					next = nil
				}
			}
		}
	}
```

- [ ] **Step 7: Wait on the goroutines in Close()**

In `Close()`, insert `r.wg.Wait()` between the cancel loop and `r.store.Close()` (currently lines 504-508):

```go
	for _, c := range cancels {
		c()
	}
	r.wg.Wait() // every job goroutine has now flushed its terminal state; safe to close the store
	_ = r.store.Close()
	return err
```

- [ ] **Step 8: Run the jobs suite to verify green**

Run: `go test -race -count=1 ./internal/jobs/ -v`
Expected: PASS — incl. `TestClose_WaitsForTerminalFlush`, the existing `TestCloseCancelsRunningAndFlushes`, `TestCapQueuesFifthJobAndPromotesFIFO`, `TestSubmit*`. (Watch for deadlocks/races — there should be none: `finish()` waits on `correlated` which closes on ctx-cancel; `Close` waits on `wg` only after releasing `r.mu`.)

- [ ] **Step 9: gofmt + commit**

```bash
gofmt -w internal/jobs/registry.go internal/jobs/registry_test.go
git add internal/jobs/registry.go internal/jobs/registry_test.go
git commit -m "fix(jobs): Close waits for job goroutines before closing the store (no lost terminal flush)"
```

---

## Task 6: Run reconcile periodically, not only at boot (hardening)

**Bug:** `internal/app/app.go:70` reconciles once at startup. A sibling server that dies *after* this one started is never reaped until some server restarts. With Task 1's longer staleness threshold, periodic reconcile is what keeps dead-owner jobs surfacing as `failed` promptly on the dashboard.

**Fix:** A `reconcileLoop(ctx, reconciler, interval, logger)` goroutine, launched from `Run` after the boot reconcile, ticking every 60s until ctx is done. Errors are logged and the loop continues. A small `reconciler` interface keeps it unit-testable without a real DB.

**Files:**
- Modify: `internal/app/app.go:7-19,61-74`
- Test: `internal/app/app_test.go:3-14`

- [ ] **Step 1: Write the failing tests**

In `internal/app/app_test.go`, add `"time"` to the import block (lines 3-14):

```go
import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/jobs"
)
```

Append:

```go
type fakeReconciler struct{ calls chan struct{} }

func (f *fakeReconciler) Reconcile(ctx context.Context) (int, error) {
	select {
	case f.calls <- struct{}{}:
	default:
	}
	return 0, nil
}

func TestReconcileLoop_TicksUntilCtxDone(t *testing.T) {
	fr := &fakeReconciler{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		reconcileLoop(ctx, fr, time.Millisecond, log.New(&bytes.Buffer{}, "", 0))
		close(done)
	}()
	select {
	case <-fr.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcileLoop never ticked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconcileLoop did not stop on ctx cancel")
	}
}

type errReconciler struct{ calls chan struct{} }

func (e errReconciler) Reconcile(ctx context.Context) (int, error) {
	select {
	case e.calls <- struct{}{}:
	default:
	}
	return 0, errors.New("recon boom")
}

func TestReconcileLoop_ContinuesAfterError(t *testing.T) {
	er := errReconciler{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reconcileLoop(ctx, er, time.Millisecond, log.New(&bytes.Buffer{}, "", 0))
	for i := 0; i < 2; i++ { // two ticks => the loop did not die on the first error
		select {
		case <-er.calls:
		case <-time.After(2 * time.Second):
			t.Fatal("reconcileLoop stopped after an error")
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail (undefined symbol)**

Run: `go test -race -count=1 ./internal/app/ -run TestReconcileLoop -v` → FAIL: `undefined: reconcileLoop`.

- [ ] **Step 3: Implement the loop + wiring**

In `internal/app/app.go`, add `"time"` to the import block (lines 7-19):

```go
import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/config"
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/mcpserver"
)
```

After the `serverName`/`serverVersion` const block (line 24), add:

```go
// reconcileInterval is how often the periodic sweep reaps dead-owner jobs. Boot
// reconcile only catches siblings that died before THIS server started; a sibling
// that dies later must be reaped too.
const reconcileInterval = 60 * time.Second

// reconciler is the subset of *jobs.Registry the periodic loop needs (test seam).
type reconciler interface {
	Reconcile(ctx context.Context) (int, error)
}

// reconcileLoop reconciles every interval until ctx is done. A reconcile error is
// logged and the loop continues — a transient DB error must not stop future sweeps.
func reconcileLoop(ctx context.Context, r reconciler, interval time.Duration, logger *log.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.Reconcile(ctx); err != nil {
				logger.Printf("periodic reconcile error: %v (continuing)", err)
			}
		}
	}
}
```

Then, in `Run`, immediately after the boot-reconcile `if/else` block (lines 70-74) and before `js := &jobsAdapter{...}`, add:

```go
	go reconcileLoop(ctx, reg, reconcileInterval, d.Logger)
```

- [ ] **Step 4: Run the app suite to verify green**

Run: `go test -race -count=1 ./internal/app/ -v`
Expected: PASS — incl. the two new loop tests and the existing `TestRun_*` (which pass a `context.Background()` that never cancels; the loop goroutine simply ticks harmlessly and is abandoned when the test process exits — `serve` returns immediately so `Run` returns).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/app/app.go internal/app/app_test.go
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): periodically reconcile dead-owner jobs (not only at boot)"
```

---

## Task 7: Owner-guard the UPSERT conflict clause (hardening)

**Bug:** `internal/jobs/store.go:79-87` `ON CONFLICT(jobId) DO UPDATE` overwrites unconditionally. Each server only upserts its own jobs, so this is latent — but a future caller (or a jobId collision) could clobber a row owned by a *different live* server.

**Fix:** Add `WHERE jobs.ownerPid=excluded.ownerPid OR jobs.ownerPid=0` to the conflict update — only update a row that is mine or unowned. A false predicate makes the conflicting insert a silent no-op (not an error), preserving the foreign live row.

**Files:**
- Modify: `internal/jobs/store.go:79-87`
- Test: `internal/jobs/store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/jobs/store_test.go`:

```go
func TestStore_UpsertOwnerGuard(t *testing.T) {
	s, err := OpenStore(tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertJobs([]model.JobRecord{rec("x", "running")}, 1, "t1"); err != nil {
		t.Fatal(err)
	}
	// A FOREIGN owner (pid 2) must NOT clobber pid 1's live row.
	if err := s.UpsertJobs([]model.JobRecord{rec("x", "completed")}, 2, "t2"); err != nil {
		t.Fatalf("foreign upsert should be a silent no-op, not an error: %v", err)
	}
	recs, owners, err := s.AllJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 row, got %d", len(recs))
	}
	if string(recs[0].Status) != "running" {
		t.Errorf("foreign owner clobbered status -> %q want running", recs[0].Status)
	}
	if owners[0].Pid != 1 {
		t.Errorf("foreign owner changed ownership -> pid %d want 1", owners[0].Pid)
	}
	// The TRUE owner (pid 1) can still update its own row.
	if err := s.UpsertJobs([]model.JobRecord{rec("x", "completed")}, 1, "t1"); err != nil {
		t.Fatal(err)
	}
	recs, _, _ = s.AllJobs()
	if string(recs[0].Status) != "completed" {
		t.Errorf("owner self-update blocked -> %q want completed", recs[0].Status)
	}
}
```

- [ ] **Step 2: Run to verify it fails on current code**

Run: `go test -race -count=1 ./internal/jobs/ -run TestStore_UpsertOwnerGuard -v`
Expected: FAIL — `foreign owner clobbered status -> "completed" want running` (unguarded UPSERT).

- [ ] **Step 3: Implement the guard**

In `internal/jobs/store.go`, replace the `upsertSQL` const (lines 79-87):

```go
const upsertSQL = `
INSERT INTO jobs (jobId,runId,sessionId,mode,cwd,runsDir,worktreePath,branch,pid,status,startedAt,errorCode,errorMessage,ownerPid,ownerStartedAt,updatedAt)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(jobId) DO UPDATE SET
  runId=excluded.runId, sessionId=excluded.sessionId, mode=excluded.mode, cwd=excluded.cwd,
  runsDir=excluded.runsDir, worktreePath=excluded.worktreePath, branch=excluded.branch, pid=excluded.pid,
  status=excluded.status, startedAt=excluded.startedAt, errorCode=excluded.errorCode,
  errorMessage=excluded.errorMessage, ownerPid=excluded.ownerPid, ownerStartedAt=excluded.ownerStartedAt,
  updatedAt=excluded.updatedAt
WHERE jobs.ownerPid=excluded.ownerPid OR jobs.ownerPid=0;`
```

(`jobs.ownerPid` = the existing row's owner; `excluded.ownerPid` = the proposed writer. Update only when mine or unowned.)

- [ ] **Step 4: Run the store + multi-writer suites to verify green**

Run: `go test -race -count=1 ./internal/jobs/ -run 'TestStore' -v`
Expected: PASS — incl. `TestStore_UpsertOwnerGuard`, `TestStore_UpsertAndAll` (self-update owner 100→100 still applies), `TestStore_MultiWriterNoClobber` (distinct jobIds → no conflict), `TestStore_MigratesLegacyJSON`.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/jobs/store.go internal/jobs/store_test.go
git add internal/jobs/store.go internal/jobs/store_test.go
git commit -m "fix(jobs): owner-guard the UPSERT conflict clause (never clobber a foreign live row)"
```

---

## Task 8: Forbid per-agent `timeoutMs` in the forcing prompt (hardening)

**Bug:** The forcing prompt injects `agentTimeoutMs:1200000`, but an orchestrator can still emit `agent(..., {timeoutMs:300000})`, re-introducing the 5-minute kill the fix was meant to remove.

**Fix:** Add an explicit instruction to the template forbidding a per-agent `timeoutMs`.

**Files:**
- Modify: `internal/config/config.go:97-104`
- Test: `internal/config/config_test.go:40-56`

- [ ] **Step 1: Add the failing assertion**

In `internal/config/config_test.go` `TestForcingPromptTemplate`, add `"per-agent timeoutMs"` to the substring slice (lines 42-51):

```go
	for _, sub := range []string{
		"exactly ONE call",
		"`workflow`",
		"background:false",
		"Do not use background:true",
		"INLINE",
		"tokenBudget", // orchestrator must NOT cap the run by tokens (avoids TOKEN_BUDGET_EXHAUSTED)
		"per-agent timeoutMs", // orchestrator must NOT re-introduce the 5-min kill per agent
		"{{CONTRACT}}",
		"{{TASK}}",
		"{{CONTEXT}}",
	} {
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -race -count=1 ./internal/config/ -run TestForcingPromptTemplate -v`
Expected: FAIL — `ForcingPromptTemplate missing "per-agent timeoutMs"`.

- [ ] **Step 3: Implement the template line**

In `internal/config/config.go`, in `ForcingPromptTemplate`, change the "Give agents room…" sentence (lines 103-104) to append the prohibition:

```go
Give agents room to finish: pass agentTimeoutMs:1200000 (20 minutes) to the workflow tool so
coding/TDD agents are not killed by the 5-minute default per-agent timeout. Do NOT set a
per-agent timeoutMs on any agent() call — it overrides agentTimeoutMs and re-introduces the
5-minute kill; rely solely on the single agentTimeoutMs above.
```

- [ ] **Step 4: Run to verify green**

Run: `go test -race -count=1 ./internal/config/ -v`
Expected: PASS (incl. `TestForcingPromptTemplate`).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(prompt): forbid per-agent timeoutMs so orchestrators cannot recreate the 5-min kill"
```

---

## Task 9: Re-add the spec-promised pragmas (hardening)

**Bug:** The writer DSN dropped `foreign_keys(ON)` and the dashboard reader dropped `query_only(1)` (both promised in the SQLite-registry spec).

**Fix:** Add `&_pragma=foreign_keys(ON)` to the writer `OpenStore` DSN and `&_pragma=query_only(1)` to the dashboard `ReadRegistry` DSN (belt-and-suspenders over the existing `mode=ro`).

**Files:**
- Modify: `internal/jobs/store.go:60`, `internal/dashboard/registry.go:26`
- Test: `internal/jobs/store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/jobs/store_test.go`:

```go
func TestStore_ForeignKeysPragmaOn(t *testing.T) {
	s, err := OpenStore(tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys=%d want 1 (ON)", fk)
	}
}
```

- [ ] **Step 2: Run to verify it fails on current code**

Run: `go test -race -count=1 ./internal/jobs/ -run TestStore_ForeignKeysPragmaOn -v`
Expected: FAIL — `foreign_keys=0 want 1`.

- [ ] **Step 3: Implement the writer pragma**

In `internal/jobs/store.go`, change the DSN (line 60):

```go
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
```

- [ ] **Step 4: Implement the dashboard reader pragma**

In `internal/dashboard/registry.go`, change the DSN (line 26):

```go
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)&mode=ro")
```

- [ ] **Step 5: Run to verify green**

Run: `go test -race -count=1 ./internal/jobs/ ./internal/dashboard/ -v`
Expected: PASS — incl. `TestStore_ForeignKeysPragmaOn` and the existing `TestReadRegistry_*` (read paths unaffected by `query_only`).

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/jobs/store.go internal/dashboard/registry.go internal/jobs/store_test.go
git add internal/jobs/store.go internal/dashboard/registry.go internal/jobs/store_test.go
git commit -m "fix(jobs,dashboard): re-add spec pragmas (writer foreign_keys=ON, reader query_only=1)"
```

---

## Task 10: Make legacy-JSON migration atomic (hardening)

**Bug:** `internal/jobs/store.go:162-202` `migrateLegacyJSON` returns an error on a corrupt JSON file (leaving it in place to fail every boot), and its final `os.Rename` errors if a concurrent migrator already renamed the file.

**Fix:** Quarantine a corrupt file (`registry.json.corrupt`) and continue with an empty registry; treat "file already gone" on the final rename (a concurrent migrator won the race) as success.

**Files:**
- Modify: `internal/jobs/store.go:175-201`
- Test: `internal/jobs/store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/jobs/store_test.go`:

```go
func TestStore_MigrationQuarantinesCorrupt(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "registry.db")
	legacy := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(legacy, []byte("{ this is not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Migration runs inside OpenStore; a corrupt file is non-fatal (logged) and the
	// store still opens.
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore should not fail on corrupt legacy JSON: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("corrupt legacy file should be quarantined (renamed away), stat err=%v", err)
	}
	if _, err := os.Stat(legacy + ".corrupt"); err != nil {
		t.Errorf("expected registry.json.corrupt quarantine file, err=%v", err)
	}
	recs, _, _ := s.AllJobs()
	if len(recs) != 0 {
		t.Errorf("corrupt migration must import nothing, got %+v", recs)
	}
}
```

- [ ] **Step 2: Run to verify it fails on current code**

Run: `go test -race -count=1 ./internal/jobs/ -run TestStore_MigrationQuarantinesCorrupt -v`
Expected: FAIL — no `.corrupt` file (current code leaves `registry.json` in place and returns a decode error).

- [ ] **Step 3: Implement quarantine + race-as-success**

In `internal/jobs/store.go` `migrateLegacyJSON`, replace the decode-error block (lines 178-180):

```go
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("decode legacy: %w", err)
	}
```

with:

```go
	if err := json.Unmarshal(data, &pf); err != nil {
		// Corrupt legacy file: quarantine it so we don't retry-and-fail every boot,
		// then continue with an empty registry.
		_ = os.Rename(legacy, legacy+".corrupt")
		return fmt.Errorf("decode legacy (quarantined to %s.corrupt): %w", legacy, err)
	}
```

Then replace the final return (line 201):

```go
	return os.Rename(legacy, legacy+".migrated")
```

with:

```go
	if err := os.Rename(legacy, legacy+".migrated"); err != nil {
		if os.IsNotExist(err) {
			return nil // a concurrent migrator already moved it — not an error
		}
		return err
	}
	return nil
```

- [ ] **Step 4: Run to verify green (incl. the happy-path migration)**

Run: `go test -race -count=1 ./internal/jobs/ -run 'TestStore_Migrat' -v`
Expected: PASS — `TestStore_MigrationQuarantinesCorrupt` and `TestStore_MigratesLegacyJSON`.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/jobs/store.go internal/jobs/store_test.go
git add internal/jobs/store.go internal/jobs/store_test.go
git commit -m "fix(jobs): atomic legacy migration — quarantine corrupt JSON, treat won race as success"
```

---

## Task 11: Full verification gate

- [ ] **Step 1: cgo-free build**

Run: `CGO_ENABLED=0 go build ./...`
Expected: no output (success). This proves we stayed on pure-Go `modernc` (no accidental cgo dependency).

- [ ] **Step 2: vet + format**

Run: `go vet ./... && gofmt -l internal/ cmd/`
Expected: no output from either (gofmt printing nothing == all formatted).

- [ ] **Step 3: full race suite, no cache**

Run: `go test -race -count=1 ./...`
Expected: all packages `ok` (no `FAIL`, no `DATA RACE`).

- [ ] **Step 4: real-pi e2e (spends tokens — run COMPLETE, show full output)**

Run: `PI_MCP_E2E=1 go test ./test/e2e/ -v -count=1`
Expected: PASS. Per the project memory, run it to completion and show the full output (do not grep it down).

- [ ] **Step 5: stop — do NOT auto-deploy**

Deploy is an operator action, not part of this plan. When the user approves, the handoff's deploy recipe is:

```bash
CGO_ENABLED=0 go build -o /usr/local/bin/pi-mcp ./cmd/pi-mcp
CGO_ENABLED=0 go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard && systemctl --user restart pi-dashboard
```

and the rollout caveat still stands (other Claude sessions must reopen to adopt the new binary). Surface this; don't run it unprompted.

---

## Self-review (run against the handoff before executing)

**Spec coverage** — the 4 confirmed HIGHs: staleness→Task 1; `--state-dir`→Task 2; reconcile-prune-vs-orphan→Task 3 (+ root-cause Task 4); `Close()` race→Task 5. Cheap hardening: owner-guard→Task 7; periodic reconcile→Task 6; forbid per-agent `timeoutMs`→Task 8; pragmas→Task 9; migration atomicity→Task 10. Deferred-and-documented: `ownerStartedAt` PID-reuse tiebreak; dashboard single-handle refactor (Task 9 still adds `query_only`).

**Placeholder scan** — every code step shows the exact replacement text and the exact file:line anchor; every test step shows the full test body; expected pass/fail output is stated for each run.

**Type/name consistency** — `RegistryPathFor` (config) and `registryPathFor` (cmd) are distinct by package; `reconciler`/`reconcileLoop`/`reconcileInterval` are introduced together in Task 6 and used consistently; `fakeReconciler`/`errReconciler` don't collide with `app_test.go`'s existing `newTestRegistry`; `wg` is added to the `Registry` struct and used in `start`/`runAttempts`/`Close`; `ForcedAgentTimeoutMs` is referenced by `StaleThreshold` and the config test.

**Regression-test direction** — each new test is asserted to FAIL on current code (Step 2 of each task) and PASS after the change, and the threshold bump's two collateral test edits (`config_test` constant, `state_test` hardcoded 600s) are handled in Task 1.
