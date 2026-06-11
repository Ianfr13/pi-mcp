# Event-driven updates + delta-based pi_status

**Date:** 2026-06-11
**Status:** approved (design), amended after adversarial review, pending implementation

## Implementation sequencing (added after adversarial review)

Two independently testable and deployable phases:

1. **Phase 1 — delta protocol.** The context win for Claude; no new
   dependency, no watcher. Ships alone.
2. **Phase 2 — fsnotify events.** Server-side latency/CPU optimization
   layered under the (already shipped) delta protocol.

## Goal

Two coupled improvements:

1. **Server-side:** replace the three independent polling layers (250ms
   `pi_status` long-poll, 500ms `correlate` readdir, 1s dashboard poller)
   with fsnotify events. Idle CPU goes to ~zero; update latency drops from
   seconds to <100ms.
2. **Client-side (the feature):** make `pi_status` context-frugal for the
   calling Claude. Today every call returns the FULL `intermediate[]` list
   again (handler_status.go: `buildStatus` → `runstore.Intermediates`),
   re-injecting results the caller already saw. Replace with a
   cursor-based delta in **minimal mode**: one line per newly-finished
   agent, full results only at terminal time.

User requirement (verbatim intent): Claude must receive what is necessary
when the job advances — failure, stall, progress — without verbose dumps
filling its context.

## Non-goals (recorded for later rounds)

- Mutex timeout guards / registry.go refactor (545 lines)
- `pi_list` search/pagination
- `pi_followup` (iterate on an existing run)
- Token/cost aggregation beyond what JobSummary already shows

## Architecture

### New package: `internal/watch`

Thin wrapper over `github.com/fsnotify/fsnotify` (new dependency; mature,
no transitive deps).

- `Watcher.Subscribe(dir) (<-chan struct{}, cancel func(), error)` —
  notification channel for any create/write/rename in `dir`, debounced
  ~50ms (coalesce write bursts; pi rewrites the run file frequently).
- **Events are hints, never correctness (invariant, added after
  adversarial review).** Every consumer re-reads authoritative state on
  wake AND keeps its fallback ticker; a dropped/missed fsnotify event
  costs at most one fallback interval of latency, never a wrong answer.
  Idle CPU is "~zero between fallback ticks", not literally zero.
- **Late-born runsDir (added after adversarial review):** the runsDir
  often does not exist at subscribe time (pi creates it after launch).
  Subscribe on the nearest existing ancestor and re-attempt the precise
  subscription on each fallback tick until the dir exists. Test case:
  subscribe → mkdir later → file create still wakes.
- Subscribe error (FS without inotify) → caller keeps its fallback
  ticker; the system never breaks, only degrades to slow polling.
- Channel semantics mirror Hub: buffered(1), non-blocking send (a pending
  notification is enough; consumers re-read state on wake anyway).

### Consumer 1: `pi_status --wait` (internal/mcpserver/handler_status.go)

`waitForChange` today: 250ms ticker, each tick re-reads + re-parses the
full run JSON. New shape:

- Subscribe to `tgt.runsDir` on entry; wake on event → `store.Load` →
  existing `wakeChanged` predicate (unchanged).
- Fallback ticker: 2s (was 250ms). `WaitCap` (60s) unchanged.
- Blind window covered for free: run-file creation and authoring-file
  writes land in the same runsDir and fire the watcher.
- **WaitCap raised to 5min (decided in review)**, from 60s. With
  event-driven wakes a quiet 20-minute run drops from ~20 "no change"
  round-trips to ~4. **Amended:** the cap becomes configurable via env
  (`PI_MCP_WAIT_CAP`, default 5min) so it can be lowered without a
  rebuild, and before shipping we VERIFY the actual Claude Code MCP
  tool-call timeout on this box (raise `MCP_TOOL_TIMEOUT` if needed) —
  a client that kills the call at 60s would strand every wait.
