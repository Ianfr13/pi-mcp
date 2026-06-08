# pi-mcp · Control Plane Dashboard — Design Spec

**Date:** 2026-06-08
**Status:** Approved (brainstorming + grill-me complete) — pending implementation plan
**Scope:** A standalone, always-online, read-only web dashboard that visualizes pi-mcp
workflows in realtime, from submission through the live agent fan-out to the final
synthesized result.

---

## 1. Goal

Give the user a "mission control" view of everything happening across pi-mcp
workflows — **desde o início até o fim**:

- **Início:** a job is submitted (registry shows `queued`/`running`, the ~20s
  "blind window" before the run file exists).
- **Meio:** the live agent fleet — which models, which phase, per-agent status,
  tokens, intermediate results — as it fans out.
- **Fim:** the synthesized result, final token/cost/duration, or the error.

The dashboard is a **reader** of state pi-mcp and pi already persist to disk. It
never writes to any pi-mcp/pi file and never mutates a running job (read-only).

---

## 2. Key decisions (resolved during brainstorming + grill-me)

| # | Decision | Choice |
|---|----------|--------|
| 1 | Host architecture | Standalone Go binary `pi-dashboard` in this repo |
| 2 | Interactivity | **Read-only** (no cancel/launch from the UI) |
| 3 | Time scope | **Live + history + drill-down** |
| 4 | Realtime trigger | **Poll 1s** coalesced (no `fsnotify` dependency) |
| 5 | Live metric | Live `Σ agents[].tokens`; cost `$` only at end (verbatim) |
| 6 | History retention | **Time-window filter** (default 24h; 24h / 7d / all) |
| 7 | SSE payload | **Light list** over SSE; heavy detail via `GET /api/job/{id}` on demand |
| 8 | Decomposition view | **Fleet only** (no authored-script viewer, no logs panel) |
| 9 | Result rendering | **§5.4 structured contract** view (+ minimal raw fallback for degenerate shapes) |
| 10 | Exposure | Bind **directly to the Tailscale IP** (auto-detect; `--addr` override; fail-fast if no tailnet) |
| 11 | Deploy | **systemd** unit (runs as the user, shares HOME/env with pi-mcp) |

---

## 3. Architecture

```
                         ┌─────────────────────────────────────────┐
   Claude Code sessions  │  pi-mcp server (stdio, per session)      │
   submit pi_workflow ──►│  writes registry.json + spawns pi        │
                         └───────────────┬─────────────────────────┘
                                         │ writes
                 $XDG_STATE_HOME/pi-mcp/registry.json   (job index)
                 <RunsDir>/<RunID>.json  (per-job; scattered)
                                         │ reads (read-only)
                         ┌───────────────▼─────────────────────────┐
   you (via Tailscale) ─►│  pi-dashboard (systemd, always-online)   │
   100.x.y.z:7777        │  poll 1s ► state builder ► SSE hub        │
                         │  GET / · /static · /api/state             │
                         │  GET /events (SSE list) · /api/job/{id}   │
                         └──────────────────────────────────────────┘
```

`pi-dashboard` is a long-lived service, independent of any Claude Code session.
The pi-mcp server only writes to `registry.json` while a Claude Code session is
running it; the dashboard reads that shared file regardless and picks up new jobs
on the next poll tick.

### 3.1 Critical insight: the registry is the index; run files are scattered

There is **no single global runs directory**. Each `model.JobRecord` carries its
own `RunsDir`:

- **read** jobs → `<cwd>/.pi/workflows/runs` (cwd is arbitrary, anywhere on the host)
- **write** jobs → `<worktree>/.pi/workflows/runs` (under `$XDG_STATE_HOME/pi-mcp/worktrees/job-<id>`)

So the dashboard reads `registry.json` to learn *which* jobs exist and *where*
each one's run file lives, then overlays `<RunsDir>/<RunID>.json` for the live
fleet detail. Because the dashboard runs on the **host** (systemd, same user),
these absolute host paths resolve directly — no path remapping.

---

## 4. Data sources

