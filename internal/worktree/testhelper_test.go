package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// SetBaseDirForTest redirects all worktree creation into dir for the duration
// of the test. Restored automatically via t.Cleanup.
func SetBaseDirForTest(t *testing.T, dir string) {
	t.Helper()
	prev := baseDirFn
	baseDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { baseDirFn = prev })
}

// git runs a git command in dir and fails the test on error, returning stdout.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// newRepo creates a throwaway git repo under t.TempDir() with one commit and a
// deterministic identity (works in CI with no global git config). Returns cwd.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "test@pi-mcp.local")
	git(t, dir, "config", "user.name", "pi-mcp test")
	git(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "README.md", "hello\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// writeFile writes name=content under dir (mkdir -p of parents).
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ctx is a short helper for a background context in tests.
func ctx() context.Context { return context.Background() }
