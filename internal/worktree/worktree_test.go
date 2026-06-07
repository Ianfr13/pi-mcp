package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi-mcp/internal/config"
)

func TestResolveBaseDir(t *testing.T) {
	tests := []struct {
		name    string
		xdg     string
		home    string
		wantSub string // suffix the result must end with
	}{
		{name: "xdg set", xdg: "/tmp/xdgstate", home: "/home/u", wantSub: filepath.Join("/tmp/xdgstate", "pi-mcp", "worktrees")},
		{name: "xdg empty falls to home", xdg: "", home: "/home/u", wantSub: filepath.Join("/home/u", ".local", "state", "pi-mcp", "worktrees")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)
			got, err := resolveBaseDir()
			if err != nil {
				t.Fatalf("resolveBaseDir() err = %v", err)
			}
			if got != tt.wantSub {
				t.Fatalf("resolveBaseDir() = %q, want %q", got, tt.wantSub)
			}
		})
	}
}

func TestResolveBaseDir_TempFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	got, err := resolveBaseDir()
	if err != nil {
		t.Fatalf("resolveBaseDir() err = %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("pi-mcp", "worktrees")) {
		t.Fatalf("temp fallback = %q, want suffix pi-mcp/worktrees", got)
	}
	if !strings.HasPrefix(got, os.TempDir()) {
		t.Fatalf("temp fallback = %q, want prefix %q", got, os.TempDir())
	}
}

func TestNew_NotAGitRepo(t *testing.T) {
	dir := t.TempDir() // plain dir, never `git init`ed
	_, err := New(ctx(), dir)
	if err == nil {
		t.Fatal("New() on non-repo: want error, got nil")
	}
	if err.Error() != config.ErrNotAGitRepo {
		t.Fatalf("New() err = %q, want %q", err.Error(), config.ErrNotAGitRepo)
	}
}

func TestNew_NestedNonRepoSubdir(t *testing.T) {
	// A dir that is NOT inside any repo (temp dir is outside the module repo).
	sub := filepath.Join(t.TempDir(), "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctx(), sub); err == nil || err.Error() != config.ErrNotAGitRepo {
		t.Fatalf("New() nested non-repo err = %v, want %q", err, config.ErrNotAGitRepo)
	}
}

func TestNew_ValidRepo(t *testing.T) {
	repo := newRepo(t)
	m, err := New(ctx(), repo)
	if err != nil {
		t.Fatalf("New() on valid repo err = %v", err)
	}
	if m == nil {
		t.Fatal("New() returned nil Manager")
	}
}

func TestCreate_BranchAndPathOutsideTree(t *testing.T) {
	repo := newRepo(t)
	base := t.TempDir()
	SetBaseDirForTest(t, base)

	m, err := New(ctx(), repo)
	if err != nil {
		t.Fatal(err)
	}
	h, err := m.Create(ctx(), "job-1234")
	if err != nil {
		t.Fatalf("Create() err = %v", err)
	}

	// Branch name follows config.WorktreeBranchPrefix.
	if h.Branch != "pi-mcp/job-job-1234" {
		t.Fatalf("Branch = %q, want pi-mcp/job-job-1234", h.Branch)
	}
	// Worktree path is under the (test) base dir, NOT under the repo cwd.
	if !strings.HasPrefix(h.Path, base) {
		t.Fatalf("Path %q not under base %q", h.Path, base)
	}
	if strings.HasPrefix(h.Path, repo) {
		t.Fatalf("Path %q must be OUTSIDE repo %q", h.Path, repo)
	}
	// The worktree dir exists and contains the committed file.
	if _, err := os.Stat(filepath.Join(h.Path, "README.md")); err != nil {
		t.Fatalf("worktree missing README.md: %v", err)
	}
	// git lists the new worktree.
	out := git(t, repo, "worktree", "list", "--porcelain")
	if !strings.Contains(out, h.Path) {
		t.Fatalf("git worktree list missing %q:\n%s", h.Path, out)
	}
	// BaseCommit equals repo HEAD.
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	if h.BaseCommit != head {
		t.Fatalf("BaseCommit = %q, want HEAD %q", h.BaseCommit, head)
	}
}

func TestCreate_Collision(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	if _, err := m.Create(ctx(), "dup"); err != nil {
		t.Fatalf("first Create err = %v", err)
	}
	if _, err := m.Create(ctx(), "dup"); err == nil {
		t.Fatal("second Create with same jobID: want collision error, got nil")
	}
}