### 4.1 Registry (the job index)

`$XDG_STATE_HOME/pi-mcp/registry.json` → `{ "jobs": [ JobRecord, ... ] }`.

Resolution mirrors `internal/app/app.go`: `$XDG_STATE_HOME/pi-mcp` →
`$HOME/.local/state/pi-mcp` → `$TMPDIR/pi-mcp`. Overridable with `--state-dir`.
**The dashboard must resolve the same path the pi-mcp server used** — guaranteed
when both run as the same user with the same env (the systemd unit ensures this).

`JobRecord` fields consumed: `jobId, runId, sessionId, mode, cwd, worktreePath,
branch, pid, status, startedAt, errorCode, errorMessage`.

Notes from the code:
- The registry **never prunes** terminal jobs → it grows unbounded (motivates the
  time-window filter, decision #6).
- Recovered-after-restart jobs are already terminalized by `Reconcile`: queued →
  `failed`/`ErrServerRestarted`; stale running → `failed`/`ErrServerRestarted`.
- The **task text is never persisted** (may contain secrets) and `args` is null
  headlessly. The dashboard cannot show the original task string. Job intent is
  recoverable from `workflowName` + `agents[].label`/`agents[].prompt` (those
  *are* in the run file).

### 4.2 Run file (the live fleet)

`<RunsDir>/<RunID>.json`, decoded into `model.Run` via the existing
`runstore.ReadRun` (which already falls back to the sibling `.bak` on a corrupt
primary and never reads `.tmp`).

Fields consumed: `workflowName, status, currentPhase, phases, agents[]
(id/callIndex/label/model/phase/status/tokens/startedAt/endedAt/error/resultPreview),
journal[] (index/result), tokenUsage, durationMs, result`.

Joins/derivations reuse existing exported helpers:
- `runstore.ModelHistogram(run)` → `{model: count}` fleet histogram.
- `runstore.Intermediates(run, maxBytes)` → completion-ordered list joining
  `journal[].Index == agents[].CallIndex`, truncating results > 16KB
  (`config.MaxInlineResultBytes`) to a UTF-8-safe preview.

---

## 5. Components (`internal/dashboard`)

### 5.1 `state` — pure view-model builder
Reads the registry + each relevant run file and derives a `DashboardState`. Pure
and golden-testable. Applies the liveness override so the status shown matches
`pi_status` (see §6). Splits into a **light list** (always built) and **heavy
per-job detail** (built on demand for `/api/job/{id}`).

Light `JobSummary` (one per job, sent over SSE):
```
jobId, mode, status(liveness-adjusted), workflowName?, cwd, worktreePath?, branch?,
runId?, startedAt, blindWindow(bool), phase?,
agentsDone, agentsTotal, fleetByModel{model:count},
liveTokens(Σ agents[].tokens),        // shown while running
cost?(verbatim, end only), durationMs?(end), completedAt?,
errorCode?, errorMessage?
```
`startedAt` is the source of truth for elapsed time; the **client** ticks the
elapsed display locally each second (so the wall clock never forces a server push).

Heavy `JobDetail` (one job, built for `/api/job/{id}`):
```
JobSummary fields +
agents[]: { label, model, phase, status, tokens, startedAt?, endedAt?, durationMs?, error?, prompt, resultPreview }
intermediates[]: runstore.Intermediates(run, 16KB)   // full journal results / preview+truncated
tokenUsage?: { input, output, total, cost, cacheRead, cacheWrite }
result?: any                                          // synthesized §5.4 result, end only
```

`DashboardState` (top-level, sent over SSE):
```
generatedAt, stateDir,
counts: { running, queued, completed, failed, aborted, total },
jobs: JobSummary[]   // sorted: active (running/queued) first, then terminal by startedAt desc
```
(The UI labels `running` as **LIVE**. SSE connection health is a client-side
concept — the EventSource `readyState` — not a server field.)

### 5.2 `trigger` — poll loop (seam)
A 1s ticker rebuilds the light state each tick, recomputing **staleness liveness**
(a non-terminal job crossing `StaleThreshold` flips to `failed` — a substantive
change). Defined behind a small interface so tests inject a manual trigger.
**Caching:** terminal jobs' run files never change → cache their derived summary
keyed by `(runId, runFile mtime)`; only re-read the registry + non-terminal jobs'
run files each tick. Elapsed time is not recomputed server-side (client-derived
from `startedAt`).

### 5.3 `hub` — SSE broadcaster
Tracks connected clients; on each rebuild broadcasts the light `DashboardState`
as an SSE `data:` frame. Per-client done-channel cleanup on disconnect. Broadcasts
only when the state hash changed since the last push (the hash excludes the wall
clock, so an idle fleet does not push every second), plus a low-frequency
keepalive comment to hold the connection.

### 5.4 `server` — HTTP + embedded assets (`go:embed`)
- `GET /` → embedded `index.html`
- `GET /static/*` → embedded `app.js`, `app.css`
- `GET /api/state` → current light `DashboardState` (one-shot; initial paint, debug)
- `GET /events` → `text/event-stream`; sends the current state immediately, then
  pushes on every changed rebuild
- `GET /api/job/{id}` → heavy `JobDetail` (built on demand)

Frontend is **vanilla** HTML/CSS/JS embedded via `go:embed` — no build step.

---

## 6. Liveness (status correctness)

The dashboard must show the same status `pi_status` would. It reuses a shared,
pure status-derivation helper (extracted/exported from the existing
`mcpserver.liveStatus` / `jobs.effectiveStatus` logic) so the two readers cannot
drift:

- disk/registry `paused`/`running` → `running` unless stale.
- A non-terminal job whose `updatedAt` is older than `config.StaleThreshold`
  (300s) → `failed`, **unless** a write job's worktree was modified recently
  (worktree-activity overrides run-file staleness).
- **PID is not trusted** cross-process (the dashboard did not launch the jobs and
  PIDs can be reused) → `pidAlive` is treated as "unknown / alive"; liveness
  rests on staleness + worktree-activity + run/registry status.

**Additive refactors to existing code (justified by reuse, prevent drift):**
1. Extract state-path resolution (`registry.json` path + `xdgStateDir`) from
   `internal/app` into a shared exported helper used by both `app` and `dashboard`.
2. Export a pure liveness/status derivation reused by both `mcpserver` and
   `dashboard`.

---

## 7. UI (mission-control, dark, dense)

- **Top bar:** title `pi-mcp · control plane`; counts `LIVE n · QUEUED n · DONE n
  · FAILED n`; SSE connection dot (connected / reconnecting); state-dir path.
- **Left rail:**
  - **LIVE** (running/queued): cards — short jobId / `workflowName`, mode badge,
    status pill, current phase, `agentsDone/agentsTotal` bar, elapsed,
    `Σ <n>k tok` live.
  - **HISTORY** (terminal, time-window filtered 24h/7d/all): rows — status icon,
    agent count, model chips, total tokens, cost `$`, duration, completedAt.
- **Main panel (on select):**
  - Header: jobId, mode, status, cwd (or worktree+branch), runId, elapsed/duration.
  - **Blind window:** "✍ orchestrator authoring workflow… (no run file yet)".
  - **Fleet grid:** one card per agent — model badge, label, phase, status
    (spinner / ✓ / ✗), tokens, timing, `agents[].prompt` + result preview
    (expand → full journal result via the on-demand detail).
  - **Phase timeline:** `phases[]` with `currentPhase` highlighted.
  - **Footer:** token usage (input/output/cache), total cost, duration.
  - **Result:** §5.4 structured contract (read: `summary`, `findings[]` with
    severity, `confidence`, `open_questions[]`; write: `summary`,
    `files_changed[]`, `diff_summary`, `tests_run`, `notes`). Degenerate /
    off-contract result → small "resultado fora do contrato" with the raw value
    (never a blank panel).
  - **Error:** `errorCode` + message for failed/aborted.
- **Realtime:** `EventSource('/events')` replaces the light state and re-renders,
  **preserving selection and scroll**. When the selected job is **live**, the
  browser re-fetches `GET /api/job/{id}` on each list tick; when **terminal**, it
  fetches once and freezes. `EventSource` auto-reconnects; the dot reflects state.
- **Empty state:** "aguardando o pi-mcp…" + the state-dir path.

---

## 8. Exposure & deploy

### 8.1 Bind
Default: auto-detect the host's Tailscale IPv4 (`tailscale ip -4`, or the
`100.64.0.0/10` CGNAT interface) and bind `<tailscale-ip>:7777`. `--addr`
overrides. **Fail fast** if no tailnet address is found and no `--addr` given (so
it never silently falls back to LAN/public). HTTP plain on the tailnet; no
app-level auth — **tailnet membership is the security boundary**.

