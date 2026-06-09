package jobs

import (
	"database/sql"
	"testing"

	"pi-mcp/internal/model"
)

func loadSnapshot(t *testing.T, s *regStore, jobID string) []byte {
	t.Helper()
	var b []byte
	if err := s.db.QueryRow(`SELECT runSnapshot FROM jobs WHERE jobId=?`, jobID).Scan(&b); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return b
}

func TestSaveSnapshot_RoundTripAndOwnerGuard(t *testing.T) {
	s, err := OpenStore(tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertJobs([]model.JobRecord{rec("j1", "completed")}, 100, "t0"); err != nil {
		t.Fatal(err)
	}

	snap := []byte(`{"runId":"r1","status":"completed"}`)
	if err := s.SaveSnapshot("j1", snap, 100); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if got := loadSnapshot(t, s, "j1"); string(got) != string(snap) {
		t.Fatalf("snapshot round-trip: got %q want %q", got, snap)
	}

	// Owner guard: a different owner must NOT overwrite a row it does not own.
	if err := s.SaveSnapshot("j1", []byte("hijacked"), 999); err != nil {
		t.Fatalf("SaveSnapshot (wrong owner) err: %v", err)
	}
	if got := loadSnapshot(t, s, "j1"); string(got) != string(snap) {
		t.Fatalf("wrong-owner SaveSnapshot clobbered the snapshot: %q", got)
	}
}

// TestOpenStore_MigratesRunSnapshotColumn opens a DB created with the
// pre-runSnapshot schema and asserts OpenStore adds the column so SaveSnapshot works.
func TestOpenStore_MigratesRunSnapshotColumn(t *testing.T) {
	path := tmpDB(t)
	const oldDDL = `CREATE TABLE jobs (
	  jobId TEXT PRIMARY KEY, runId TEXT NOT NULL DEFAULT '', sessionId TEXT NOT NULL DEFAULT '',
	  mode TEXT NOT NULL, cwd TEXT NOT NULL, runsDir TEXT NOT NULL, worktreePath TEXT NOT NULL DEFAULT '',
	  branch TEXT NOT NULL DEFAULT '', pid INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL,
	  startedAt TEXT NOT NULL, errorCode TEXT NOT NULL DEFAULT '', errorMessage TEXT NOT NULL DEFAULT '',
	  ownerPid INTEGER NOT NULL DEFAULT 0, ownerStartedAt TEXT NOT NULL DEFAULT '', updatedAt TEXT NOT NULL);`
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO jobs (jobId,mode,cwd,runsDir,status,startedAt,updatedAt)
		VALUES ('old1','read','/c','/c/runs','completed','t','t')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := OpenStore(path) // must ALTER TABLE ADD COLUMN runSnapshot
	if err != nil {
		t.Fatalf("OpenStore on legacy schema: %v", err)
	}
	defer s.Close()
	if err := s.SaveSnapshot("old1", []byte(`{"ok":true}`), 0); err != nil {
		t.Fatalf("SaveSnapshot after migration: %v", err)
	}
	if got := loadSnapshot(t, s, "old1"); string(got) != `{"ok":true}` {
		t.Fatalf("post-migration snapshot: %q", got)
	}
}
