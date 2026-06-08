# pi-mcp Control-Plane Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `pi-dashboard` — a standalone, always-online, read-only web service that visualizes pi-mcp workflows in realtime (submission → live agent fan-out → final result), accessed remotely over Tailscale.

**Architecture:** A separate Go binary in this repo reads the state pi-mcp/pi already persist: the `registry.json` job index and the per-job, scattered run files (`<RunsDir>/<RunID>.json`). A 1s poll loop rebuilds a view-model, an SSE hub pushes a **light** job list to browsers, and heavy per-job detail is fetched on demand. It binds directly to the host's Tailscale IPv4 and runs as a systemd **user** unit. It never writes any pi-mcp/pi file.

**Tech Stack:** Go 1.26 (stdlib `net/http`, `go:embed`, `os/exec`, stdlib `testing` — **no testify**), vanilla HTML/CSS/JS (no build step), systemd user unit. Reuses existing packages `internal/model`, `internal/runstore`, `internal/config`.

**Reference spec:** `docs/superpowers/specs/2026-06-08-pi-mcp-dashboard-design.md`

---

## Conventions (read once)

- Module path is `pi-mcp`. Imports look like `pi-mcp/internal/model`.
- Tests use the stdlib `testing` package only. No assertion libraries. Pattern: `if got != want { t.Errorf(...) }`. Fixtures live in a `testdata/` dir next to the package.
- Run the full suite with the race detector: `go test -race ./...`.
- Build the dashboard binary: `go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard`.
- Commit after every task with the shown message. You are on branch `build/dashboard`.

### Key existing types you will reuse (already defined in `internal/model/model.go`)

- `model.Run` — decoded run file. Fields used: `RunID, WorkflowName, Status, CurrentPhase *string, Phases []string, Agents []Agent, Journal []JournalEntry, Result json.RawMessage, CompletedAt *time.Time, UpdatedAt *time.Time, DurationMs *int64, TokenUsage *TokenUsage`.
- `model.Agent` — `ID, CallIndex, Label, Model, Phase, Prompt, Status, ResultPreview, Tokens int64, StartedAt *time.Time, EndedAt *time.Time, Error *string`.
- `model.TokenUsage` — `Input, Output, Total int64, Cost float64, CacheRead, CacheWrite int64`.
- `model.IntermediateResult` — `Label, Model, Phase string, Result any, Preview string, Truncated bool`.
- `model.JobRecord` — persisted registry record: `JobID, RunID, SessionID string, Mode JobMode, CWD, RunsDir, WorktreePath, Branch string, PID int, Status JobStatus, StartedAt time.Time, ErrorCode, ErrorMessage string`.
- `model.JobMode` consts `ModeRead`/`ModeWrite` ("read"/"write"); `model.JobStatus` consts `JobQueued/JobRunning/JobCompleted/JobFailed/JobAborted`.

### Existing reuse functions

- `runstore.ReadRun(path string) (*model.Run, error)` — decodes a run file, `.bak` fallback.
- `runstore.ModelHistogram(r *model.Run) map[string]int` — agents-per-model.
- `runstore.Intermediates(r *model.Run, maxBytes int) []model.IntermediateResult` — journal↔agent join, truncates > maxBytes.
- `config.MaxInlineResultBytes` (16384), `config.StaleThreshold` (300s), `config.RunsDirRel` (".pi/workflows/runs").

---

## File Structure

**New shared (small, additive refactors to prevent status/path drift):**
- `internal/livestatus/livestatus.go` — pure status derivation (`MapDisk`, `IsTerminal`, `Derive`), extracted from `mcpserver/status_map.go`.
- `internal/config/config.go` — add `StateDir()` + `RegistryPath()`.

**Modified (delegate to the shared code):**
- `internal/mcpserver/status_map.go` — `mapDiskStatus`/`isTerminal`/`liveStatus` become thin wrappers over `livestatus`.
- `internal/app/app.go` — `xdgStateDir`/`statePaths` use `config.StateDir`/`config.RegistryPath`.

**New dashboard package:**
- `internal/dashboard/registry.go` — read+decode `registry.json` → `[]model.JobRecord`.
- `internal/dashboard/worktree.go` — newest-mtime worktree-activity signal (write-job liveness).
- `internal/dashboard/state.go` — view-model types + light/heavy builders.
- `internal/dashboard/poller.go` — 1s trigger loop, terminal-run cache, change detection.
- `internal/dashboard/hub.go` — SSE broadcaster.
- `internal/dashboard/server.go` — HTTP handlers + `go:embed` of `web/`.
- `internal/dashboard/tailscale.go` — detect Tailscale IPv4 + wait-for-tailnet.
- `internal/dashboard/web/{index.html,app.css,app.js}` — the SPA.
- `internal/dashboard/testdata/{completed.json,running.json,paused.json,registry.json}` — fixtures.

**Entrypoint & deploy:**
- `cmd/pi-dashboard/main.go` — flags, wiring, serve.
- `deploy/pi-dashboard.service` — systemd user unit.
- `deploy/README.md` — install steps.

---

## Task 1: Shared `livestatus` package (extract status derivation)

**Files:**
- Create: `internal/livestatus/livestatus.go`
- Create: `internal/livestatus/livestatus_test.go`
- Modify: `internal/mcpserver/status_map.go`

- [ ] **Step 1: Write the failing test**

`internal/livestatus/livestatus_test.go`:
```go
package livestatus

import (
	"testing"
	"time"

	"pi-mcp/internal/config"
)

func TestMapDisk(t *testing.T) {
	cases := map[string]string{
		"completed": "completed", "failed": "failed", "aborted": "aborted",
		"running": "running", "paused": "running", "weird": "running",
	}
	for disk, want := range cases {
		if got := MapDisk(disk); got != want {
			t.Errorf("MapDisk(%q)=%q want %q", disk, got, want)
		}
	}
}

func TestDerive(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-2 * config.StaleThreshold)

	// terminal disk status passes through.
	if got := Derive("completed", &fresh, now, true, false); got != "completed" {
		t.Errorf("completed -> %q", got)
	}
	// running + fresh + alive -> running.
	if got := Derive("running", &fresh, now, true, false); got != "running" {
		t.Errorf("fresh running -> %q want running", got)
	}
	// running + stale -> failed.
	if got := Derive("running", &stale, now, true, false); got != "failed" {
		t.Errorf("stale running -> %q want failed", got)
	}
	// running + stale BUT worktree active -> running.
	if got := Derive("running", &stale, now, true, true); got != "running" {
		t.Errorf("stale+worktreeActive -> %q want running", got)
	}
	// dead pid wins over everything non-terminal.
	if got := Derive("running", &fresh, now, false, true); got != "failed" {
		t.Errorf("dead pid -> %q want failed", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/livestatus/`
Expected: FAIL (package/functions not defined).

- [ ] **Step 3: Write the implementation**

`internal/livestatus/livestatus.go`:
```go
// Package livestatus is the single source of truth for mapping a pi run-file
// status to the MCP/dashboard status vocabulary and applying the liveness
// override (staleness + worktree activity). Both internal/mcpserver and
// internal/dashboard use it so the two readers never drift.
package livestatus

import (
	"time"

	"pi-mcp/internal/config"
)

// MapDisk maps a run-file status to the surfaced status. paused (non-terminal)
// collapses to running.
func MapDisk(disk string) string {
	switch disk {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "aborted":
		return "aborted"
	default: // running, paused, or any unknown non-terminal
		return "running"
	}
}

// IsTerminal reports whether a mapped status is terminal.
func IsTerminal(mapped string) bool {
	switch mapped {
	case "completed", "failed", "aborted":
		return true
	default:
		return false
	}
}

// Derive applies the liveness override to a disk status. A non-terminal status
// whose updatedAt is older than config.StaleThreshold, OR whose process is
// confirmed dead, becomes "failed". A recently-active worktree overrides
// run-file staleness (a direct-editing write job freezes its run file while
// alive). A confirmed-dead pid still wins. pidAlive==true means "alive or
// unknown" (callers with no liveness signal pass true).
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
		return "failed"
	}
	return mapped
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/livestatus/`
Expected: PASS.

- [ ] **Step 5: Refactor mcpserver to delegate (keep existing tests green)**