### 8.2 systemd
A `pi-dashboard.service` unit: runs as the user (same `HOME`/env as the pi-mcp
server so `registry.json` resolves identically), `Restart=always`,
`WantedBy=multi-user.target`. Installed via `systemctl --user enable --now` (or
system unit with `User=`). `ExecStart=/usr/local/bin/pi-dashboard`.

---

## 9. Error handling

| Condition | Behavior |
|-----------|----------|
| Registry file absent | Empty state ("aguardando o pi-mcp"); keep polling until it appears |
| Registry decode error (mid-rename / partial) | Keep last good state, log, retry next tick (never blank the UI) |
| Run file corrupt | `runstore.ReadRun` `.bak` fallback; if both fail, show last-known + soft warning |
| Blind window (runId empty / file absent) | Show job as "authoring…", no fleet yet |
| Result off-contract / non-object | Show raw value under "resultado fora do contrato" |
| SSE client disconnect | Hub drops the client; no leak |
| No tailnet address & no `--addr` | Fail fast with a clear message |
| Port in use | Fail fast with a clear message |

The dashboard **never writes** to any pi-mcp/pi file (read-only guarantee).

---

## 10. Testing

- **`state` builder:** golden tests over existing fixtures (`sample-run.json`,
  `sample-run-partial.json`, `sample-run-partial-paused.json`,
  `sample-run-no-workflow.jsonl`) + a synthetic `registry.json`; assert derived
  `JobSummary`/`JobDetail` (status mapping, blind window, fleet histogram,
  intermediates join, live token sum, cost only-at-end).
