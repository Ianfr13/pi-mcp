package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/mcpserver"
	"pi-mcp/internal/model"
)

func TestWriteInfoFor_NonEmptyDiff(t *testing.T) {
	a, _ := newAdapter(t)
	repo := gitInitRepo(t)
	rec, err := a.Submit(context.Background(), mcpserver.JobSpec{
		Task: "edit", Mode: model.ModeWrite, CWD: repo,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Simulate a write-mode agent editing the worktree + a new file.
	if err := os.WriteFile(filepath.Join(rec.WorktreePath, "seed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec.WorktreePath, "NEW.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wi, ok := a.WriteInfoFor(rec.JobID)
	if !ok {
		t.Fatal("WriteInfoFor must return ok=true for a write job")
	}
	if wi.DiffStat == "" {
		t.Fatal("diff_stat must be non-empty after edits")
	}
	if len(wi.FilesChanged) == 0 {
		t.Fatal("files_changed must be non-empty after edits")
	}
	waitTerminal(t, a, rec.JobID)
}

func TestWriteInfoFor_ReadJobIsFalse(t *testing.T) {
	a, _ := newAdapter(t)
	rec, _ := a.Submit(context.Background(), mcpserver.JobSpec{
		Task: "scan", Mode: model.ModeRead, CWD: t.TempDir(),
	})
	if _, ok := a.WriteInfoFor(rec.JobID); ok {
		t.Fatal("read job must have WriteInfoFor ok=false")
	}
	waitTerminal(t, a, rec.JobID)
}

func TestWriteInfoFor_UnknownJobIsFalse(t *testing.T) {
	a, _ := newAdapter(t)
	if _, ok := a.WriteInfoFor("no-such-job"); ok {
		t.Fatal("unknown job must have WriteInfoFor ok=false")
	}
}