func TestCreate_DirtyHeadWarnsProceeds(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())

	// Leave an uncommitted + untracked change in the repo HEAD.
	writeFile(t, repo, "README.md", "hello dirty\n") // modify tracked
	writeFile(t, repo, "scratch.txt", "untracked\n") // add untracked

	m, _ := New(ctx(), repo)
	h, err := m.Create(ctx(), "dirty1")
	if err != nil {
		t.Fatalf("Create() on dirty HEAD must proceed, got err = %v", err)
	}
	if h.DirtyWarning == "" {
		t.Fatal("DirtyWarning empty; want a non-empty warn on dirty HEAD")
	}
	// The worktree reflects committed HEAD only: README is the committed "hello",
	// and the untracked file is absent.
	got, err := os.ReadFile(filepath.Join(h.Path, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("worktree README = %q, want committed %q", got, "hello\n")
	}
	if _, err := os.Stat(filepath.Join(h.Path, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file leaked into worktree (err=%v)", err)
	}
}

func TestCreate_CleanHeadNoWarn(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, err := m.Create(ctx(), "clean1")
	if err != nil {
		t.Fatal(err)
	}
	if h.DirtyWarning != "" {
		t.Fatalf("clean HEAD DirtyWarning = %q, want empty", h.DirtyWarning)
	}
}

func TestDiff_CapturesWorktreeEdits(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, err := m.Create(ctx(), "diff1")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate write-mode agents editing the shared job worktree: one modified,
	// one new file. Do NOT commit — Diff must see uncommitted worktree changes.
	writeFile(t, h.Path, "README.md", "hello\nmore\n")
	writeFile(t, h.Path, "src/new.go", "package src\n")
	git(t, h.Path, "add", "-A") // stage so untracked new file shows in name-only

	diffStat, files, err := m.Diff(ctx(), h)
	if err != nil {
		t.Fatalf("Diff() err = %v", err)
	}
	if !strings.Contains(diffStat, "README.md") {
		t.Fatalf("diffStat missing README.md:\n%s", diffStat)
	}
	wantFiles := map[string]bool{"README.md": true, "src/new.go": true}
	for _, f := range files {
		delete(wantFiles, f)
	}
	if len(wantFiles) != 0 {
		t.Fatalf("files_changed missing %v; got %v", wantFiles, files)
	}
}

func TestDiff_EmptyWhenNoChanges(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, _ := m.Create(ctx(), "empty1")

	diffStat, files, err := m.Diff(ctx(), h)
	if err != nil {
		t.Fatalf("Diff() err = %v", err)
	}
	if strings.TrimSpace(diffStat) != "" {
		t.Fatalf("empty diff stat = %q, want empty", diffStat)
	}
	if len(files) != 0 {
		t.Fatalf("empty files_changed = %v, want none", files)
	}
}

func TestWriteInfo_FillsContract(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, _ := m.Create(ctx(), "wi1")

	writeFile(t, h.Path, "README.md", "hello\nedit\n")
	git(t, h.Path, "add", "-A")

	wi, err := m.WriteInfo(ctx(), h)
	if err != nil {
		t.Fatalf("WriteInfo() err = %v", err)
	}
	if wi.Branch != h.Branch {
		t.Fatalf("Branch = %q, want %q", wi.Branch, h.Branch)
	}
	if wi.WorktreePath != h.Path {
		t.Fatalf("WorktreePath = %q, want %q", wi.WorktreePath, h.Path)
	}
	if !strings.Contains(wi.DiffStat, "README.md") {
		t.Fatalf("DiffStat missing README.md:\n%s", wi.DiffStat)
	}
	found := false
	for _, f := range wi.FilesChanged {
		if f == "README.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("FilesChanged missing README.md: %v", wi.FilesChanged)
	}
}

func TestPrune_RemovesWorktreeAndBranch(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, _ := m.Create(ctx(), "prune1")

	// Add uncommitted changes — Prune must force-remove despite a dirty worktree.
	writeFile(t, h.Path, "README.md", "dirty before prune\n")

	if err := m.Prune(ctx(), h); err != nil {
		t.Fatalf("Prune() err = %v", err)
	}
	// Worktree dir gone.
	if _, err := os.Stat(h.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present (err=%v)", err)
	}
	// Branch gone.
	if exists, _ := m.branchExists(ctx(), h.Branch); exists {
		t.Fatalf("branch %s still present after Prune", h.Branch)
	}
	// git worktree list no longer references it.
	out := git(t, repo, "worktree", "list", "--porcelain")
	if strings.Contains(out, h.Path) {
		t.Fatalf("git worktree list still has %q:\n%s", h.Path, out)
	}
}

func TestPrune_Idempotent(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, _ := m.Create(ctx(), "prune2")

	if err := m.Prune(ctx(), h); err != nil {
		t.Fatalf("first Prune err = %v", err)
	}
	// Second prune on an already-removed handle must be a no-op (no error).
	if err := m.Prune(ctx(), h); err != nil {
		t.Fatalf("second Prune (idempotent) err = %v", err)
	}
}

func TestPruneOrphans_RemovesAllJobWorktrees(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)

	h1, _ := m.Create(ctx(), "orph-a")
	h2, _ := m.Create(ctx(), "orph-b")
	// A non-pi-mcp worktree + branch that MUST be preserved.
	other := filepath.Join(t.TempDir(), "other-wt")
	git(t, repo, "worktree", "add", "-b", "feature/keep", other, "HEAD")

	removed, err := m.PruneOrphans(ctx())
	if err != nil {
		t.Fatalf("PruneOrphans() err = %v", err)
	}
	// Both pi-mcp branches reported as removed.
	gotRemoved := map[string]bool{}
	for _, b := range removed {
		gotRemoved[b] = true
	}
	if !gotRemoved[h1.Branch] || !gotRemoved[h2.Branch] {
		t.Fatalf("removed = %v, want both %s and %s", removed, h1.Branch, h2.Branch)
	}
	// pi-mcp worktree dirs gone.
	for _, h := range []*Handle{h1, h2} {
		if _, err := os.Stat(h.Path); !os.IsNotExist(err) {
			t.Fatalf("orphan worktree %s still present", h.Path)
		}
		if exists, _ := m.branchExists(ctx(), h.Branch); exists {
			t.Fatalf("orphan branch %s still present", h.Branch)
		}
	}
	// The unrelated worktree + branch survived.
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("non-pi-mcp worktree wrongly removed: %v", err)
	}
	if exists, _ := m.branchExists(ctx(), "feature/keep"); !exists {
		t.Fatal("non-pi-mcp branch feature/keep wrongly removed")
	}
}

