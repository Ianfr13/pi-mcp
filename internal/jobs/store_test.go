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
	// non-destructive migration: a legacy non-terminal ("running") job is imported
	// as terminal (failed) so reconcile never prunes its (possibly live) worktree.
	if string(recs[0].Status) != "failed" {
		t.Errorf("legacy running job should import as failed, got %q", recs[0].Status)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy registry.json should be renamed away, stat err=%v", err)
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Errorf("expected registry.json.migrated, err=%v", err)
	}
}

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
