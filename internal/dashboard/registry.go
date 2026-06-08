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
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)&mode=ro")
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
