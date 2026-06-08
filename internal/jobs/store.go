package jobs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"pi-mcp/internal/config"
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
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
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
  updatedAt=excluded.updatedAt
WHERE jobs.ownerPid=excluded.ownerPid OR jobs.ownerPid=0;`

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
		// Corrupt legacy file: quarantine it so we don't retry-and-fail every boot,
		// then continue with an empty registry.
		_ = os.Rename(legacy, legacy+".corrupt")
		return fmt.Errorf("decode legacy (quarantined to %s.corrupt): %w", legacy, err)
	}
	// Terminalize any non-terminal legacy job before import: they belong to the
	// pre-SQLite world and cannot be resumed here. Marking them terminal means
	// reconcile will NOT prune their worktrees — which, during a rollout, may still
	// belong to a live old-binary server. Migration must never destroy live work.
	for i := range pf.Jobs {
		switch pf.Jobs[i].Status {
		case model.JobCompleted, model.JobFailed, model.JobAborted:
			// already terminal — keep as-is
		default:
			pf.Jobs[i].Status = model.JobFailed
			if pf.Jobs[i].ErrorCode == "" {
				pf.Jobs[i].ErrorCode = config.ErrServerRestarted
			}
		}
	}
	if len(pf.Jobs) > 0 {
		if err := s.UpsertJobs(pf.Jobs, 0, ""); err != nil { // ownerPid 0 == dead owner
			return fmt.Errorf("import legacy: %w", err)
		}
	}
	if err := os.Rename(legacy, legacy+".migrated"); err != nil {
		if os.IsNotExist(err) {
			return nil // a concurrent migrator already moved it — not an error
		}
		return err
	}
	return nil
}