func TestPruneOrphans_NoneIsNoError(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	removed, err := m.PruneOrphans(ctx())
	if err != nil {
		t.Fatalf("PruneOrphans() with no orphans err = %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want none", removed)
	}
}

func TestCreate_CanceledContext(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)

	c, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled: every git invocation must fail fast.

	if _, err := m.Create(c, "cancel1"); err == nil {
		t.Fatal("Create() with canceled ctx: want error, got nil")
	}
	// No worktree leaked under the base dir.
	base, _ := baseDirFn()
	if _, err := os.Stat(filepath.Join(base, "job-cancel1")); err == nil {
		t.Fatal("worktree dir created despite canceled context")
	}
}

// TestPruneFunc_RemovesWorktreeAndBranch exercises the free function Prune,
// which the jobs/app pruner seam uses (no Manager, only path+branch). It resolves
// the owning repo from the worktree path and is idempotent.
func TestPruneFunc_RemovesWorktreeAndBranch(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, _ := m.Create(ctx(), "freeprune1")

	// Dirty the worktree to ensure force-remove.
	writeFile(t, h.Path, "README.md", "dirty\n")

	if err := Prune(ctx(), h.Path, h.Branch); err != nil {
		t.Fatalf("Prune() free func err = %v", err)
	}
	if _, err := os.Stat(h.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present after free Prune (err=%v)", err)
	}
	if exists, _ := m.branchExists(ctx(), h.Branch); exists {
		t.Fatalf("branch %s still present after free Prune", h.Branch)
	}
	out := git(t, repo, "worktree", "list", "--porcelain")
	if strings.Contains(out, h.Path) {
		t.Fatalf("git worktree list still has %q:\n%s", h.Path, out)
	}
}

func TestPruneFunc_Idempotent(t *testing.T) {
	repo := newRepo(t)
	SetBaseDirForTest(t, t.TempDir())
	m, _ := New(ctx(), repo)
	h, _ := m.Create(ctx(), "freeprune2")

	if err := Prune(ctx(), h.Path, h.Branch); err != nil {
		t.Fatalf("first free Prune err = %v", err)
	}
	// Second call: worktree path is gone, so resolution falls back gracefully (no error).
	if err := Prune(ctx(), h.Path, h.Branch); err != nil {
		t.Fatalf("second free Prune (idempotent) err = %v", err)
	}
}