- **`trigger`:** mutate a temp registry/run file via atomic rename; assert a
  rebuild reflects the change within ~1 tick; assert terminal-job caching skips
  re-reads.
- **`hub`:** fake SSE client receives the framed event; disconnect → cleanup.
- **`server`:** `httptest` — `GET /` serves embedded HTML; `/api/state` returns
  current JSON; `/events` sets `text/event-stream` + streams the initial snapshot;
  `/api/job/{id}` returns the heavy detail; unknown id → 404.
- **liveness helper:** unit tests proving dashboard status == `pi_status` status
  across stale/worktree-active/terminal cases.
- Optional smoke: run `pi-dashboard --state-dir <tmp>` and hit `/api/state`.

---

## 11. Scope / YAGNI

**In:** read-only live + history + drill-down; poll-1s; Tailscale-IP bind;
systemd; structured §5.4 result view.

**Out (v1):** auth (tailnet is the boundary); DB / extra retention (run files are
the store); control actions (cancel/launch); charts/analytics; authored-script &
logs viewer; orphan run files not in the registry; `fsnotify`; Docker.

---

## 12. Layout

```
cmd/pi-dashboard/main.go            # flags (--addr, --state-dir, --interval), wire, serve
internal/dashboard/
  state.go        state_test.go     # view-model builder (light + heavy)
  trigger.go      trigger_test.go   # poll loop seam + terminal-job cache
  hub.go          hub_test.go       # SSE broadcaster
  server.go       server_test.go    # http handlers + go:embed
  liveness.go                       # (or shared helper) status derivation reuse
  web/index.html  web/app.js  web/app.css
internal/config (or new shared)     # exported state-path resolution
deploy/pi-dashboard.service         # systemd unit
```
