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
