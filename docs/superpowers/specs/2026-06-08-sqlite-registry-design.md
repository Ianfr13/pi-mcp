# SQLite Job Registry (multi-writer safe) — Design Spec

**Date:** 2026-06-08
**Status:** Approved (brainstorming) — pending implementation plan
**Scope:** Replace pi-mcp's whole-file JSON job registry with a SQLite database so that **multiple concurrent pi-mcp server processes** (one per Claude Code session — observed 5 live) and the dashboard share job state safely, and a starting server can never clobber or destroy another live server's jobs/worktrees. Target scale: trivial (~20–30 concurrent jobs across N servers). Also bundles a one-line fix to the forcing prompt (`agentTimeoutMs`) that is the real cause of coding fan-outs producing no code.

---

## 1. Problem (root cause, evidence-backed)

The registry is `$XDG_STATE_HOME/pi-mcp/registry.json`, written by each server via `persist(path, recordsUnlocked())` — a **whole-file overwrite of only that server's in-memory jobs**. With N concurrent servers:

1. **Clobber:** server B's flush erases server A's jobs from the file. Jobs "vanish from the registry"; `pi_status` then falls back to `UNKNOWN` (couldn't find the record), which is what made failures look like a mysterious "engine decomposition bug."
2. **Destructive cross-server reconcile (worse):** on startup, `Reconcile` loads the (clobbered) file and `gcOrphanWorktrees` **prunes any `pi-mcp/job-*` worktree whose jobId isn't in the loaded set** — so a starting server can delete the worktree of another session's **live** write job → "Workflow aborted" / file errors mid-run.

Evidence: 5 live `pi-mcp` processes; a real failed job (`ea7bfc1f`) had a valid run file on disk (authored fine, agents timed out) but was **absent from `registry.json`** (clobbered); `d129db4c` = "Workflow aborted".

A separate, real cause of coding-job failure (not registry-related): the engine's **300 s per-agent timeout** killed every TDD coding agent (`ea7bfc1f` run log: 4× `timed out after 300000ms`). The `workflow` tool accepts an optional `agentTimeoutMs` (engine `workflow-tool.ts:84`, default 300000) that pi-mcp's forcing prompt never sets.

## 2. Decision (from brainstorming)

- **Registry → SQLite** (`registry.db`), WAL mode, pure-Go driver. Per-row UPSERT (never whole-table rewrite). Owner-scoped reconcile/GC. The dashboard reads the DB.
- **Bundle** the `agentTimeoutMs` forcing-prompt fix (~20 min) so coding fleets stop dying at 5 min.
- Driver: **`modernc.org/sqlite`** (pure Go, **cgo-free** — keeps `go build`/`CGO_ENABLED=0` clean; no C toolchain). Accessed via `database/sql` (driver name `"sqlite"`).

## 3. Why SQLite handles this

SQLite in **WAL mode** is built for multiple processes on one DB file on a local filesystem: concurrent readers never block, writers serialize via a short lock (`busy_timeout` handles contention). For ~20–30 jobs with infrequent state transitions, write contention is negligible. Transactions give us atomic per-row claims (no double-GC, no clobber) for free — no hand-rolled file sharding or flock.

## 4. Storage

`$XDG_STATE_HOME/pi-mcp/registry.db` (+ WAL sidecars `-wal`/`-shm`; local fs only, which is the case). `config.RegistryPath()` returns this `.db` path (both the MCP server and the dashboard derive it from the same `StateDir()`).

PRAGMAs on open: `journal_mode=WAL`, `busy_timeout=5000`, `synchronous=NORMAL`, `foreign_keys=ON`.

Schema (one row per job; mirrors `model.JobRecord` + ownership):
```sql
CREATE TABLE IF NOT EXISTS jobs (
  jobId         TEXT PRIMARY KEY,
  runId         TEXT NOT NULL DEFAULT '',
  sessionId     TEXT NOT NULL DEFAULT '',
  mode          TEXT NOT NULL,
  cwd           TEXT NOT NULL,
  runsDir       TEXT NOT NULL,
  worktreePath  TEXT NOT NULL DEFAULT '',
  branch        TEXT NOT NULL DEFAULT '',
  pid           INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL,
  startedAt     TEXT NOT NULL,              -- RFC3339Nano
  errorCode     TEXT NOT NULL DEFAULT '',
  errorMessage  TEXT NOT NULL DEFAULT '',
  ownerPid      INTEGER NOT NULL,           -- the pi-mcp server process that owns this job
  ownerStartedAt TEXT NOT NULL DEFAULT '',  -- owner server boot time (PID-reuse tiebreak)
  updatedAt     TEXT NOT NULL              -- RFC3339Nano, last write
);
```
`model.JobRecord` is unchanged (the persisted wire type stays JSON-tagged for the MCP outputs); ownership lives only in the DB layer.

## 5. Components

- **`internal/jobs/store.go` (new, replaces `persist.go`)** — a `regStore` over `*sql.DB`:
  - `OpenStore(path string) (*regStore, error)` — opens (creating parent dir), sets PRAGMAs, runs the `CREATE TABLE IF NOT EXISTS`.
  - `UpsertJobs(recs []model.JobRecord, ownerPid int, ownerStartedAt string) error` — one transaction, `INSERT … ON CONFLICT(jobId) DO UPDATE SET …` for each (only the caller's own jobs). Never deletes others' rows.
  - `AllJobs() ([]model.JobRecord, []ownerInfo, error)` — SELECT all rows → records + per-row `{ownerPid, ownerStartedAt}` (for reconcile).
  - `ClaimTerminal(jobID, status, errorCode, byPid int) (claimed bool, err error)` — atomic `UPDATE jobs SET status=?, errorCode=?, ownerPid=? WHERE jobId=? AND status NOT IN ('completed','failed','aborted')`; `claimed = RowsAffected==1` (so two starting servers can't both adopt the same dead job).
  - `Close() error`.
- **`internal/jobs/registry.go` (modify)** — `Registry` holds `store *regStore`, `ownerPid int` (`os.Getpid()`), `ownerStartedAt string` (process start, captured once at `NewRegistry`). `flushUnlocked()` → `store.UpsertJobs(recordsUnlocked(), r.ownerPid, r.ownerStartedAt)` (upserts only this server's jobs — minimal call-site change; still cheap at this scale). The mandatory Submit flush keeps its rollback-on-error semantics.
- **`internal/jobs/reconcile.go` (modify)** — `Reconcile` becomes **owner-scoped**:
  - `store.AllJobs()`; for each non-terminal row whose **ownerPid is DEAD** (`syscall.Kill(pid,0)` fails) → `ClaimTerminal(jobID, failed, SERVER_RESTARTED, me)`; if claimed AND write-mode → `pruner.Prune(worktree, branch)` (destructive GC ONLY for confirmed-dead owners). Rows with a **live owner are never touched** (no status change, no worktree prune).
  - This server does NOT re-adopt its own prior jobs (after a restart it is a new PID; its old rows are dead-owner and get terminalized like any other — correct, since their pi processes died with the old server).
  - Keep the existing queued→failed and stale-running heuristics, but gated on dead-owner (never terminalize a live owner's job).
- **`internal/config/config.go` (modify)** — `RegistryPath()` → `<StateDir>/pi-mcp/registry.db`.
- **`internal/dashboard/registry.go` (modify)** — `ReadRegistry(dbPath)` opens the SQLite DB **read-only** (`busy_timeout`, query_only), `SELECT * FROM jobs` → `[]model.JobRecord`. Missing DB file → empty slice, no error (today's behavior). The dashboard already applies liveness derivation on top; that is unchanged.
- **`internal/app/app.go`** — passes `config.RegistryPath()` (now `.db`) to `jobs.Config.PersistPath` (rename the field conceptually to a DB path; value flows the same way). The registry opens the store at build time and `Close()`s it on shutdown (already `defer reg.Close()`).
- **`go.mod`** — add `modernc.org/sqlite` (pure Go).

The **in-memory job map stays** (it owns the live ctx/cancel/channels for running jobs — not persistable). SQLite replaces only the JSON persistence + cross-process visibility layer.

## 6. agentTimeoutMs bundle

`internal/config/config.go` `ForcingPromptTemplate`: add a directive next to the existing "NO token budget" one, instructing the orchestrator to pass `agentTimeoutMs: 1200000` (20 min) to the `workflow` tool so fleet agents (esp. TDD coding agents) aren't killed at the 5-min default. One sentence; no other behavior change. (The engine reads `params.agentTimeoutMs`; verified in the fork.)

## 7. Migration

On `OpenStore`, if the legacy `registry.json` exists in the same dir and the `jobs` table is empty, **best-effort import** its records once (as `ownerPid=0` → treated as dead-owner → reconciled/terminalized on the next pass), then rename it to `registry.json.migrated`. If import fails, log and continue (the old records are mostly e2e/terminal junk; a fresh DB is acceptable). After migration the JSON file is never read or written again.

## 8. Concurrency & correctness

- **No clobber:** each server only UPSERTs its own jobIds; SELECTs union all. SQLite serializes the writes.
- **No destructive cross-server GC:** worktree prune happens only after `ClaimTerminal` succeeds for a **dead-owner** job (atomic claim → exactly one server prunes; never a live owner's worktree).
- **PID-reuse safety:** GC is gated on `ownerPid` being dead *now*; `ownerStartedAt` is stored for future tiebreaking but the conservative rule (only prune confirmed-dead PIDs) already avoids nuking a live job. Non-destructive staleness display is handled by the readers' existing liveness logic.
- **Crash recovery:** a crashed server's jobs are terminalized + GC'd by the **next** server that starts (or are shown failed-by-staleness by readers meanwhile).
- **WAL on local fs only** — `$XDG_STATE_HOME` is local; documented assumption.

## 9. Error handling

| Condition | Behavior |
|-----------|----------|
| DB open fails at server start | fatal for that server (can't persist) — same gravity as today's mkdir failure |
| `UpsertJobs` error mid-run | log-and-continue (a launch never blocks on persistence), except the **mandatory Submit insert** which rolls back + fails (unchanged semantics) |
| Busy/locked write | `busy_timeout=5000` retries; transient |
| Dashboard: DB absent | empty job list, no error |
| Dashboard: DB locked during read | WAL readers don't block on writers; `busy_timeout` covers the rare checkpoint |
| Legacy `registry.json` import fails | log, start fresh |

## 10. Testing

- **store:** open temp DB → upsert → AllJobs round-trips; reopen persists; `ClaimTerminal` returns claimed once then false (idempotent).
- **multi-writer:** two `regStore` (or two `Registry`) on the **same temp DB** upsert disjoint jobs concurrently → both jobs present, neither clobbered (the core regression test for the bug).
- **owner-scoped reconcile:** seed a dead-owner non-terminal row + a live-owner (self) row → reconcile terminalizes + GCs the dead one, leaves the live one untouched; assert the live job's worktree is NOT pruned.
- **dashboard:** `ReadRegistry` over a temp DB returns the rows; missing DB → empty.
- **migration:** a legacy `registry.json` present + empty table → rows imported once, file renamed `.migrated`.
- **existing jobs tests:** migrate from `PersistPath=<tmp.json>` to a temp `.db` (via `OpenStore`); keep their assertions.
- **forcing prompt:** assert the rendered prompt contains the `agentTimeoutMs` directive (extend the runner prompt test).
- Gate: `go build` (cgo-free), `go vet`, `go test -race ./...`, `gofmt -l`.

## 11. Scope / YAGNI

**In:** SQLite store + WAL + per-row upsert + owner-scoped reconcile/GC + dashboard DB reader + config path + go.mod dep + best-effort JSON migration + agentTimeoutMs prompt directive.

**Out:** a shared registry daemon; cross-session `pi_status` by another server's jobId (in-memory own-jobs path stays); historical analytics tables; changing `model.JobRecord`'s JSON shape (MCP outputs unchanged); network-filesystem WAL.

## 12. File-touch summary

```
go.mod                              # + modernc.org/sqlite
internal/config/config.go          # RegistryPath -> registry.db; ForcingPromptTemplate += agentTimeoutMs directive
internal/jobs/store.go (new)       # regStore: Open/UpsertJobs/AllJobs/ClaimTerminal/Close (+ migration)
internal/jobs/persist.go (remove)  # replaced by store.go
internal/jobs/registry.go          # hold *regStore + ownerPid/ownerStartedAt; flush -> UpsertJobs
internal/jobs/reconcile.go         # owner-scoped terminalize + GC (dead-owner only, atomic claim)
internal/jobs/*_test.go            # migrate to temp .db; + multi-writer + owner-scoped tests
internal/app/app.go                # registry path is the .db; open/close store
internal/dashboard/registry.go     # ReadRegistry reads SQLite (read-only)
internal/dashboard/*_test.go       # ReadRegistry over temp .db
internal/runner/prompt_test.go     # assert agentTimeoutMs directive present
```