- **Transient parse-error grace (added after adversarial review):** a
  watcher can wake mid-write; the mcpserver `Load` path has no `.bak`
  fallback (the dashboard's does). During a NON-terminal job, a run-file
  decode error is treated as "no change yet" (retry on next wake/tick)
  for a short grace period (~2s) instead of immediately surfacing
  `failed`/persistence error; a persistent decode error still fails.
- Stall wake: today a stalled run never wakes the long-poll (no file
  change). Add a deadline check inside the wait loop: if the loaded run's
  `liveStatus` becomes `stalled` (run file idle + worktree idle past
  StaleThreshold), return — the caller gets a one-line stalled status
  instead of silence until WaitCap.
- **Early inactivity warning (decided in review):** when a non-terminal
  run shows no activity (run-file mtime + worktree) for ~5min, the wait
  wakes ONCE per threshold crossing and the response carries the existing
  `progress.lastActivitySeconds` ("no activity for Xmin"). The status
  string stays `running`; only the 30min StaleThreshold flips it to
  `stalled`. No global threshold change (a legitimate 20min agent must
  not read as stalled).
  **Amended — deterministic state model:** the warning is keyed by the
  activity epoch: `warned[jobID] = lastActivityTimestamp` at warn time.
  It re-arms only when observed activity advances past that timestamp
  (any newer run-file/worktree mtime). Server restart loses the map →
  at most one repeated warning. Per-job (not per-wait), so concurrent
  waits in one session do not double-warn.

### Consumer 2: `correlate` (internal/jobs/registry.go)

Replace the 500ms `correlatePollInterval` readdir loop with a runsDir
watch; resolve RunID the instant the run file appears. Fallback ticker 2s.
The injectable seam stays (tests currently override the interval; they
will inject a fake watcher instead or keep using the fallback ticker).

### Consumer 3: dashboard poller (internal/dashboard/poller.go)

- Watch the registry.db directory + the runsDirs of all non-terminal
  jobs (dynamic set, refreshed on each rebuild). **Amended:** the
  "`-wal` is touched on every flush" observation is a wake HINT only —
  any event in the DB directory triggers a rebuild, and the 5s fallback
  reconciles regardless; correctness never depends on which file SQLite
  happens to touch.
- Event → debounce → `Tick()`. Fallback ticker 5s (was 1s).
- **Run-file read cache** in the `readRun` path keyed by (path, mtime,
  size — ext4 has ns mtime granularity on this box): active jobs are
  re-parsed only when the key changed. **Amended:** "terminal" caching
  keys off the REGISTRY terminal status (authoritative, per existing
  status-precedence rule), and the cache entry is dropped if the file's
  key changes anyway (covers `.bak` repair / rename-replacement). Today
  `BuildState` re-reads + re-parses EVERY job's run file every second,
  including terminal ones that can never change.
- Existing hash-gate on broadcast stays (idle fleet pushes nothing).
- Front-end unchanged: app.js already refetches the open job detail
  (throttled) on each SSE state event, so intermediates render
  progressively once ticks become event-driven.

## pi_status delta protocol (minimal mode)

### Delivery tracking (server-side, decided in review)

The SERVER tracks, in memory per jobId, the last journal index already
delivered to a status caller. Claude passes nothing; every `pi_status`
call automatically returns only what is new since the previous call for
that job. Rationale: models routinely forget optional parameters; a
client-managed cursor would silently degrade to full dumps.

- Tracking is in-memory only. A server restart loses it and the next
  call re-delivers all events once — acceptable.
- Optional input `from_start: true` resets the position (re-read all
  events from 0). No opaque cursor field in the wire protocol.

### Output (model.StatusOutput)

- New field `events []StatusEvent` replacing the always-full
  `intermediate[]` in the default path:

  ```
  StatusEvent { label, model, phase, status ("ok"|"error"), error? }
  ```

  One entry per journal entry not yet delivered — i.e. only agents that
  finished since the last call. No result bodies, no previews (user
  decision: "label + minimal summary").
- **Escape hatch (decided in review):** optional input
  `include_results: true` attaches the full result body to each NEW
  event in that response (existing 16KB `MaxInlineResultBytes`
  truncation applies). Default stays minimal; the cost exists only when
  explicitly requested.
- `intermediate[]` is REMOVED from the response. **Scope (amended):**
  this removal applies ONLY to the MCP `pi_status` output. The shared
  `runstore.Intermediates` helper and the dashboard's
  `JobDetail.Intermediate` are untouched. The only consumer of
  `pi_status` is this user's Claude Code sessions; the output schema and
  its tests (outputschema_test.go) are updated in the same change.
  Full results appear exactly once: `result` at `status: completed`
  (existing behavior). Failure surfaces only the failing agent's error
  line (existing `failureMessage` order preserved).
- **Schema additions (amended — was underspecified):**
  - `agentsDone int` / `agentsTotal int` on StatusOutput (JSON
    `agentsDone`/`agentsTotal`). An errored agent counts as done
    (journal entry exists). Blind window → both 0.
  - `status` vocabulary gains `stalled`. It is NON-terminal: a stalled
    run that resumes goes back to `running`; long-poll waits treat it as
    a wake condition, not an exit-forever. The dashboard already derives
    a stalled display via `livestatus`; the MCP wire schema now matches.
- A wait that expires with no change returns a one-line summary with
  empty `events`.

### Backward compatibility

`pi_status` keeps its name and required inputs. Output schema gains
fields and drops `intermediate[]`; the tool description is updated to
state: "each call returns only events new since your previous call for
this job; pass `from_start: true` to re-read everything, and
`include_results: true` to attach the new events' full results". There
is NO client-managed cursor anywhere in the protocol (fixed: an earlier
draft contradicted this). The MCP output schema test
(outputschema_test.go) is updated accordingly.

Why server-side tracking is the right boundary here (adversarial-review
finding #1, rejected): pi-mcp is a stdio MCP server — every Claude Code
session runs its OWN server process, so in-memory per-jobId tracking is
naturally per-session. Two callers racing for the same delta would
require two concurrent calls inside one session's serial tool loop,
which does not happen. A second session polling the same job has its own
process and its own tracking (first call delivers everything once).

## Error / stall semantics (one-liners, never dumps)

- **Agent failure mid-run:** event row with `status:"error"` + that
  agent's error string. Run keeps going; no other content attached.
- **Job failure:** `status: failed` + `error` via the existing §9
  extraction order. No intermediates attached.
- **Stall:** `status: stalled` + `progress.lastActivitySeconds`; the
  long-poll wakes for it (new deadline check) instead of staying silent.

## Testing

- `internal/watch`: real tempdir tests (create/write/debounce/cancel) +
  subscribe-failure path.
- mcpserver: existing long-poll tests keep working via fallback ticker
  with fake clock; new tests for delta tracking (first call delivers all,
  second call delivers only new, `from_start` resets, server-restart
  re-delivery, `include_results` attachment + truncation), the 5min
  WaitCap, stall wake, and the once-per-crossing early warning.
- jobs: correlate tests switch to fake watcher injection.
- dashboard: cache tests (terminal never re-read; mtime change
  invalidates), event-tick test.
- Full e2e at the end, complete output shown (user preference).

## Deploy

Build + tests → deploy straight to prod (systemd dashboard restart +
installer-pinned MCP runtime), verify live. No staging gate (user
preference).
