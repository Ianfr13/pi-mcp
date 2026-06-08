package dashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeActive_RecentFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !WorktreeActive(dir, time.Now()) {
		t.Errorf("freshly written worktree should be active")
	}
}

func TestWorktreeActive_SkipsGitAndPi(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{".git", ".pi"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Only bookkeeping dirs touched now -> not "active" relative to a now far in
	// the future (the real worktree files are old / absent).
	future := time.Now().Add(48 * time.Hour)
	if WorktreeActive(dir, future) {
		t.Errorf(".git/.pi churn must not count as activity")
	}
}

func TestWorktreeActive_MissingDir(t *testing.T) {
	if WorktreeActive("", time.Now()) {
		t.Errorf("empty path -> not active")
	}
	if WorktreeActive(filepath.Join(t.TempDir(), "nope"), time.Now()) {
		t.Errorf("missing dir -> not active")
	}
}
