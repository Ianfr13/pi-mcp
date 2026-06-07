package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/config"
	"pi-mcp/internal/mcpserver"
)

func TestRunStoreAdapter_LoadFound(t *testing.T) {
	runs := t.TempDir()
	b, err := os.ReadFile(filepath.Join("..", "runstore", "testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runs, "mq40rdpt-yij9hj.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := runStoreAdapter{}.Load(runs, "mq40rdpt-yij9hj")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r == nil || r.RunID != "mq40rdpt-yij9hj" {
		t.Fatalf("Load returned wrong run: %+v", r)
	}
}

func TestRunStoreAdapter_LoadMissingMapsToMcpserverErrRunNotFound(t *testing.T) {
	runs := t.TempDir()
	_, err := runStoreAdapter{}.Load(runs, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing run, got nil")
	}
	if !errors.Is(err, mcpserver.ErrRunNotFound) {
		t.Fatalf("error %v should map to mcpserver.ErrRunNotFound", err)
	}
}

func TestRunStoreAdapter_ListItems(t *testing.T) {
	cwd := t.TempDir()
	runs := filepath.Join(cwd, config.RunsDirRel)
	if err := os.MkdirAll(runs, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "runstore", "testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runs, "mq40rdpt-yij9hj.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := runStoreAdapter{}.ListItems(cwd, 10)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("ListItems returned %d items, want 1", len(items))
	}
}