Replace the bodies in `internal/mcpserver/status_map.go` so they delegate (keep the same unexported names + signatures so the rest of mcpserver and its tests are untouched):
```go
package mcpserver

import (
	"time"

	"pi-mcp/internal/livestatus"
)

// mapDiskStatus maps run-file status -> MCP status. paused (non-terminal) -> running.
func mapDiskStatus(disk string) string { return livestatus.MapDisk(disk) }

func isTerminal(mcpStatus string) bool { return livestatus.IsTerminal(mcpStatus) }

// liveStatus applies the §5.2 liveness override. See livestatus.Derive.
func liveStatus(disk string, updatedAt *time.Time, now time.Time, pidAlive, worktreeActive bool) string {
	return livestatus.Derive(disk, updatedAt, now, pidAlive, worktreeActive)
}
```

- [ ] **Step 6: Run the full suite to confirm no regression**

Run: `go test -race ./internal/livestatus/ ./internal/mcpserver/`
Expected: PASS (existing `status_map_test.go` still green via the delegation).

- [ ] **Step 7: Commit**

```bash
git add internal/livestatus/ internal/mcpserver/status_map.go
git commit -m "refactor: extract shared livestatus package (mcpserver delegates)"
```

---

## Task 2: Shared state-path helpers in `config`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/app/app.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:
```go
func TestStateDir_XDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got := StateDir(); got != "/xdg/state" {
		t.Errorf("StateDir()=%q want /xdg/state", got)
	}
}

func TestStateDir_HomeFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	want := "/home/u/.local/state"
	if got := StateDir(); got != want {
		t.Errorf("StateDir()=%q want %q", got, want)
	}
}

func TestRegistryPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	want := "/xdg/state/pi-mcp/registry.json"
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath()=%q want %q", got, want)
	}
}
```
(If `internal/config/config_test.go` does not exist, create it with `package config` and the standard imports `"testing"`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL (StateDir/RegistryPath undefined).

- [ ] **Step 3: Add the helpers**

Append to `internal/config/config.go` (add `"os"` and `"path/filepath"` to its import block):
```go
// StateDir resolves pi-mcp's state base dir: $XDG_STATE_HOME, else
// $HOME/.local/state, else the OS temp dir. This is the single source of truth
// shared by the MCP server (internal/app) and the dashboard.
func StateDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return xdg
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state")
	}
	return os.TempDir()
}

// RegistryPath is the job-registry file: <StateDir>/pi-mcp/registry.json.
func RegistryPath() string {
	return filepath.Join(StateDir(), "pi-mcp", "registry.json")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Refactor app.go to use them**

In `internal/app/app.go`, change `statePaths()` to use the shared helpers and delete the now-duplicate `xdgStateDir`:
```go
func statePaths() (persist, worktreeRoot string, err error) {
	base := config.StateDir()
	root := filepath.Join(base, config.WorktreeSubdir)
	if mkErr := os.MkdirAll(root, 0o755); mkErr != nil {
		return "", "", fmt.Errorf("create state dir: %w", mkErr)
	}
	return config.RegistryPath(), root, nil
}
```
Then delete the `xdgStateDir` function from `app.go` (it is now unused). Leave the rest of `app.go` unchanged.

- [ ] **Step 6: Run the suite to confirm no regression**

Run: `go test -race ./internal/config/ ./internal/app/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config/ internal/app/app.go
git commit -m "refactor: share state-path resolution via config.StateDir/RegistryPath"
```

---

## Task 3: Dashboard registry reader

**Files:**
- Create: `internal/dashboard/registry.go`
- Create: `internal/dashboard/registry_test.go`
- Create: `internal/dashboard/testdata/registry.json`

- [ ] **Step 1: Create the fixture**

`internal/dashboard/testdata/registry.json`:
```json
{
  "jobs": [
    {
      "jobId": "job-completed", "runId": "completed", "mode": "read",
      "cwd": "/tmp/proj", "runsDir": "TESTDATA_PLACEHOLDER",
      "pid": 111, "status": "completed",
      "startedAt": "2026-06-07T16:51:33.041Z"
    },
    {
      "jobId": "job-running", "runId": "running", "mode": "read",
      "cwd": "/tmp/proj", "runsDir": "TESTDATA_PLACEHOLDER",
      "pid": 222, "status": "running",
      "startedAt": "2026-06-07T16:51:40.000Z"
    },
    {
      "jobId": "job-blind", "runId": "", "mode": "read",
      "cwd": "/tmp/proj", "runsDir": "TESTDATA_PLACEHOLDER",
      "pid": 333, "status": "running",
      "startedAt": "2026-06-07T16:51:50.000Z"
    },
    {
      "jobId": "job-queued", "runId": "", "mode": "write",
      "cwd": "/tmp/proj", "runsDir": "TESTDATA_PLACEHOLDER",
      "worktreePath": "/tmp/wt/job-queued", "branch": "pi-mcp/job-queued",
      "pid": 0, "status": "queued",
      "startedAt": "2026-06-07T16:51:52.000Z"
    }
  ]
}
```
(The `runsDir` values are overwritten at test runtime to point at `testdata`; see the test.)

- [ ] **Step 2: Write the failing test**

`internal/dashboard/registry_test.go`:
```go
package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/model"
)

func TestReadRegistry_Missing(t *testing.T) {
	recs, err := ReadRegistry(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should be no error, got %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("want 0 records, got %d", len(recs))
	}
}

func TestReadRegistry_Decodes(t *testing.T) {
	recs, err := ReadRegistry(filepath.Join("testdata", "registry.json"))
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("want 4 records, got %d", len(recs))
	}
	if recs[0].JobID != "job-completed" || recs[0].Status != model.JobCompleted {
		t.Errorf("record[0] = %+v", recs[0])
	}
	if recs[3].Mode != model.ModeWrite || recs[3].Branch != "pi-mcp/job-queued" {
		t.Errorf("record[3] = %+v", recs[3])
	}
}

func TestReadRegistry_CorruptIsError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "reg.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegistry(p); err == nil {
		t.Errorf("corrupt registry should error")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/dashboard/`
Expected: FAIL (ReadRegistry undefined).

- [ ] **Step 4: Write the implementation**

`internal/dashboard/registry.go`:
```go
// Package dashboard implements pi-dashboard: a read-only realtime viewer of
// pi-mcp workflows. It reads the registry.json job index and the per-job run
// files, derives a view-model, and serves it over HTTP + SSE.
package dashboard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"pi-mcp/internal/model"
)

// persistedRegistry mirrors the on-disk shape written by internal/jobs.
type persistedRegistry struct {
	Jobs []model.JobRecord `json:"jobs"`
}

// ReadRegistry decodes the registry file into job records. A missing file is not
// an error (the pi-mcp server may not have run yet) and yields an empty slice. A
// present-but-corrupt file IS an error (callers keep their last good state). The
// pi-mcp server writes the registry via atomic rename, so a successful read is
// always a complete file.
func ReadRegistry(path string) ([]model.JobRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []model.JobRecord{}, nil
		}
		return nil, err
	}
	var pr persistedRegistry
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, err
	}
	if pr.Jobs == nil {
		return []model.JobRecord{}, nil
	}
	return pr.Jobs, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/dashboard/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/registry.go internal/dashboard/registry_test.go internal/dashboard/testdata/registry.json
git commit -m "feat(dashboard): registry.json reader"
```

---

## Task 4: Worktree-activity signal (write-job liveness)

**Files:**
- Create: `internal/dashboard/worktree.go`
- Create: `internal/dashboard/worktree_test.go`

- [ ] **Step 1: Write the failing test**

`internal/dashboard/worktree_test.go`:
```go
package dashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeActive_RecentFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !WorktreeActive(dir, time.Now()) {
		t.Errorf("freshly written worktree should be active")
	}
}

