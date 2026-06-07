package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-mcp/internal/jobs"
	"pi-mcp/internal/mcpserver"
	"pi-mcp/internal/model"
)

// waitTerminal blocks until the job reaches a terminal status. With the noop
// launcher a submitted job finishes near-instantly via a background goroutine
// whose final registry flush would otherwise race t.TempDir() cleanup. Polling
// to a terminal state (the jobs package's own synchronization pattern) ensures
// that flush has landed before the test returns.
func waitTerminal(t *testing.T, a *jobsAdapter, jobID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec, ok := a.Lookup(jobID)
		if ok {
			switch rec.Status {
			case model.JobCompleted, model.JobFailed, model.JobAborted:
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached a terminal status", jobID)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// noopLauncher returns a started "process" that never produces a session and
// completes immediately. Used so Submit does not spawn the real pi.
type noopLauncher struct{}

func (noopLauncher) Launch(ctx context.Context, s jobs.Spec) (int, <-chan string, func() error, error) {
	ch := make(chan string)
	close(ch)
	return 1, ch, func() error { return nil }, nil
}

type noopCorrelator struct{}

func (noopCorrelator) RunIDForSession(string, string) (string, bool) { return "", false }

// setWorktreeBase isolates worktree creation under base for the duration of the
// test by pointing the XDG state dir at it (resolveBaseDir honors XDG_STATE_HOME).
func setWorktreeBase(t *testing.T, base string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", base)
}

// newAdapter builds a jobsAdapter over a real registry with noop launcher/correlator
// and an isolated worktree base. It returns the adapter and the worktree base dir.
func newAdapter(t *testing.T) (*jobsAdapter, string) {
	t.Helper()
	base := t.TempDir()
	setWorktreeBase(t, base)
	reg := jobs.NewRegistry(
		jobs.Config{Cap: 4, PersistPath: filepath.Join(t.TempDir(), "r.json")},
		noopLauncher{}, noopCorrelator{}, worktreePruner{},
	)
	t.Cleanup(func() { _ = reg.Close() })
	a := &jobsAdapter{reg: reg}
	return a, base
}

// gitInitRepo creates a throwaway git repo with one commit and returns its path.
func gitInitRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "seed")
	return dir
}

func TestSubmit_ReadNoWorktree(t *testing.T) {
	a, _ := newAdapter(t)
	rec, err := a.Submit(context.Background(), mcpserver.JobSpec{
		Task: "scan", Mode: model.ModeRead, CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Submit read: %v", err)
	}
	if rec.WorktreePath != "" {
		t.Fatalf("read mode must have no worktree, got %q", rec.WorktreePath)
	}
	waitTerminal(t, a, rec.JobID)
}

func TestSubmit_WriteCreatesIsolatedWorktree(t *testing.T) {
	a, base := newAdapter(t)
	repo := gitInitRepo(t)

	rec, err := a.Submit(context.Background(), mcpserver.JobSpec{
		Task: "edit", Mode: model.ModeWrite, CWD: repo,
	})
	if err != nil {
		t.Fatalf("Submit write: %v", err)
	}
	if rec.WorktreePath == "" {
		t.Fatal("write mode must have a worktree path")
	}
	// The worktree must live under the isolated base, NOT inside the user's repo.
	if !strings.HasPrefix(rec.WorktreePath, base) {
		t.Fatalf("worktree %q not under isolated base %q", rec.WorktreePath, base)
	}
	if strings.HasPrefix(rec.WorktreePath, repo) {
		t.Fatalf("worktree %q must NOT be inside the user repo %q", rec.WorktreePath, repo)
	}
	// The branch must be pi-mcp/job-<jobId>.
	if rec.Branch != "pi-mcp/job-"+rec.JobID {
		t.Fatalf("branch = %q, want pi-mcp/job-%s", rec.Branch, rec.JobID)
	}

	// Isolation: the user cwd must NOT be mutated — only seed.txt and .git remain.
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if n != "seed.txt" && n != ".git" {
			t.Fatalf("user repo cwd was mutated: unexpected entry %q", n)
		}
	}
	waitTerminal(t, a, rec.JobID)
}

func TestSubmit_WriteOnNonGitRepoReturnsErrNotAGitRepo(t *testing.T) {
	a, _ := newAdapter(t)
	notGit := t.TempDir() // a plain dir, not a git repo

	_, err := a.Submit(context.Background(), mcpserver.JobSpec{
		Task: "edit", Mode: model.ModeWrite, CWD: notGit,
	})
	if err == nil {
		t.Fatal("expected NOT_A_GIT_REPO error, got nil")
	}
	if !strings.Contains(err.Error(), "NOT_A_GIT_REPO") {
		t.Fatalf("error = %q, want it to contain NOT_A_GIT_REPO", err.Error())
	}
}
