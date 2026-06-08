# SQLite Job Registry (multi-writer safe) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace pi-mcp's whole-file JSON job registry with a SQLite (WAL) database so N concurrent pi-mcp servers + the dashboard share job state without clobbering each other or destroying each other's worktrees; plus a one-line forcing-prompt fix (`agentTimeoutMs`).

**Architecture:** A `regStore` over `database/sql` + pure-Go `modernc.org/sqlite` (cgo-free). Each server UPSERTs only its own job rows (no whole-table rewrite → no clobber). Reconcile is owner-scoped: it only terminalizes/GCs rows whose owning process is **dead**, via an atomic `ClaimTerminal` UPDATE, so a starting server never touches a live server's job or worktree. The dashboard reads the DB. The in-memory job map stays (it owns live ctx/cancel handles).

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure Go), `database/sql`, stdlib `testing`.

**Reference spec:** `docs/superpowers/specs/2026-06-08-sqlite-registry-design.md`

---

## Conventions

- Module `pi-mcp`. Stdlib `testing` only (no testify). Run: `go test -race ./...`. Build MUST stay cgo-free: `CGO_ENABLED=0 go build ./...` works.
- Commit after each task with the shown message. You are on branch `build/dashboard`.
- `model.JobRecord` (in `internal/model/model.go`) is the persisted record; its fields: `JobID, RunID, SessionID string; Mode model.JobMode; CWD, RunsDir, WorktreePath, Branch string; PID int; Status model.JobStatus; StartedAt time.Time; ErrorCode, ErrorMessage string`. **Do not change it.**

## File Structure

```
go.mod / go.sum                     # + modernc.org/sqlite                              (Task 1)
internal/jobs/store.go (new)        # regStore: Open/UpsertJobs/AllJobs/ClaimTerminal/Close + migration  (Task 2)
internal/jobs/store_test.go (new)   # replaces persist_test.go                          (Task 2)
internal/jobs/persist.go (DELETE)   # superseded by store.go                            (Task 2)
internal/jobs/persist_test.go (DELETE)                                                  (Task 2)
internal/jobs/registry.go           # hold *regStore + ownerPid/ownerStartedAt; flush->UpsertJobs; NewRegistry returns error; Close closes store  (Task 3)
internal/jobs/reconcile.go          # owner-scoped terminalize + dead-owner-only GC     (Task 4)
internal/jobs/*_test.go             # migrate NewRegistry callsites (+ mustRegistry helper) + readback helper  (Task 3/4)
internal/config/config.go           # RegistryPath -> registry.db; ForcingPromptTemplate += agentTimeoutMs  (Task 5, Task 7)
internal/app/app.go                 # statePaths already returns config.RegistryPath(); NewRegistry now returns error (already handled)  (Task 5)
internal/dashboard/registry.go      # ReadRegistry reads SQLite (read-only)             (Task 6)
internal/dashboard/registry_test.go # ReadRegistry over a temp .db                      (Task 6)
internal/runner/prompt_test.go      # assert agentTimeoutMs directive present           (Task 7)
```

---