func TestWorktreeActive_SkipsGitAndPi(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{".git", ".pi"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Only bookkeeping dirs touched now -> not "active" relative to a now far in
	// the future (the real worktree files are old / absent).
	future := time.Now().Add(48 * time.Hour)
	if WorktreeActive(dir, future) {
		t.Errorf(".git/.pi churn must not count as activity")
	}
}

func TestWorktreeActive_MissingDir(t *testing.T) {
	if WorktreeActive("", time.Now()) {
		t.Errorf("empty path -> not active")
	}
	if WorktreeActive(filepath.Join(t.TempDir(), "nope"), time.Now()) {
		t.Errorf("missing dir -> not active")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestWorktreeActive`
Expected: FAIL (WorktreeActive undefined).

- [ ] **Step 3: Write the implementation**

`internal/dashboard/worktree.go`:
```go
package dashboard

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"pi-mcp/internal/config"
)

// WorktreeActive reports whether a write job's worktree shows recent activity:
// the newest mtime under dir (excluding the .git and .pi bookkeeping trees,
// which churn independently of the agent's work) is within ±StaleThreshold of
// now. A small future mtime is plausible clock skew and still counts; a mtime
// far in the future is corrupt, not liveness. Empty/missing dir -> false.
func WorktreeActive(dir string, now time.Time) bool {
	if dir == "" {
		return false
	}
	var newest time.Time
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if path != dir && (base == ".git" || base == ".pi") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+".pi"+string(filepath.Separator)) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if newest.IsZero() {
		return false
	}
	d := now.Sub(newest)
	if d < 0 {
		d = -d
	}
	return d <= config.StaleThreshold
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestWorktreeActive`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/worktree.go internal/dashboard/worktree_test.go
git commit -m "feat(dashboard): worktree-activity liveness signal"
```

---

## Task 5: View-model + state builder (the core)

**Files:**
- Create: `internal/dashboard/state.go`
- Create: `internal/dashboard/state_test.go`
- Create: `internal/dashboard/testdata/completed.json` (copy of `internal/runstore/testdata/sample-run.json`)
- Create: `internal/dashboard/testdata/running.json` (copy of `internal/runstore/testdata/sample-run-partial.json`)
- Create: `internal/dashboard/testdata/paused.json` (copy of `internal/runstore/testdata/sample-run-partial-paused.json`)

- [ ] **Step 1: Copy the fixtures**

```bash
cp internal/runstore/testdata/sample-run.json          internal/dashboard/testdata/completed.json
cp internal/runstore/testdata/sample-run-partial.json  internal/dashboard/testdata/running.json
cp internal/runstore/testdata/sample-run-partial-paused.json internal/dashboard/testdata/paused.json
```
These match the registry fixture's `runId` values (`completed`, `running`) so `loadRun` resolves `<runsDir>/<runId>.json`.

- [ ] **Step 2: Write the failing test**

`internal/dashboard/state_test.go`:
```go
package dashboard

import (
	"testing"
	"time"

	"pi-mcp/internal/model"
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestBuild`
Expected: FAIL (types/functions undefined).

- [ ] **Step 4: Write the implementation**

`internal/dashboard/state.go`:
```go
package dashboard

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"sort"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/livestatus"
	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
)

// Counts is the aggregate job tally for the overview.
type Counts struct {
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Aborted   int `json:"aborted"`
	Total     int `json:"total"`
}

// JobSummary is the light per-job row pushed over SSE.
type JobSummary struct {
	JobID        string         `json:"jobId"`
	Mode         string         `json:"mode"`
	Status       string         `json:"status"` // displayed (liveness-adjusted)
	WorkflowName string         `json:"workflowName,omitempty"`
	CWD          string         `json:"cwd"`
	WorktreePath string         `json:"worktreePath,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	RunID        string         `json:"runId,omitempty"`
	StartedAt    time.Time      `json:"startedAt"`
	CompletedAt  *time.Time     `json:"completedAt,omitempty"`
	BlindWindow  bool           `json:"blindWindow"`
	Phase        string         `json:"phase,omitempty"`
	AgentsDone   int            `json:"agentsDone"`
	AgentsTotal  int            `json:"agentsTotal"`
	FleetByModel map[string]int `json:"fleetByModel,omitempty"`
	LiveTokens   int64          `json:"liveTokens"`
	Cost         *float64       `json:"cost,omitempty"`
	DurationMs   *int64         `json:"durationMs,omitempty"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
}

// DashboardState is the top-level light snapshot pushed over SSE.
type DashboardState struct {
	GeneratedAt time.Time    `json:"generatedAt"`
	StateDir    string       `json:"stateDir"`
	Counts      Counts       `json:"counts"`
	Jobs        []JobSummary `json:"jobs"`
}

// AgentView is one fleet card in the heavy detail.
type AgentView struct {
	Label         string     `json:"label"`
	Model         string     `json:"model"`
	Phase         string     `json:"phase"`
	Status        string     `json:"status"`
	Tokens        int64      `json:"tokens"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	Error         string     `json:"error,omitempty"`
	Prompt        string     `json:"prompt,omitempty"`
	ResultPreview string     `json:"resultPreview,omitempty"`
}

// JobDetail is the heavy per-job view fetched on demand.
type JobDetail struct {
	JobSummary
	Phases       []string                   `json:"phases,omitempty"`
	Agents       []AgentView                `json:"agents"`
	Intermediate []model.IntermediateResult `json:"intermediate"`
	TokenUsage   *model.TokenUsage          `json:"tokenUsage,omitempty"`
	Result       any                        `json:"result,omitempty"`
}

// readRun is the run-file loader seam (overridable in tests). It builds the path
// <runsDir>/<runId>.json and decodes it with runstore (which falls back to .bak).
var readRun = func(runsDir, runID string) (*model.Run, error) {
	if runID == "" {
		return nil, fs.ErrNotExist
	}
	return runstore.ReadRun(filepath.Join(runsDir, runID+".json"))
}

// BuildState derives the light snapshot from the registry records.
func BuildState(recs []model.JobRecord, stateDir string, now time.Time) DashboardState {
	st := DashboardState{GeneratedAt: now, StateDir: stateDir, Jobs: make([]JobSummary, 0, len(recs))}
	for i := range recs {
		js := summarize(recs[i], now)
		st.Jobs = append(st.Jobs, js)
		st.Counts.Total++
		switch js.Status {
		case "running":
			st.Counts.Running++
		case "queued":
			st.Counts.Queued++
		case "completed":
			st.Counts.Completed++
		case "failed":
			st.Counts.Failed++
		case "aborted":
			st.Counts.Aborted++
		}
	}
	sort.SliceStable(st.Jobs, func(a, b int) bool {
		ta, tb := isTerminalStatus(st.Jobs[a].Status), isTerminalStatus(st.Jobs[b].Status)
		if ta != tb {
			return !ta // active (non-terminal) first
		}
		return st.Jobs[a].StartedAt.After(st.Jobs[b].StartedAt)
	})
	return st
}

func isTerminalStatus(s string) bool { return livestatus.IsTerminal(s) }

// summarize derives one JobSummary, mirroring mcpserver.buildStatus precedence:
// run file present -> livestatus.Derive(run.Status,...); run file absent ->
// registry status (terminal surfaced; running -> blind with StartedAt staleness;
// queued -> queued).
func summarize(rec model.JobRecord, now time.Time) JobSummary {
	js := JobSummary{
		JobID: rec.JobID, Mode: string(rec.Mode), CWD: rec.CWD,
		WorktreePath: rec.WorktreePath, Branch: rec.Branch, RunID: rec.RunID,
		StartedAt: rec.StartedAt, ErrorCode: rec.ErrorCode, ErrorMessage: rec.ErrorMessage,
	}
	worktreeActive := rec.Mode == model.ModeWrite && WorktreeActive(rec.WorktreePath, now)

	run, err := readRun(rec.RunsDir, rec.RunID)
	if err != nil || run == nil {
		// No run file. Surface registry status.
		switch rec.Status {
		case model.JobQueued:
			js.Status = "queued"
		case model.JobRunning:
			if !worktreeActive && now.Sub(rec.StartedAt) > config.StaleThreshold {
				js.Status = "failed"
				if js.ErrorCode == "" {
					js.ErrorCode = config.ErrServerRestarted
				}
			} else {
				js.Status = "running"
				js.BlindWindow = true
			}
		default: // terminal
			js.Status = string(rec.Status)
		}
		return js
	}

	// Run file present.
	js.RunID = run.RunID
	js.WorkflowName = run.WorkflowName
	if run.CurrentPhase != nil {
		js.Phase = *run.CurrentPhase
	}
	js.Status = livestatus.Derive(run.Status, run.UpdatedAt, now, true, worktreeActive)
	js.AgentsTotal = len(run.Agents)
	for i := range run.Agents {
		if run.Agents[i].Status == "done" {
			js.AgentsDone++
		}
		js.LiveTokens += run.Agents[i].Tokens
	}
	fleet := runstore.ModelHistogram(run)
	if len(fleet) > 0 {
		js.FleetByModel = fleet
	}
	js.CompletedAt = run.CompletedAt
	js.DurationMs = run.DurationMs
	if run.TokenUsage != nil {
		c := run.TokenUsage.Cost
		js.Cost = &c
	}
	return js
}

// BuildDetail derives the heavy per-job view. ok is false only for an unknown
// record; a blind-window job (no run file yet) still returns ok with a
// summary-only detail.
func BuildDetail(rec model.JobRecord, now time.Time) (JobDetail, bool) {
	d := JobDetail{JobSummary: summarize(rec, now), Agents: []AgentView{}, Intermediate: []model.IntermediateResult{}}
	run, err := readRun(rec.RunsDir, rec.RunID)
	if err != nil || run == nil {
		return d, true // blind / no run file: summary only
	}
	d.Phases = run.Phases
	d.Agents = make([]AgentView, 0, len(run.Agents))
	for i := range run.Agents {
		a := run.Agents[i]
		av := AgentView{
			Label: a.Label, Model: a.Model, Phase: a.Phase, Status: a.Status,
			Tokens: a.Tokens, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
			Prompt: a.Prompt, ResultPreview: a.ResultPreview,
		}
		if a.Error != nil {
			av.Error = *a.Error
		}
		d.Agents = append(d.Agents, av)
	}
	d.Intermediate = runstore.Intermediates(run, config.MaxInlineResultBytes)
	d.TokenUsage = run.TokenUsage
	if livestatus.IsTerminal(d.Status) && d.Status == "completed" {
		d.Result = rawToAny(run.Result)
	}
	return d, true
}

// rawToAny decodes a json.RawMessage into an any (object/array/scalar); empty or
// invalid -> nil.
func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestBuild`
Expected: PASS.

- [ ] **Step 6: Run the whole dashboard package**

Run: `go test -race ./internal/dashboard/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/state.go internal/dashboard/state_test.go internal/dashboard/testdata/
git commit -m "feat(dashboard): view-model + state builder with golden tests"
```

---

## Task 6: Poller (1s trigger, change detection, latest-state cache)

**Files:**
- Create: `internal/dashboard/poller.go`
- Create: `internal/dashboard/poller_test.go`

- [ ] **Step 1: Write the failing test**

`internal/dashboard/poller_test.go`:
```go
package dashboard

import (
	"sync"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

type captureSink struct {
	mu sync.Mutex
	n  int
}

func (c *captureSink) Broadcast([]byte) { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *captureSink) count() int       { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func TestPoller_TickBroadcastsOnChange(t *testing.T) {
	recs := recsForTest()
	sink := &captureSink{}
	p := NewPoller("testdata/registry.json", "/state", sink)
	p.now = func() time.Time { return nowFresh }

	p.Tick()                 // first build -> broadcast
	if sink.count() != 1 {
		t.Fatalf("first tick broadcasts once, got %d", sink.count())
	}
	p.Tick()                 // identical -> no broadcast
	if sink.count() != 1 {
		t.Errorf("unchanged tick must not broadcast, got %d", sink.count())
	}
	_ = recs
}

func TestPoller_LatestState(t *testing.T) {
	p := NewPoller("testdata/registry.json", "/state", &captureSink{})
	// registry.json points runsDir at a placeholder; override the reader so the
	// builder sees our deterministic records.
	p.readRegistry = func(string) ([]model.JobRecord, error) { return recsForTest(), nil }
	p.now = func() time.Time { return nowFresh }
	p.Tick()
	got := p.Latest()
	if got.Counts.Total != 4 {
		t.Errorf("latest total=%d want 4", got.Counts.Total)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestPoller`
Expected: FAIL (NewPoller/Poller undefined).

- [ ] **Step 3: Write the implementation**

`internal/dashboard/poller.go`:
```go
package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"pi-mcp/internal/model"
)

// Sink receives serialized state snapshots (implemented by *Hub).
type Sink interface {
	Broadcast([]byte)
}

// Poller rebuilds the light DashboardState on an interval, caches the latest
// snapshot for one-shot reads (/api/state, new SSE clients), and broadcasts to
// the Sink only when the snapshot changed (the hash excludes the wall clock, so
// an idle fleet does not push every second).
type Poller struct {
	registryPath string
	stateDir     string
	sink         Sink
	interval     time.Duration

	now          func() time.Time
	readRegistry func(string) ([]model.JobRecord, error)

	mu       sync.Mutex
	latest   DashboardState
	lastHash [32]byte
	primed   bool
}

// NewPoller builds a Poller with production defaults (1s interval, real readers).
func NewPoller(registryPath, stateDir string, sink Sink) *Poller {
	return &Poller{
		registryPath: registryPath,
		stateDir:     stateDir,
		sink:         sink,
		interval:     time.Second,
		now:          time.Now,
		readRegistry: ReadRegistry,
	}
}

// Run ticks until ctx is done. The first tick happens immediately.
func (p *Poller) Run(ctx context.Context) {
	p.Tick()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Tick()
		}
	}
}

// Tick rebuilds the snapshot and broadcasts if it changed. A registry read error
// keeps the last good snapshot (never blanks the UI).
func (p *Poller) Tick() {
	recs, err := p.readRegistry(p.registryPath)
	if err != nil {
		return // keep last good state
	}
	st := BuildState(recs, p.stateDir, p.now())

	// Hash everything except the wall clock so identical fleets do not push.
	hashable := st
	hashable.GeneratedAt = time.Time{}
	b, _ := json.Marshal(hashable)
	sum := sha256.Sum256(b)

	full, _ := json.Marshal(st)

	p.mu.Lock()
	p.latest = st
	changed := !p.primed || sum != p.lastHash
	p.lastHash = sum
	p.primed = true
	p.mu.Unlock()

	if changed {
		p.sink.Broadcast(full)
	}
}

// Latest returns the most recent snapshot (for /api/state and new SSE clients).
func (p *Poller) Latest() DashboardState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest
}

// LatestJSON returns the most recent snapshot already serialized.
func (p *Poller) LatestJSON() []byte {
	b, _ := json.Marshal(p.Latest())
	return b
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestPoller`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/poller.go internal/dashboard/poller_test.go
git commit -m "feat(dashboard): poller with change-detection + latest-state cache"
```

---

## Task 7: SSE hub

**Files:**
- Create: `internal/dashboard/hub.go`
- Create: `internal/dashboard/hub_test.go`

- [ ] **Step 1: Write the failing test**

`internal/dashboard/hub_test.go`:
```go
package dashboard

import (
	"testing"
	"time"
)

func TestHub_BroadcastReachesSubscriber(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.Broadcast([]byte("hello"))
	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Errorf("got %q want hello", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive broadcast")
	}
}

func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	cancel()
	h.Broadcast([]byte("x"))
	// after cancel the channel is closed/drained; a recv must not block forever.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("closed subscriber channel never returned")
	}
	if got := h.Count(); got != 0 {
		t.Errorf("after unsubscribe Count()=%d want 0", got)
	}
}

func TestHub_SlowSubscriberDropped(t *testing.T) {
	h := NewHub()
	_, _ = h.Subscribe() // never drained; buffer is small
	// Broadcasting more than the buffer must not block the hub.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Broadcast([]byte("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a slow subscriber")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestHub`
Expected: FAIL (NewHub undefined).

- [ ] **Step 3: Write the implementation**

`internal/dashboard/hub.go`:
```go
package dashboard

import "sync"

// hubBuffer is the per-subscriber send buffer. Broadcast never blocks: a
// subscriber whose buffer is full simply drops the frame (it will catch up on
// the next snapshot, which is a full state, not a delta).
const hubBuffer = 8

// Hub fans out serialized state snapshots to all connected SSE clients.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub builds an empty Hub.
func NewHub() *Hub { return &Hub{subs: make(map[chan []byte]struct{})} }

// Subscribe registers a new client. The returned cancel removes the
// subscription and closes the channel (idempotent).
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, hubBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Broadcast sends msg to every subscriber, dropping the frame for any whose
// buffer is full. Never blocks.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default: // slow client: drop this frame
		}
	}
}

// Count returns the number of active subscribers.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/dashboard/ -run TestHub`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/hub.go internal/dashboard/hub_test.go
git commit -m "feat(dashboard): non-blocking SSE hub"
```

---

## Task 8: HTTP server + embedded assets

**Files:**
- Create: `internal/dashboard/web/index.html` (placeholder; real UI in Task 11)
- Create: `internal/dashboard/web/app.css` (placeholder)
- Create: `internal/dashboard/web/app.js` (placeholder)
- Create: `internal/dashboard/server.go`
- Create: `internal/dashboard/server_test.go`

- [ ] **Step 1: Create placeholder web assets (so `go:embed` compiles)**

`internal/dashboard/web/index.html`:
```html
<!doctype html><html><head><meta charset="utf-8"><title>pi-mcp control plane</title>
<link rel="stylesheet" href="/static/app.css"></head>
<body><div id="app">loading…</div><script src="/static/app.js"></script></body></html>
```
`internal/dashboard/web/app.css`:
```css
/* replaced in Task 11 */
body { font-family: system-ui, sans-serif; }
```
`internal/dashboard/web/app.js`:
```js
/* replaced in Task 11 */
console.log("pi-dashboard");
```

- [ ] **Step 2: Write the failing test**

`internal/dashboard/server_test.go`:
```go
package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	hub := NewHub()
	p := NewPoller("testdata/registry.json", "/state", hub)
	p.readRegistry = func(string) ([]model.JobRecord, error) { return recsForTest(), nil }
	p.now = func() time.Time { return nowFresh }
	p.Tick()
	return NewServer(p, hub)
}

func TestServer_Index(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "control plane") {
		t.Errorf("index body missing title")
	}
}

func TestServer_APIState(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type=%q", ct)
	}
	var st DashboardState
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Counts.Total != 4 {
		t.Errorf("total=%d want 4", st.Counts.Total)
	}
}

func TestServer_APIJob(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/job/job-completed", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var d JobDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Agents) != 4 {
		t.Errorf("agents=%d want 4", len(d.Agents))
	}
}

func TestServer_APIJob_Unknown(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/job/nope", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("unknown job status=%d want 404", w.Code)
	}
}

func TestServer_Events_StreamsInitial(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}
	// Read the initial frame (the current snapshot) then bail.
	buf := make([]byte, 64)
	done := make(chan struct{})
	go func() { _, _ = io.ReadAtLeast(resp.Body, buf, 6); close(done) }()
	select {
	case <-done:
		if !strings.HasPrefix(string(buf), "data: ") {
			t.Errorf("first frame not an SSE data line: %q", string(buf))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no initial SSE frame")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestServer`
Expected: FAIL (NewServer/Server undefined).

- [ ] **Step 4: Write the implementation**

`internal/dashboard/server.go`:
```go
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed web/index.html web/app.css web/app.js
var webFS embed.FS

// Server wires the HTTP handlers over a Poller (snapshot source) and a Hub (live
// stream). All endpoints are read-only.
type Server struct {
	poller *Poller
	hub    *Hub
	static http.Handler
}

// NewServer builds the HTTP server.
func NewServer(p *Poller, h *Hub) *Server {
	sub, _ := fs.Sub(webFS, "web")
	return &Server{poller: p, hub: h, static: http.FileServer(http.FS(sub))}
}

// Handler returns the configured mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/static/", s.handleStatic)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/job/", s.handleJob)
	mux.HandleFunc("/events", s.handleEvents)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "index missing", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	http.StripPrefix("/static/", s.static).ServeHTTP(w, r)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.poller.LatestJSON())
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/job/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	recs, err := s.poller.readRegistry(s.poller.registryPath)
	if err != nil {
		http.Error(w, "registry unavailable", 503)
		return
	}
	for i := range recs {
		if recs[i].JobID == id {
			d, ok := BuildDetail(recs[i], s.poller.now())
			if !ok {
				break
			}
			writeJSON(w, d)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.hub.Subscribe()
	defer cancel()

	// Send the current snapshot immediately so a fresh client paints at once.
	writeSSE(w, s.poller.LatestJSON())
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-ch:
			if !open {
				return
			}
			writeSSE(w, msg)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, data []byte) {
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b, err := jsonMarshal(v)
	if err != nil {
		http.Error(w, "encode error", 500)
		return
	}
	_, _ = w.Write(b)
}
```
Add a tiny helper at the bottom of `state.go` (keeps `encoding/json` usage centralized):
```go
// jsonMarshal is a thin wrapper so server.go need not import encoding/json
// directly for one call.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -race ./internal/dashboard/ -run TestServer`
Expected: PASS.

- [ ] **Step 6: Run the whole package**

Run: `go test -race ./internal/dashboard/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/server.go internal/dashboard/server_test.go internal/dashboard/web/
git commit -m "feat(dashboard): http server + SSE + go:embed (placeholder UI)"
```

---

## Task 9: Tailscale IP detection + wait-for-tailnet

**Files:**
- Create: `internal/dashboard/tailscale.go`
- Create: `internal/dashboard/tailscale_test.go`

- [ ] **Step 1: Write the failing test**

`internal/dashboard/tailscale_test.go`:
```go
package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseTailscaleIP_Valid(t *testing.T) {
	ip, err := parseTailscaleIP([]byte("100.101.102.103\nfd7a::1\n"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ip != "100.101.102.103" {
		t.Errorf("ip=%q", ip)
	}
}

func TestParseTailscaleIP_RejectsNonCGNAT(t *testing.T) {
	if _, err := parseTailscaleIP([]byte("192.168.1.5\n")); err == nil {
		t.Errorf("LAN IP must be rejected")
	}
	if _, err := parseTailscaleIP([]byte("\n")); err == nil {
		t.Errorf("empty must be rejected")
	}
}

func TestWaitForTailscaleIP_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	detect := func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("not up")
		}
		return "100.64.0.9", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ip, err := waitForTailscaleIP(ctx, detect, time.Millisecond)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ip != "100.64.0.9" || calls != 3 {
		t.Errorf("ip=%q calls=%d", ip, calls)
	}
}

func TestWaitForTailscaleIP_ContextCancel(t *testing.T) {
	detect := func() (string, error) { return "", errors.New("never") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForTailscaleIP(ctx, detect, time.Millisecond); err == nil {
		t.Errorf("canceled ctx should error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run Tailscale`
Expected: FAIL (functions undefined).

- [ ] **Step 3: Write the implementation**

`internal/dashboard/tailscale.go`:
```go
package dashboard

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"time"
)

// cgnat is the Tailscale CGNAT range (100.64.0.0/10) all tailnet IPv4 addresses
// fall in. Validating against it guarantees we never bind a LAN/public address.
var cgnat = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// tailscaleCandidates are the absolute fallbacks tried when "tailscale" is not on
// PATH (systemd user units have a minimal PATH).
var tailscaleCandidates = []string{"tailscale", "/usr/bin/tailscale", "/usr/local/bin/tailscale", "/usr/sbin/tailscale"}

// DetectTailscaleIP runs `tailscale ip -4` (trying PATH then common absolute
// locations) and returns the validated CGNAT IPv4.
func DetectTailscaleIP() (string, error) {
	var lastErr error
	for _, bin := range tailscaleCandidates {
		out, err := exec.Command(bin, "ip", "-4").Output()
		if err != nil {
			lastErr = err
			continue
		}
		return parseTailscaleIP(out)
	}
	if lastErr == nil {
		lastErr = errors.New("tailscale CLI not found")
	}
	return "", lastErr
}

// parseTailscaleIP extracts the first CGNAT IPv4 from `tailscale ip -4` output.
func parseTailscaleIP(out []byte) (string, error) {
	for _, line := range strings.Split(string(out), "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil {
			continue
		}
		if cgnat.Contains(ip) {
			return s, nil
		}
		return "", errors.New("tailscale ip not in CGNAT range: " + s)
	}
	return "", errors.New("no tailscale IPv4 found")
}

// waitForTailscaleIP polls detect until it returns an IP, ctx is canceled, or
// (never) — it loops on backoff. It never falls back to a non-tailnet address.
func waitForTailscaleIP(ctx context.Context, detect func() (string, error), backoff time.Duration) (string, error) {
	for {
		if ip, err := detect(); err == nil {
			return ip, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// WaitForTailscaleIP is the production wait loop (2s backoff, logs via the
// caller). It blocks until a tailnet IP appears or ctx is canceled.
func WaitForTailscaleIP(ctx context.Context) (string, error) {
	return waitForTailscaleIP(ctx, DetectTailscaleIP, 2*time.Second)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run Tailscale`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/tailscale.go internal/dashboard/tailscale_test.go
git commit -m "feat(dashboard): tailscale IP detection + wait-for-tailnet"
```

---

## Task 10: Entrypoint `cmd/pi-dashboard`

**Files:**
- Create: `cmd/pi-dashboard/main.go`
- Create: `cmd/pi-dashboard/main_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/pi-dashboard/main_test.go`:
```go
package main

import "testing"

func TestResolveAddr_Explicit(t *testing.T) {
	got, err := resolveAddr("1.2.3.4:9999", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "1.2.3.4:9999" {
		t.Errorf("addr=%q want 1.2.3.4:9999", got)
	}
}

func TestResolveAddr_DetectsTailscale(t *testing.T) {
	detect := func() (string, error) { return "100.64.0.5", nil }
	got, err := resolveAddr("", detect)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "100.64.0.5:7777" {
		t.Errorf("addr=%q want 100.64.0.5:7777", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/pi-dashboard/`
Expected: FAIL (resolveAddr undefined).

- [ ] **Step 3: Write the implementation**

`cmd/pi-dashboard/main.go`:
```go
// Command pi-dashboard is a standalone, read-only realtime viewer of pi-mcp
// workflows. It reads the registry + run files the pi-mcp server persists and
// serves a web UI over HTTP + SSE, bound to the host's Tailscale IPv4.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/dashboard"
)

const defaultPort = "7777"

func main() {
	addrFlag := flag.String("addr", "", "explicit bind address host:port (default: detected Tailscale IP + :7777)")
	stateDir := flag.String("state-dir", config.StateDir(), "pi-mcp state dir (holds pi-mcp/registry.json)")
	flag.Parse()

	logger := log.New(os.Stderr, "pi-dashboard ", log.LstdFlags)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr, err := resolveAddrWait(ctx, *addrFlag, logger)
	if err != nil {
		logger.Fatalf("resolve bind address: %v", err)
	}

	registryPath := config.RegistryPath()
	// honor an overridden state dir for the registry path too
	if *stateDir != config.StateDir() {
		registryPath = filepathJoin(*stateDir, "pi-mcp", "registry.json")
	}

	hub := dashboard.NewHub()
	poller := dashboard.NewPoller(registryPath, *stateDir, hub)
	go poller.Run(ctx)

	srv := dashboard.NewServer(poller, hub)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	logger.Printf("serving on http://%s (state-dir=%s)", addr, *stateDir)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("serve: %v", err)
	}
}

// resolveAddr returns the explicit addr when given, else detects the Tailscale
// IP (one shot) and appends :7777. detect is injectable for tests; nil uses the
// production detector.
func resolveAddr(explicit string, detect func() (string, error)) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if detect == nil {
		detect = dashboard.DetectTailscaleIP
	}
	ip, err := detect()
	if err != nil {
		return "", err
	}
	return ip + ":" + defaultPort, nil
}

// resolveAddrWait is the production path: explicit addr binds immediately; an
// empty addr WAITS for the tailnet IP (never falls back to LAN).
func resolveAddrWait(ctx context.Context, explicit string, logger *log.Logger) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	logger.Printf("waiting for tailnet IP…")
	ip, err := dashboard.WaitForTailscaleIP(ctx)
	if err != nil {
		return "", err
	}
	logger.Printf("bound tailnet IP %s", ip)
	return ip + ":" + defaultPort, nil
}

// filepathJoin avoids importing path/filepath at the top for one call site.
func filepathJoin(parts ...string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/pi-dashboard/`
Expected: PASS.

- [ ] **Step 5: Build the binary**

Run: `go build -o /tmp/pi-dashboard ./cmd/pi-dashboard && echo OK`
Expected: prints `OK` (compiles + embeds placeholder UI).

- [ ] **Step 6: Commit**

```bash
git add cmd/pi-dashboard/
git commit -m "feat(dashboard): pi-dashboard entrypoint (flags, wait-for-tailnet, serve)"
```

---

## Task 11: The real frontend (SPA)

**Files:**
- Modify: `internal/dashboard/web/index.html`
- Modify: `internal/dashboard/web/app.css`
- Modify: `internal/dashboard/web/app.js`

This task replaces the placeholders with the real light/airy UI. There is no Go test for rendering; correctness is verified by the existing `server_test.go` (assets still embed + serve) plus manual verification in Step 4.

- [ ] **Step 1: Write `index.html`**

`internal/dashboard/web/index.html`:
```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>pi-mcp · control plane</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <header class="topbar">
    <span class="brand">pi-mcp <span class="muted">· control plane</span></span>
    <span class="counts" id="counts"></span>
    <span class="conn" id="conn" title="connection">●</span>
  </header>
  <main class="layout">
    <aside class="rail">
      <section>
        <h2>Live</h2>
        <div id="live" class="list"></div>
      </section>
      <section>
        <h2>History
          <span class="filter" id="filter">
            <button data-w="24h" class="on">24h</button><button data-w="7d">7d</button><button data-w="all">all</button>
          </span>
        </h2>
        <div id="history" class="list"></div>
      </section>
    </aside>
    <section class="panel" id="panel"></section>
  </main>
  <script src="/static/app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Write `app.css`** (light/white, airy cards, color only on status)

`internal/dashboard/web/app.css`:
```css
:root{
  --bg:#ffffff; --fg:#1a1a1a; --muted:#8a8a8a; --line:#ececec; --card:#ffffff;
  --shadow:0 1px 3px rgba(0,0,0,.06),0 1px 2px rgba(0,0,0,.04);
  --run:#2563eb; --done:#16a34a; --queue:#d97706; --fail:#dc2626; --abort:#6b7280;
  --mono:ui-monospace,SFMono-Regular,Menlo,monospace;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;font-size:14px}
.muted{color:var(--muted)}
.topbar{display:flex;align-items:center;gap:16px;padding:10px 18px;border-bottom:1px solid var(--line)}
.brand{font-weight:600}
.counts{display:flex;gap:14px;font-family:var(--mono);font-size:12px}
.counts b{font-weight:600}
.conn{margin-left:auto;color:var(--done)}
.conn.down{color:var(--fail)}
.layout{display:grid;grid-template-columns:340px 1fr;height:calc(100vh - 49px)}
.rail{border-right:1px solid var(--line);overflow:auto;padding:12px}
.rail h2{font-size:12px;text-transform:uppercase;letter-spacing:.05em;color:var(--muted);margin:14px 6px 8px;display:flex;align-items:center;gap:8px}
.list{display:flex;flex-direction:column;gap:8px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;box-shadow:var(--shadow);padding:10px 12px;cursor:pointer}
.card:hover{border-color:#dcdcdc}
.card.sel{outline:2px solid var(--run)}
.card .row{display:flex;align-items:center;gap:8px;justify-content:space-between}
.jid{font-family:var(--mono);font-size:12px}
.pill{font-size:11px;padding:1px 8px;border-radius:999px;color:#fff;font-family:var(--mono)}
.pill.running{background:var(--run)} .pill.completed{background:var(--done)}
.pill.queued{background:var(--queue)} .pill.failed{background:var(--fail)} .pill.aborted{background:var(--abort)}
.bar{height:6px;border-radius:3px;background:#eee;overflow:hidden;margin-top:8px}
.bar > i{display:block;height:100%;background:var(--run)}
.sub{font-family:var(--mono);font-size:11px;color:var(--muted);margin-top:6px}
.chip{font-family:var(--mono);font-size:10px;background:#f4f4f5;border-radius:6px;padding:1px 6px;margin-right:4px}
.filter button{font:inherit;font-size:11px;border:1px solid var(--line);background:#fff;border-radius:6px;padding:1px 6px;cursor:pointer;color:var(--muted)}
.filter button.on{color:var(--fg);border-color:#cfcfcf}
.panel{overflow:auto;padding:22px}
.panel h1{font-size:18px;margin:0 0 4px}
.fleet{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:12px;margin:16px 0}
.agent{background:var(--card);border:1px solid var(--line);border-radius:12px;box-shadow:var(--shadow);padding:10px 12px}
.agent .m{font-family:var(--mono);font-size:11px}
.agent .st{font-size:11px}
.kv{font-family:var(--mono);font-size:12px;color:var(--muted)}
pre{background:#fafafa;border:1px solid var(--line);border-radius:10px;padding:12px;overflow:auto;font-family:var(--mono);font-size:12px}
.overview{display:flex;gap:28px;flex-wrap:wrap;margin-top:8px}
.ov{background:var(--card);border:1px solid var(--line);border-radius:12px;box-shadow:var(--shadow);padding:14px 18px;min-width:96px}
.ov b{display:block;font-size:26px;font-family:var(--mono)}
details summary{cursor:pointer;color:var(--muted);font-size:12px}
.finding{border-left:3px solid var(--line);padding:4px 10px;margin:6px 0}
.finding.high{border-color:var(--fail)} .finding.med{border-color:var(--queue)} .finding.low{border-color:var(--done)}
```

- [ ] **Step 3: Write `app.js`** (EventSource, render, drill-down, history filter, overview, local elapsed clock)

`internal/dashboard/web/app.js`:
```js
"use strict";
let STATE = { jobs: [], counts: {}, stateDir: "" };
let SELECTED = null;       // jobId
let HIST_WINDOW = "24h";

const $ = (s) => document.querySelector(s);
const TERMINAL = new Set(["completed", "failed", "aborted"]);
const shortId = (id) => (id || "").replace(/^job-/, "").slice(0, 8);

function fmtTokens(n) { return n >= 1000 ? (n / 1000).toFixed(0) + "k" : String(n); }
function fmtDur(ms) {
  if (ms == null) return "";
  const s = Math.floor(ms / 1000);
  return (s >= 60 ? Math.floor(s / 60) + "m" + (s % 60) + "s" : s + "s");
}
function elapsed(startedAt) {
  const s = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000));
  const m = Math.floor(s / 60), sec = s % 60;
  return (m > 0 ? m + ":" + String(sec).padStart(2, "0") : "0:" + String(sec).padStart(2, "0"));
}
function within(startedAt, win) {
  if (win === "all") return true;
  const ageMs = Date.now() - new Date(startedAt).getTime();
  return ageMs <= (win === "7d" ? 7 * 864e5 : 864e5);
}

function jobCard(j, live) {
  const sel = j.jobId === SELECTED ? " sel" : "";
  const title = j.workflowName || shortId(j.jobId);
  let body = "";
  if (live && j.status === "running") {
    const frac = j.agentsTotal ? Math.round(100 * j.agentsDone / j.agentsTotal) : 0;
    const meta = j.blindWindow ? "authoring…" :
      `${j.phase || ""} · Σ ${fmtTokens(j.liveTokens)} tok · ${j.agentsDone}/${j.agentsTotal}`;
    body = `<div class="bar"><i style="width:${frac}%"></i></div>
            <div class="sub" data-elapsed="${j.startedAt}">${meta} · ${elapsed(j.startedAt)}</div>`;
  } else if (j.status === "queued") {
    body = `<div class="sub">queued</div>`;
  } else {
    const chips = Object.entries(j.fleetByModel || {}).map(([m, c]) =>
      `<span class="chip">${m.split("/").pop()}×${c}</span>`).join("");
    const cost = j.cost != null ? "$" + j.cost.toFixed(2) : "";
    body = `<div class="sub">${chips} ${fmtTokens(j.liveTokens)} tok ${cost} ${fmtDur(j.durationMs)}</div>`;
  }
  return `<div class="card${sel}" data-id="${j.jobId}">
    <div class="row"><span class="jid">${title}</span>
      <span class="pill ${j.status}">${j.status}</span></div>${body}</div>`;
}

function render() {
  const c = STATE.counts || {};
  $("#counts").innerHTML =
    `<span>LIVE <b>${c.running || 0}</b></span><span>QUEUED <b>${c.queued || 0}</b></span>` +
    `<span>DONE <b>${c.completed || 0}</b></span><span>FAIL <b>${c.failed || 0}</b></span>`;

  const live = STATE.jobs.filter((j) => j.status === "running" || j.status === "queued");
  const hist = STATE.jobs.filter((j) => TERMINAL.has(j.status) && within(j.startedAt, HIST_WINDOW));
  $("#live").innerHTML = live.length ? live.map((j) => jobCard(j, true)).join("") : `<div class="muted">no active jobs</div>`;
  $("#history").innerHTML = hist.length ? hist.map((j) => jobCard(j, false)).join("") : `<div class="muted">none in ${HIST_WINDOW}</div>`;

  for (const el of document.querySelectorAll(".card")) {
    el.onclick = () => { SELECTED = el.dataset.id; loadDetail(SELECTED); render(); };
  }
  if (!SELECTED) renderOverview();
}

function renderOverview() {
  const c = STATE.counts || {};
  $("#panel").innerHTML = `<h1>Overview</h1>
    <div class="muted">${STATE.stateDir || ""}</div>
    <div class="overview">
      <div class="ov"><span class="muted">live</span><b>${c.running || 0}</b></div>
      <div class="ov"><span class="muted">queued</span><b>${c.queued || 0}</b></div>
      <div class="ov"><span class="muted">done</span><b>${c.completed || 0}</b></div>
      <div class="ov"><span class="muted">failed</span><b>${c.failed || 0}</b></div>
      <div class="ov"><span class="muted">aborted</span><b>${c.aborted || 0}</b></div>
    </div>
    <p class="muted">Select a job to drill into its fleet.</p>`;
}

async function loadDetail(id) {
  try {
    const r = await fetch("/api/job/" + encodeURIComponent(id));
    if (!r.ok) { $("#panel").innerHTML = `<div class="muted">job not found</div>`; return; }
    renderDetail(await r.json());
  } catch (e) { /* keep last panel */ }
}

function agentCard(a) {
  const icon = a.status === "done" ? "✓" : a.status === "error" ? "✗" : "⏳";
  const prompt = (a.prompt || "").slice(0, 160);
  return `<div class="agent">
    <div class="row"><span class="m">${a.model || "?"}</span><span class="st">${icon} ${a.status}</span></div>
    <div class="kv">${a.label || ""} · ${a.phase || ""} · ${fmtTokens(a.tokens || 0)} tok</div>
    <details><summary>prompt / result</summary>
      <pre>${escapeHTML(prompt)}</pre>
      <pre>${escapeHTML(a.resultPreview || "")}</pre></details>
  </div>`;
}

function renderResult(res) {
  if (res == null) return "";
  if (typeof res !== "object" || Array.isArray(res)) {
    return `<h3>Result (fora do contrato)</h3><pre>${escapeHTML(JSON.stringify(res, null, 2))}</pre>`;
  }
  let h = `<h3>Result</h3>`;
  if (res.summary) h += `<p>${escapeHTML(res.summary)}</p>`;
  if (Array.isArray(res.findings)) {
    h += res.findings.map((f) =>
      `<div class="finding ${f.severity || ""}"><b>${escapeHTML(f.title || "")}</b><br>${escapeHTML(f.detail || "")}</div>`).join("");
  }
  if (Array.isArray(res.files_changed) && res.files_changed.length) {
    h += `<div class="kv">files: ${res.files_changed.map(escapeHTML).join(", ")}</div>`;
  }
  if (res.diff_summary) h += `<p class="kv">${escapeHTML(res.diff_summary)}</p>`;
  if (Array.isArray(res.open_questions) && res.open_questions.length) {
    h += `<h4>Open questions</h4><ul>` + res.open_questions.map((q) => `<li>${escapeHTML(q)}</li>`).join("") + `</ul>`;
  }
  if (res.confidence) h += `<div class="kv">confidence: ${escapeHTML(res.confidence)}</div>`;
  // Nothing recognized -> raw fallback (never a blank panel).
  if (h === `<h3>Result</h3>`) h += `<pre>${escapeHTML(JSON.stringify(res, null, 2))}</pre>`;
  return h;
}

function renderDetail(d) {
  const where = d.mode === "write" ? `${d.branch || ""} · ${d.worktreePath || ""}` : d.cwd;
  let h = `<h1>${escapeHTML(d.workflowName || shortId(d.jobId))} <span class="pill ${d.status}">${d.status}</span></h1>
    <div class="muted">${d.mode} · ${escapeHTML(where || "")} · run ${d.runId || "—"}</div>`;
  if (d.blindWindow) h += `<p>✍ orchestrator authoring workflow… (no run file yet)</p>`;
  if (d.phases && d.phases.length) {
    h += `<div class="sub">phases: ${d.phases.map((p) => p === d.phase ? `<b>${escapeHTML(p)}</b>` : escapeHTML(p)).join(" → ")}</div>`;
  }
  if (d.agents && d.agents.length) h += `<div class="fleet">${d.agents.map(agentCard).join("")}</div>`;
  if (d.tokenUsage) {
    const t = d.tokenUsage;
    h += `<div class="kv">tokens in ${t.input} / out ${t.output} / cache ${t.cacheRead} · total ${t.total}${d.cost != null ? " · $" + d.cost.toFixed(4) : ""}${d.durationMs ? " · " + fmtDur(d.durationMs) : ""}</div>`;
  }
  if (d.error) h += `<p class="pill failed">${escapeHTML(d.errorCode || "error")}</p><pre>${escapeHTML(d.error)}</pre>`;
  h += renderResult(d.result);
  $("#panel").innerHTML = h;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function applyState(st) {
  STATE = st;
  render();
  // If a live job is selected, refresh its detail each push; terminal stays frozen.
  if (SELECTED) {
    const j = STATE.jobs.find((x) => x.jobId === SELECTED);
    if (j && !TERMINAL.has(j.status)) loadDetail(SELECTED);
  }
}

function connect() {
  const es = new EventSource("/events");
  es.onmessage = (e) => { try { applyState(JSON.parse(e.data)); } catch (_) {} $("#conn").classList.remove("down"); };
  es.onerror = () => { $("#conn").classList.add("down"); };
}

// History window buttons.
document.addEventListener("click", (e) => {
  const b = e.target.closest("#filter button");
  if (!b) return;
  HIST_WINDOW = b.dataset.w;
  for (const x of document.querySelectorAll("#filter button")) x.classList.toggle("on", x === b);
  render();
});

// Tick the live elapsed labels locally (no server push needed).
setInterval(() => {
  for (const el of document.querySelectorAll("[data-elapsed]")) {
    const base = el.textContent.replace(/ · \d+:\d\d$/, "");
    el.textContent = base + " · " + elapsed(el.dataset.elapsed);
  }
}, 1000);

connect();
```

- [ ] **Step 4: Manual verification**

```bash
go build -o /tmp/pi-dashboard ./cmd/pi-dashboard
# Point at the repo's local runs dir as a quick smoke (no tailnet needed):
XDG_STATE_HOME=/tmp/empty-state /tmp/pi-dashboard --addr 127.0.0.1:7777 --state-dir "$(pwd)" &
sleep 1
curl -s http://127.0.0.1:7777/api/state | head -c 400; echo
curl -s http://127.0.0.1:7777/ | grep -o "control plane"
kill %1
```
Expected: `/api/state` returns JSON with a `jobs` array; `/` contains "control plane". Open `http://127.0.0.1:7777/` in a browser to eyeball the light UI. (Note: `--addr 127.0.0.1` is for local smoke only; production omits `--addr` to bind the tailnet IP.)

- [ ] **Step 5: Confirm embed tests still pass**

Run: `go test -race ./internal/dashboard/`
Expected: PASS (the real assets still embed + serve; `TestServer_Index` still finds "control plane").

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/web/
git commit -m "feat(dashboard): light/airy SPA — live, history, overview, drill-down"
```

---

## Task 12: systemd user unit + install docs

**Files:**
- Create: `deploy/pi-dashboard.service`
- Create: `deploy/README.md`

- [ ] **Step 1: Write the unit**

`deploy/pi-dashboard.service`:
```ini
[Unit]
Description=pi-mcp control-plane dashboard
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/pi-dashboard
# user units have a minimal PATH; ensure the tailscale CLI is findable.
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
```

- [ ] **Step 2: Write install docs**

`deploy/README.md`:
````markdown
# pi-dashboard deploy (systemd user unit)

The dashboard is read-only and binds the host's **Tailscale IP** (`100.x.y.z:7777`).
It must run as the **same user** as the pi-mcp server so `~/.local/state/pi-mcp/registry.json`
resolves identically.

## Install

```bash
# 1. Build + install the binary
go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard   # may need sudo for /usr/local/bin

# 2. Install the user unit
mkdir -p ~/.config/systemd/user
cp deploy/pi-dashboard.service ~/.config/systemd/user/

# 3. Survive logout / start at boot (no root login needed)
loginctl enable-linger "$USER"

# 4. Enable + start
systemctl --user daemon-reload
systemctl --user enable --now pi-dashboard
```

## Verify / operate

```bash
systemctl --user status pi-dashboard
journalctl --user -u pi-dashboard -f      # logs (waits for tailnet, bind, etc.)
```

Open `http://<tailscale-ip>:7777/` from any device on your tailnet
(`tailscale ip -4` shows the IP). HTTP is plain on the tailnet — tailnet
membership is the access boundary (no app-level auth).

## Update

```bash
go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard
systemctl --user restart pi-dashboard
```

## Notes

- On boot the service logs `waiting for tailnet IP…` until tailscaled is up, then
  binds; it never falls back to a LAN/public interface.
- Override the bind with `ExecStart=/usr/local/bin/pi-dashboard --addr 100.x.y.z:7777`
  if auto-detection ever fails.
````

- [ ] **Step 3: Verify the unit file is well-formed**

Run: `systemd-analyze verify deploy/pi-dashboard.service || true`
Expected: no fatal errors (a warning about the absolute ExecStart path existing only on the target host is fine).

- [ ] **Step 4: Commit**

```bash
git add deploy/
git commit -m "feat(dashboard): systemd user unit + install docs"
```

---

## Final verification

- [ ] **Run the entire suite with the race detector**

Run: `go test -race ./...`
Expected: PASS across all packages (existing pi-mcp packages + the new `livestatus` and `dashboard` packages).

- [ ] **Build both binaries**

Run: `go build -o /tmp/pi-mcp ./cmd/pi-mcp && go build -o /tmp/pi-dashboard ./cmd/pi-dashboard && echo OK`
Expected: `OK`.

- [ ] **Vet**

Run: `go vet ./...`
Expected: no findings.

---

## Self-Review (completed by plan author)

**Spec coverage:**
- Standalone read-only Go service → Tasks 3–11 + cmd (Task 10). ✔
- Registry index + scattered run-file overlay → Task 3 (registry) + Task 5 (`readRun` builds `<RunsDir>/<RunID>.json`). ✔
- Poll-1s, terminal cache, push-on-change, client-side elapsed → Task 6 (poller) + Task 11 (local elapsed tick). ✔
- Light SSE list + on-demand `/api/job/{id}`, live-selected re-fetch → Task 8 (server) + Task 11 (`applyState` refetch). ✔
- Live `Σ agents[].tokens`; cost only at end → Task 5 (`LiveTokens`, `Cost` from `TokenUsage`). ✔
- Time-window history (24h/7d/all) → Task 11 (`within`, filter buttons). ✔
- Structured §5.4 result + degenerate fallback → Task 11 (`renderResult`). ✔
- Liveness matches `pi_status` (shared `livestatus`, pid-unknown cross-process, worktree-activity) → Task 1 + Task 4 + Task 5. ✔
- State-path shared with app → Task 2. ✔
- Light/airy theme, overview landing, write==read → Task 11. ✔
- Tailscale-IP bind, wait-for-tailnet, `tailscale ip -4` + PATH hardening → Task 9 + Task 10. ✔
- systemd user unit + linger → Task 12. ✔
- Error handling (missing registry → empty; corrupt → keep last good; blind window; off-contract; SSE cleanup) → Tasks 3, 5, 6, 7, 11. ✔
- Tests over existing fixtures → Task 5 golden tests. ✔

**Placeholder scan:** No "TBD"/"handle edge cases"/"similar to". The web placeholders in Task 8 are explicitly replaced in Task 11 (intentional, to keep `go:embed` compiling under TDD). ✔

**Type consistency:** `JobSummary`/`JobDetail`/`DashboardState`/`Counts`/`AgentView` defined once in Task 5; `Sink`/`Poller` in Task 6; `Hub` in Task 7; `Server` in Task 8; `livestatus.{MapDisk,IsTerminal,Derive}` in Task 1; `config.{StateDir,RegistryPath}` in Task 2; `resolveAddr` in Task 10. The JSON field names match between `state.go` (Go tags) and `app.js` (accessors: `status`, `jobId`, `liveTokens`, `fleetByModel`, `agentsDone/Total`, `blindWindow`, `tokenUsage`, `cost`, `durationMs`, `workflowName`, `worktreePath`, `branch`, `phases`, `phase`, `result`, `intermediate`→ unused by UI but present). ✔
```