## Task 1: Add the `modernc.org/sqlite` dependency (cgo-free) + driver smoke

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/jobs/sqlite_smoke_test.go`

- [ ] **Step 1: Add the dependency**

Run:
```bash
cd /root/pi-mcp
go get modernc.org/sqlite@v1.34.4
```
Expected: `go.mod` gains `require modernc.org/sqlite v1.34.4` (+ indirect modernc deps in go.sum).

- [ ] **Step 2: Write the smoke test (proves the driver works + registers as "sqlite")**

`internal/jobs/sqlite_smoke_test.go`:
```go
package jobs

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteDriverSmoke(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, v INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t(id,v) VALUES('a',1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var v int
	if err := db.QueryRow(`SELECT v FROM t WHERE id='a'`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("select v=%d err=%v", v, err)
	}
}
```

- [ ] **Step 3: Run it (cgo-free)**

Run: `CGO_ENABLED=0 go test ./internal/jobs/ -run SQLiteDriverSmoke -v`
Expected: PASS (proves modernc works without a C toolchain).

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum internal/jobs/sqlite_smoke_test.go
git commit -m "build(jobs): add modernc.org/sqlite (pure-Go) + driver smoke test"
```

---

## Task 2: `regStore` — the SQLite store (replaces persist.go)

**Files:**
- Create: `internal/jobs/store.go`
- Create: `internal/jobs/store_test.go`
- Delete: `internal/jobs/persist.go`, `internal/jobs/persist_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/jobs/store_test.go`:
```go
package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/model"
)

func tmpDB(t *testing.T) string { return filepath.Join(t.TempDir(), "registry.db") }

func rec(id, status string) model.JobRecord {
	return model.JobRecord{JobID: id, Mode: model.ModeRead, CWD: "/c", RunsDir: "/c/.pi/workflows/runs",
		Status: model.JobStatus(status), StartedAt: timeNowUTC()}
}

func TestStore_UpsertAndAll(t *testing.T) {
	s, err := OpenStore(tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertJobs([]model.JobRecord{rec("a", "running"), rec("b", "queued")}, 100, "t0"); err != nil {
		t.Fatal(err)
	}
	// update a
	if err := s.UpsertJobs([]model.JobRecord{rec("a", "completed")}, 100, "t0"); err != nil {
		t.Fatal(err)
	}
	recs, owners, err := s.AllJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 rows, got %d", len(recs))
	}
	m := map[string]model.JobRecord{}
	for _, r := range recs {
		m[r.JobID] = r
	}
	if string(m["a"].Status) != "completed" {
		t.Errorf("a status = %q want completed (upsert)", m["a"].Status)
	}
	if len(owners) != len(recs) || owners[0].Pid == 0 {
		t.Errorf("owners not populated: %+v", owners)
	}
}

func TestStore_MultiWriterNoClobber(t *testing.T) {
	path := tmpDB(t)
	a, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenStore(path) // second server, same DB
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.UpsertJobs([]model.JobRecord{rec("a1", "running")}, 1, "ta"); err != nil {
		t.Fatal(err)
	}
	if err := b.UpsertJobs([]model.JobRecord{rec("b1", "running")}, 2, "tb"); err != nil {
		t.Fatal(err)
	}
	// each writer only wrote its own row; NEITHER clobbered the other.
	recs, _, err := b.AllJobs()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range recs {
		got[r.JobID] = true
	}
	if !got["a1"] || !got["b1"] {
		t.Fatalf("multi-writer clobber: rows=%v want a1 AND b1", got)
	}
}

func TestStore_ClaimTerminalIdempotent(t *testing.T) {
	s, err := OpenStore(tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.UpsertJobs([]model.JobRecord{rec("x", "running")}, 1, "t")
	ok, err := s.ClaimTerminal("x", "failed", "SERVER_RESTARTED", 9, "t9")
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v want true", ok, err)
	}
	ok2, err := s.ClaimTerminal("x", "failed", "SERVER_RESTARTED", 9, "t9")
	if err != nil || ok2 {
		t.Fatalf("second claim ok=%v err=%v want false (already terminal)", ok2, err)
	}
}

func TestStore_MigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "registry.db")
	legacy := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(legacy, []byte(`{"jobs":[{"jobId":"old1","mode":"read","cwd":"/c","runsDir":"/c/r","status":"running","startedAt":"2026-06-07T00:00:00Z"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recs, _, err := s.AllJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].JobID != "old1" {
		t.Errorf("legacy import failed: %+v", recs)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy registry.json should be renamed away, stat err=%v", err)
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Errorf("expected registry.json.migrated, err=%v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/jobs/ -run TestStore`
Expected: FAIL (OpenStore/timeNowUTC undefined; persist.go still present is fine).

- [ ] **Step 3: Delete the JSON persistence**

```bash
git rm internal/jobs/persist.go internal/jobs/persist_test.go
```

- [ ] **Step 4: Write `store.go`**

`internal/jobs/store.go`:
```go
package jobs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"pi-mcp/internal/model"
)

// timeNowUTC is the canonical timestamp format used for owner/updated columns.
func timeNowUTC() time.Time { return time.Now().UTC() }

// ownerInfo is the per-row ownership read back by reconcile.
type ownerInfo struct {
	Pid       int
	StartedAt string
}

const schemaDDL = `
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
  startedAt     TEXT NOT NULL,
  errorCode     TEXT NOT NULL DEFAULT '',
  errorMessage  TEXT NOT NULL DEFAULT '',
  ownerPid      INTEGER NOT NULL DEFAULT 0,
  ownerStartedAt TEXT NOT NULL DEFAULT '',
  updatedAt     TEXT NOT NULL
);`

// regStore is the SQLite-backed job registry persistence layer. Multiple
// processes may open the same DB concurrently (WAL); each UPSERTs only its own
// jobs, so writers never clobber each other.
type regStore struct {
	db   *sql.DB
	path string
}

// OpenStore opens (creating the parent dir + schema) the registry DB in WAL mode.
// On first open it best-effort migrates a sibling legacy registry.json.
func OpenStore(path string) (*regStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: schema: %w", err)
	}
	s := &regStore{db: db, path: path}
	if err := s.migrateLegacyJSON(); err != nil {
		// non-fatal: log-and-continue by returning the store anyway
		fmt.Fprintf(os.Stderr, "pi-mcp store: legacy migration skipped: %v\n", err)
	}
	return s, nil
}

func (s *regStore) Close() error { return s.db.Close() }

const upsertSQL = `
INSERT INTO jobs (jobId,runId,sessionId,mode,cwd,runsDir,worktreePath,branch,pid,status,startedAt,errorCode,errorMessage,ownerPid,ownerStartedAt,updatedAt)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(jobId) DO UPDATE SET
  runId=excluded.runId, sessionId=excluded.sessionId, mode=excluded.mode, cwd=excluded.cwd,
  runsDir=excluded.runsDir, worktreePath=excluded.worktreePath, branch=excluded.branch, pid=excluded.pid,
  status=excluded.status, startedAt=excluded.startedAt, errorCode=excluded.errorCode,
  errorMessage=excluded.errorMessage, ownerPid=excluded.ownerPid, ownerStartedAt=excluded.ownerStartedAt,
  updatedAt=excluded.updatedAt;`

// UpsertJobs writes the caller's own records in one transaction, stamping owner.
// It never deletes or rewrites rows it does not own.
func (s *regStore) UpsertJobs(recs []model.JobRecord, ownerPid int, ownerStartedAt string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := timeNowUTC().Format(time.RFC3339Nano)
	for _, r := range recs {
		if _, err := stmt.Exec(r.JobID, r.RunID, r.SessionID, string(r.Mode), r.CWD, r.RunsDir,
			r.WorktreePath, r.Branch, r.PID, string(r.Status), r.StartedAt.UTC().Format(time.RFC3339Nano),
			r.ErrorCode, r.ErrorMessage, ownerPid, ownerStartedAt, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const selectAllSQL = `SELECT jobId,runId,sessionId,mode,cwd,runsDir,worktreePath,branch,pid,status,startedAt,errorCode,errorMessage,ownerPid,ownerStartedAt FROM jobs;`

// AllJobs returns every row as records plus index-aligned owner info.
func (s *regStore) AllJobs() ([]model.JobRecord, []ownerInfo, error) {
	rows, err := s.db.Query(selectAllSQL)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var recs []model.JobRecord
	var owners []ownerInfo
	for rows.Next() {
		var r model.JobRecord
		var mode, status, startedAt, ownerStartedAt string
		var ownerPid int
		if err := rows.Scan(&r.JobID, &r.RunID, &r.SessionID, &mode, &r.CWD, &r.RunsDir, &r.WorktreePath,
			&r.Branch, &r.PID, &status, &startedAt, &r.ErrorCode, &r.ErrorMessage, &ownerPid, &ownerStartedAt); err != nil {
			return nil, nil, err
		}
		r.Mode = model.JobMode(mode)
		r.Status = model.JobStatus(status)
		if t, perr := time.Parse(time.RFC3339Nano, startedAt); perr == nil {
			r.StartedAt = t
		}
		recs = append(recs, r)
		owners = append(owners, ownerInfo{Pid: ownerPid, StartedAt: ownerStartedAt})
	}
	return recs, owners, rows.Err()
}

const claimSQL = `UPDATE jobs SET status=?, errorCode=?, ownerPid=?, ownerStartedAt=?, updatedAt=?
WHERE jobId=? AND status NOT IN ('completed','failed','aborted');`

// ClaimTerminal atomically transitions a non-terminal job to a terminal status,
// taking ownership. Returns true iff THIS call performed the transition (so two
// servers racing to adopt the same dead-owner job never both act).
func (s *regStore) ClaimTerminal(jobID, status, errorCode string, byPid int, byStartedAt string) (bool, error) {
	res, err := s.db.Exec(claimSQL, status, errorCode, byPid, byStartedAt,
		timeNowUTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// migrateLegacyJSON imports a sibling registry.json once (when the table is
// empty), as ownerPid=0 (dead) so reconcile terminalizes it, then renames the
// file to registry.json.migrated.
func (s *regStore) migrateLegacyJSON() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil // already populated
	}
	legacy := filepath.Join(filepath.Dir(s.path), "registry.json")
	data, err := os.ReadFile(legacy)
	if err != nil {
		return nil // no legacy file -> nothing to do
	}
	var pf struct {
		Jobs []model.JobRecord `json:"jobs"`
	}
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("decode legacy: %w", err)
	}
	if len(pf.Jobs) > 0 {
		if err := s.UpsertJobs(pf.Jobs, 0, ""); err != nil { // ownerPid 0 == dead owner
			return fmt.Errorf("import legacy: %w", err)
		}
	}
	return os.Rename(legacy, legacy+".migrated")
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test -race ./internal/jobs/ -run TestStore`
Expected: PASS (all four store tests).

- [ ] **Step 6: Commit**

```bash
git add internal/jobs/store.go internal/jobs/store_test.go
git commit -m "feat(jobs): SQLite regStore (WAL, per-row upsert, atomic claim, legacy migration)"
```

---

## Task 3: Wire the store into Registry (NewRegistry returns error; owner-stamped flush)

**Files:**
- Modify: `internal/jobs/registry.go`
- Modify: `internal/app/app.go`
- Modify: `internal/jobs/registry_test.go`, `internal/jobs/correlate_test.go`, `internal/jobs/reconcile_test.go`, `internal/jobs/cancel_test.go`, `internal/jobs/retry_test.go`, `internal/jobs/job_test.go`, `internal/jobs/liveness_test.go` (any with `NewRegistry(` / `loadPersisted(`)

- [ ] **Step 1: Change `Registry` + `NewRegistry` + flush + Close**

In `internal/jobs/registry.go`:

Add fields to the `Registry` struct (after `now func() time.Time`):
```go
	store          *regStore
	ownerPid       int
	ownerStartedAt string
```

Replace `NewRegistry` to open the store and return an error:
```go
// NewRegistry builds a Registry over a SQLite store at cfg.PersistPath. cfg.Cap<=0
// uses config.DefaultConcurrencyCap. Returns an error if the store cannot open.
func NewRegistry(cfg Config, l Launcher, c Correlator, p Pruner) (*Registry, error) {
	capN := cfg.Cap
	if capN <= 0 {
		capN = config.DefaultConcurrencyCap
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	store, err := OpenStore(cfg.PersistPath)
	if err != nil {
		return nil, err
	}
	return &Registry{
		jobs:           make(map[string]*Job),
		slots:          make(chan struct{}, capN),
		cap:            capN,
		persistPath:    cfg.PersistPath,
		worktreeRoot:   cfg.WorktreeRoot,
		now:            now,
		launcher:       l,
		correlator:     c,
		pruner:         p,
		hasRunFile:     runFileExists,
		store:          store,
		ownerPid:       os.Getpid(),
		ownerStartedAt: now().UTC().Format(time.RFC3339Nano),
	}, nil
}
```
(Add `"os"` and `"time"` to imports if not present — `time` already is; add `os`.)

Replace `flushUnlocked`:
```go
// flushUnlocked persists THIS server's jobs (owner-stamped) to the shared DB.
// It only upserts rows this server owns, so concurrent servers never clobber
// each other. Caller holds mu.
func (r *Registry) flushUnlocked() error {
	return r.store.UpsertJobs(r.recordsUnlocked(), r.ownerPid, r.ownerStartedAt)
}
```

In `Close()`, after the cancels + final flush, close the store. Change the tail of `Close` so it also closes the DB (find the existing `err := r.flushUnlocked()` / return and add `_ = r.store.Close()` before returning):
```go
	err := r.flushUnlocked()
	r.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	_ = r.store.Close()
	return err
```

- [ ] **Step 2: Update `app.go` callsite**

In `internal/app/app.go`, `buildRegistryReal` currently ends with `return jobs.NewRegistry(cfg, realLauncher{}, realCorrelator{}, worktreePruner{}), nil`. Change to:
```go
	return jobs.NewRegistry(
		jobs.Config{
			Cap:          config.DefaultConcurrencyCap,
			PersistPath:  persist,
			WorktreeRoot: wtRoot,
		},
		realLauncher{},
		realCorrelator{},
		worktreePruner{},
	)
```
(i.e. drop the trailing `, nil` — `NewRegistry` now returns `(*Registry, error)`, which matches `buildRegistryReal`'s signature.)

- [ ] **Step 3: Add test helpers + migrate callsites**

In `internal/jobs/registry_test.go`, add near the top (after imports):
```go
// mustRegistry builds a Registry over a temp SQLite DB, failing the test on error.
func mustRegistry(t *testing.T, cfg Config, l Launcher, c Correlator, p Pruner) *Registry {
	t.Helper()
	r, err := NewRegistry(cfg, l, c, p)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

// readBackJobs opens a fresh store on dbPath and returns all persisted records
// (replaces the old loadPersisted(...) readback).
func readBackJobs(t *testing.T, dbPath string) []model.JobRecord {
	t.Helper()
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	recs, _, err := s.AllJobs()
	if err != nil {
		t.Fatalf("AllJobs: %v", err)
	}
	return recs
}
```

Then mechanically migrate EVERY test callsite across the `internal/jobs/*_test.go` files:
- `NewRegistry(Config{... PersistPath: filepath.Join(dir, "r.json")}, fl, fc, p)` → `mustRegistry(t, Config{... PersistPath: filepath.Join(dir, "registry.db")}, fl, fc, p)`.
- The `newTestRegistry(t)` helper (registry_test.go:15) — change its body to use `mustRegistry` + `PersistPath: filepath.Join(dir, "registry.db")`.
- `loadPersisted(filepath.Join(dir, "r.json"))` (registry_test.go ~line 89) → `readBackJobs(t, filepath.Join(dir, "registry.db"))`.
- Remove any now-unused `filepath`/`loadPersisted` references; `model` is already imported.

(There is no behavioral change to assert here beyond "still persists/reads back"; the existing assertions stay, now reading from SQLite.)

- [ ] **Step 4: Run the jobs package**

Run: `go test -race ./internal/jobs/`
Expected: PASS (all existing jobs tests, now over SQLite). Fix any leftover `r.json`/`loadPersisted` references the compiler flags.

- [ ] **Step 5: Run app package**

Run: `go test -race ./internal/app/`
Expected: PASS (buildRegistryReal compiles with the new signature).

- [ ] **Step 6: Commit**

```bash
git add internal/jobs/registry.go internal/app/app.go internal/jobs/*_test.go
git commit -m "feat(jobs): Registry over SQLite store (owner-stamped flush, NewRegistry returns error)"
```

---

## Task 4: Owner-scoped Reconcile (dead-owner only; atomic claim; safe GC)

**Files:**
- Modify: `internal/jobs/reconcile.go`
- Modify: `internal/jobs/reconcile_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/jobs/reconcile_test.go`:
```go
func TestReconcile_OwnerScoped(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "registry.db")
	// Seed the DB directly: one DEAD-owner running write job, one LIVE-owner (self) running job.
	seed, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	deadWrite := model.JobRecord{JobID: "dead1", Mode: model.ModeWrite, CWD: "/c",
		RunsDir: "/c/r", WorktreePath: "/wt/job-dead1", Branch: "pi-mcp/job-dead1",
		Status: model.JobRunning, StartedAt: timeNowUTC()}
	_ = seed.UpsertJobs([]model.JobRecord{deadWrite}, 999999, "old") // pid 999999 = not alive
	liveRead := model.JobRecord{JobID: "live1", Mode: model.ModeRead, CWD: "/c",
		RunsDir: "/c/r", Status: model.JobRunning, StartedAt: timeNowUTC()}
	_ = seed.UpsertJobs([]model.JobRecord{liveRead}, os.Getpid(), "self") // self pid = alive
	seed.Close()

	pruner := &fakePruner{}
	r, err := NewRegistry(Config{Cap: 4, PersistPath: dbPath, WorktreeRoot: dir}, newFakeLauncher("s"), &fakeCorrelator{}, pruner)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	recs := readBackJobs(t, dbPath)
	m := map[string]model.JobRecord{}
	for _, x := range recs {
		m[x.JobID] = x
	}
	if string(m["dead1"].Status) != "failed" {
		t.Errorf("dead-owner job: status=%q want failed", m["dead1"].Status)
	}
	if string(m["live1"].Status) != "running" {
		t.Errorf("live-owner job MUST be untouched: status=%q want running", m["live1"].Status)
	}
	// dead-owner write job's worktree IS pruned; live owner's is NOT.
	if !pruner.prunedBranch("pi-mcp/job-dead1") {
		t.Errorf("dead-owner worktree should be pruned; pruned=%v", pruner.branches())
	}
}
```
(If `fakePruner` does not already record pruned branches, add to it in `testhelpers_test.go`: a `mu sync.Mutex; pruned []string` field; `Prune(wt, branch) error { f.mu.Lock(); f.pruned=append(f.pruned,branch); f.mu.Unlock(); return nil }`; helpers `prunedBranch(b string) bool` and `branches() []string`. Match the existing `Pruner` interface signature `Prune(worktreePath, branch string) error`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/jobs/ -run TestReconcile_OwnerScoped`
Expected: FAIL (current Reconcile uses loadPersisted/effectiveStatus, not owner-scoped; may panic or mis-handle).

- [ ] **Step 3: Rewrite `Reconcile`**

Replace the body of `Reconcile` in `internal/jobs/reconcile.go` with the owner-scoped version (keep the function signature `func (r *Registry) Reconcile(ctx context.Context) (int, error)`):
```go
// Reconcile loads all rows from the shared DB and cleans up ONLY jobs whose
// owning server process is dead: it atomically claims each dead-owner non-terminal
// job as failed (SERVER_RESTARTED) and prunes its worktree (write mode). Jobs
// owned by a LIVE server (including another concurrent pi-mcp) are never touched —
// this is what makes the registry safe for N concurrent servers. The atomic claim
// ensures two starting servers never both prune the same job. Returns the number
// of rows seen.
func (r *Registry) Reconcile(ctx context.Context) (int, error) {
	recs, owners, err := r.store.AllJobs()
	if err != nil {
		return 0, err
	}
	for i := range recs {
		rec := recs[i]
		own := owners[i]
		if isTerminal(rec.Status) {
			continue
		}
		if pidAlive(own.Pid) {
			continue // a live server owns this job — hands off
		}
		// dead owner: atomically claim it terminal. Only the winner prunes.
		claimed, cerr := r.store.ClaimTerminal(rec.JobID, string(model.JobFailed),
			config.ErrServerRestarted, r.ownerPid, r.ownerStartedAt)
		if cerr != nil || !claimed {
			continue
		}
		if rec.Mode == model.ModeWrite && rec.WorktreePath != "" {
			_ = r.pruner.Prune(rec.WorktreePath, rec.Branch)
		}
	}
	return len(recs), nil
}
```
Then DELETE the now-unused `gcOrphanWorktrees` method and any now-unused imports (`os`, `path/filepath`, `strings` may become unused in reconcile.go — remove them; keep `context`, `pi-mcp/internal/config`, `pi-mcp/internal/model`). Keep `isTerminal` (defined in reconcile.go) and `pidAlive` (defined in liveness.go).

NOTE: `pidAlive` lives in `internal/jobs/liveness.go` (same package) and treats pid<=0 as dead — so the legacy-migrated rows (ownerPid 0) are correctly treated as dead-owner and terminalized.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/jobs/`
Expected: PASS (new owner-scoped test + existing jobs tests). If an existing reconcile test asserted the OLD orphan-GC-by-absence behavior, update it to the owner-scoped semantics (dead-owner → cleaned; otherwise untouched).

- [ ] **Step 5: Commit**

```bash
git add internal/jobs/reconcile.go internal/jobs/reconcile_test.go internal/jobs/testhelpers_test.go
git commit -m "feat(jobs): owner-scoped reconcile — never touch a live server's job/worktree"
```

---

## Task 5: Point the registry path at `registry.db`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Update the failing test**

In `internal/config/config_test.go`, the `TestRegistryPath` expectation changes to `.db`:
```go
func TestRegistryPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	want := "/xdg/state/pi-mcp/registry.db"
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath()=%q want %q", got, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/config/ -run TestRegistryPath`
Expected: FAIL (still returns registry.json).

- [ ] **Step 3: Update `RegistryPath`**

In `internal/config/config.go`, change the `RegistryPath` body + doc:
```go
// RegistryPath is the SQLite job-registry DB: <StateDir>/pi-mcp/registry.db.
func RegistryPath() string {
	return filepath.Join(StateDir(), "pi-mcp", "registry.db")
}
```

- [ ] **Step 4: Run + full app/jobs gate**

Run: `go test ./internal/config/ -run TestRegistryPath && go test -race ./internal/app/ ./internal/jobs/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): registry path -> registry.db (SQLite)"
```

---

## Task 6: Dashboard reads the SQLite registry

**Files:**
- Modify: `internal/dashboard/registry.go`
- Modify: `internal/dashboard/registry_test.go`

- [ ] **Step 1: Rewrite the failing test**

Replace `internal/dashboard/registry_test.go` with a SQLite-backed version:
```go
package dashboard

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"pi-mcp/internal/model"
)

// writeJobsDB creates a registry.db with the given records (mirrors the jobs schema).
func writeJobsDB(t *testing.T, recs []model.JobRecord) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE jobs (jobId TEXT PRIMARY KEY, runId TEXT, sessionId TEXT, mode TEXT, cwd TEXT, runsDir TEXT, worktreePath TEXT, branch TEXT, pid INTEGER, status TEXT, startedAt TEXT, errorCode TEXT, errorMessage TEXT, ownerPid INTEGER, ownerStartedAt TEXT, updatedAt TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if _, err := db.Exec(`INSERT INTO jobs (jobId,runId,sessionId,mode,cwd,runsDir,worktreePath,branch,pid,status,startedAt,errorCode,errorMessage,ownerPid,ownerStartedAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.JobID, r.RunID, r.SessionID, string(r.Mode), r.CWD, r.RunsDir, r.WorktreePath, r.Branch, r.PID, string(r.Status), r.StartedAt.UTC().Format(time.RFC3339Nano), r.ErrorCode, r.ErrorMessage, 1, "t", "u"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestReadRegistry_Missing(t *testing.T) {
	recs, err := ReadRegistry(filepath.Join(t.TempDir(), "nope.db"))
	if err != nil {
		t.Fatalf("missing db should be no error, got %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("want 0 records, got %d", len(recs))
	}
}

func TestReadRegistry_ReadsRows(t *testing.T) {
	path := writeJobsDB(t, []model.JobRecord{
		{JobID: "job-completed", Mode: model.ModeRead, Status: model.JobCompleted, RunID: "r1", StartedAt: time.Now()},
		{JobID: "job-write", Mode: model.ModeWrite, Status: model.JobRunning, Branch: "pi-mcp/job-write", StartedAt: time.Now()},
	})
	recs, err := ReadRegistry(path)
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	m := map[string]model.JobRecord{}
	for _, r := range recs {
		m[r.JobID] = r
	}
	if m["job-completed"].Status != model.JobCompleted || m["job-write"].Mode != model.ModeWrite {
		t.Errorf("decoded wrong: %+v", recs)
	}
}
```
(Delete the old `internal/dashboard/testdata/registry.json` if a test referenced it; the new tests build a temp DB.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run ReadRegistry`
Expected: FAIL (ReadRegistry still parses JSON).

- [ ] **Step 3: Rewrite `ReadRegistry`**

Replace `internal/dashboard/registry.go`:
```go
// Package dashboard implements pi-dashboard: a read-only realtime viewer of
// pi-mcp workflows. It reads the SQLite job registry + the per-job run files,
// derives a view-model, and serves it over HTTP + SSE.
package dashboard

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"time"

	_ "modernc.org/sqlite"

	"pi-mcp/internal/model"
)

// ReadRegistry reads all job rows from the SQLite registry DB (read-only). A
// missing DB is not an error (the pi-mcp server may not have run yet) and yields
// an empty slice. Multiple pi-mcp servers may be writing concurrently; WAL
// readers never block on writers.
func ReadRegistry(dbPath string) ([]model.JobRecord, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return []model.JobRecord{}, nil
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT jobId,runId,sessionId,mode,cwd,runsDir,worktreePath,branch,pid,status,startedAt,errorCode,errorMessage FROM jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.JobRecord{}
	for rows.Next() {
		var r model.JobRecord
		var mode, status, startedAt string
		if err := rows.Scan(&r.JobID, &r.RunID, &r.SessionID, &mode, &r.CWD, &r.RunsDir,
			&r.WorktreePath, &r.Branch, &r.PID, &status, &startedAt, &r.ErrorCode, &r.ErrorMessage); err != nil {
			return nil, err
		}
		r.Mode = model.JobMode(mode)
		r.Status = model.JobStatus(status)
		if t, perr := time.Parse(time.RFC3339Nano, startedAt); perr == nil {
			r.StartedAt = t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run dashboard tests**

Run: `go test -race ./internal/dashboard/`
Expected: PASS. (The poller/server/state tests call `ReadRegistry` via the poller; they inject `p.readRegistry` in tests so they are unaffected, but confirm the package compiles and all pass.)

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/registry.go internal/dashboard/registry_test.go
git rm -f internal/dashboard/testdata/registry.json 2>/dev/null || true
git commit -m "feat(dashboard): read the SQLite registry (WAL, read-only)"
```

---

## Task 7: `agentTimeoutMs` forcing-prompt directive

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/runner/prompt_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/runner/prompt_test.go`:
```go
func TestRenderPrompt_IncludesAgentTimeout(t *testing.T) {
	out, err := RenderPrompt("read", "do a thing", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agentTimeoutMs") {
		t.Errorf("prompt missing agentTimeoutMs directive:\n%s", out)
	}
	if !strings.Contains(out, "1200000") {
		t.Errorf("prompt missing the 20-min timeout value:\n%s", out)
	}
}
```
(Ensure `"strings"` is imported in prompt_test.go.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/runner/ -run IncludesAgentTimeout`
Expected: FAIL (template has no agentTimeoutMs).

- [ ] **Step 3: Add the directive to the template**

In `internal/config/config.go`, edit `ForcingPromptTemplate` — add one sentence right after the existing "NO token budget" block (before the `The workflow MUST return…` line):
```go
const ForcingPromptTemplate = `You MUST make exactly ONE call to the ` + "`workflow`" + ` tool with background:false. Do not answer
directly. Do not use background:true. Return the final synthesized result INLINE this turn.
Decompose the task as you see fit and fan out subagents in parallel.
Run the workflow to completion with NO token budget: do NOT pass tokenBudget to the workflow
tool (leave it unlimited) and do NOT set per-phase budgets — never stop or throttle the run for
token or cost reasons.
Give agents room to finish: pass agentTimeoutMs:1200000 (20 minutes) to the workflow tool so
coding/TDD agents are not killed by the 5-minute default per-agent timeout.
The workflow MUST return an object matching exactly this JSON shape:
{{CONTRACT}}

TASK:
{{TASK}}

[CONTEXT:
{{CONTEXT}}]`
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/runner/`
Expected: PASS (new test + existing prompt tests; the existing tests assert TASK/CONTEXT handling which is unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/runner/prompt_test.go
git commit -m "feat(prompt): instruct orchestrator to pass agentTimeoutMs:1200000 (fix 5-min agent kills)"
```

---

## Task 8: Final gate

- [ ] **Step 1: Full race suite (cgo-free build path)**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && gofmt -l internal/ cmd/ && go test -race ./...`
Expected: builds cgo-free; `gofmt -l` prints nothing; `go vet` clean; all packages PASS (incl. e2e).

- [ ] **Step 2: Concurrency smoke (two registries, one DB, real reconcile)**

Run:
```bash
go test -race ./internal/jobs/ -run 'TestStore_MultiWriterNoClobber|TestReconcile_OwnerScoped' -count=1 -v
```
Expected: PASS — the two core regression tests for the multi-writer + owner-scoped guarantees.

- [ ] **Step 3: Commit any gofmt fixes**

```bash
gofmt -w internal/ cmd/
git add -A && git commit -m "style: gofmt" || echo "nothing to format"
```

---

## Self-Review (completed by plan author)

**Spec coverage:**
- SQLite store, WAL, modernc cgo-free → Task 1 (dep) + Task 2 (store, DSN pragmas). ✔
- Per-row UPSERT, no clobber → Task 2 (`UpsertJobs`) + `TestStore_MultiWriterNoClobber`. ✔
- Owner-scoped reconcile + atomic claim + dead-owner-only GC → Task 4 (`Reconcile` + `ClaimTerminal`) + `TestReconcile_OwnerScoped`. ✔
- Registry path `registry.db` → Task 5. ✔
- Dashboard reads DB, missing→empty → Task 6. ✔
- Legacy `registry.json` one-time migration → Task 2 (`migrateLegacyJSON`) + `TestStore_MigratesLegacyJSON`. ✔
- `agentTimeoutMs` directive → Task 7. ✔
- In-memory map untouched; `model.JobRecord` JSON shape unchanged. ✔
- Build stays cgo-free → Task 1 Step 3 + Task 8 Step 1 (`CGO_ENABLED=0`). ✔

**Placeholder scan:** none. The existing-test migration (Task 3 Step 3) is mechanical (find/replace `r.json`→`registry.db`, `NewRegistry(`→`mustRegistry(t,`, `loadPersisted(`→`readBackJobs(t,`) with the two helpers' full code given; the `fakePruner` recorder addition in Task 4 Step 1 gives the exact methods.

**Type consistency:** `regStore.{OpenStore,UpsertJobs,AllJobs,ClaimTerminal,Close}` + `ownerInfo{Pid,StartedAt}` defined in Task 2 and used identically in Tasks 3/4. `NewRegistry(cfg,l,c,p) (*Registry,error)` consistent across app.go + all test callsites (via `mustRegistry`). `ReadRegistry(dbPath)` signature unchanged (still `(path string) ([]model.JobRecord, error)`) so the poller callsite is unaffected. `pidAlive` (liveness.go) + `isTerminal` (reconcile.go) reused. `config.RegistryPath()`/`config.ErrServerRestarted`/`config.DefaultConcurrencyCap` consistent.
```
