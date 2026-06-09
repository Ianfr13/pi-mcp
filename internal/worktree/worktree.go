// Package worktree manages write-mode git worktrees: it creates an isolated
// worktree OUTSIDE the user's working tree on a fresh pi-mcp/job-<id> branch,
// computes the delivered diff, and prunes worktrees+branches on cancel/crash.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// baseDirFn resolves the directory that holds all pi-mcp worktrees. It is a
// package var ONLY so tests can redirect worktrees into a temp dir via
// SetBaseDirForTest. Production code never reassigns it.
var baseDirFn = resolveBaseDir

// resolveBaseDir implements the XDG state resolution:
// $XDG_STATE_HOME/pi-mcp/worktrees -> $HOME/.local/state/pi-mcp/worktrees -> $TMPDIR/pi-mcp/worktrees.
func resolveBaseDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, config.WorktreeSubdir), nil
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", config.WorktreeSubdir), nil
	}
	return filepath.Join(os.TempDir(), config.WorktreeSubdir), nil
}

// Manager creates/diffs/prunes write-mode worktrees for a single repo cwd.
type Manager struct {
	repoCWD string
}

// New verifies cwd is inside a git work tree. If not, it returns an error whose
// message is exactly config.ErrNotAGitRepo (the catalog code, "NOT_A_GIT_REPO"),
// so callers fail fast before spawning pi.
func New(ctx context.Context, cwd string) (*Manager, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, errors.New(config.ErrNotAGitRepo)
	}
	return &Manager{repoCWD: cwd}, nil
}

// Handle identifies one created write-mode worktree.
type Handle struct {
	JobID        string
	Path         string // absolute worktree path, OUTSIDE repoCWD
	Branch       string // pi-mcp/job-<jobId>
	BaseCommit   string // HEAD sha the branch was cut from
	DirtyWarning string // non-empty when HEAD had uncommitted/untracked changes
}

// branchFor builds the fresh branch name for a job.
func branchFor(jobID string) string { return config.WorktreeBranchPrefix + jobID }

// Create cuts branch pi-mcp/job-<jobID> from HEAD and adds a worktree at
// <base>/job-<jobID>, OUTSIDE the user's working tree. A pre-existing branch or
// target dir is a collision (error). HEAD dirtiness is recorded as a warning
// (warn+proceed) on the returned Handle; it never fails Create.
func (m *Manager) Create(ctx context.Context, jobID string) (*Handle, error) {
	base, err := baseDirFn()
	if err != nil {
		return nil, fmt.Errorf("resolve worktree base: %w", err)
	}

	// Resolve HEAD first: this is the earliest git call, so a canceled context
	// (pi_cancel) aborts BEFORE any side effects (dir creation / worktree add).
	head, err := m.runGit(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	head = strings.TrimSpace(head)

	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir worktree base: %w", err)
	}
	path := filepath.Join(base, "job-"+jobID)
	branch := branchFor(jobID)

	// Collision: target dir already present.
	if _, statErr := os.Stat(path); statErr == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", path)
	}
	// Collision: branch already present.
	if exists, berr := m.branchExists(ctx, branch); berr != nil {
		return nil, berr
	} else if exists {
		return nil, fmt.Errorf("branch already exists: %s", branch)
	}

	warn := m.dirtyWarning(ctx)

	// `git worktree add -b <branch> <path> HEAD` creates the branch from HEAD
	// and checks it out into the new worktree in one shot.
	if _, err := m.runGit(ctx, "worktree", "add", "-b", branch, path, "HEAD"); err != nil {
		return nil, fmt.Errorf("worktree add: %w", err)
	}

	return &Handle{
		JobID:        jobID,
		Path:         path,
		Branch:       branch,
		BaseCommit:   head,
		DirtyWarning: warn,
	}, nil
}

// branchExists reports whether refs/heads/<branch> is present.
func (m *Manager) branchExists(ctx context.Context, branch string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", m.repoCWD, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil // exit 1 = ref absent
	}
	return false, fmt.Errorf("show-ref %s: %w", branch, err)
}

// dirtyWarning returns a non-empty warning if HEAD has staged/unstaged/untracked
// changes (warn+proceed: those changes are NOT carried into the worktree).
func (m *Manager) dirtyWarning(ctx context.Context) string {
	out, err := m.runGit(ctx, "status", "--porcelain")
	if err != nil {
		return ""
	}
	if strings.TrimSpace(out) != "" {
		return "HEAD has uncommitted/untracked changes; worktree branched from committed HEAD only"
	}
	return ""
}

// Diff returns the git --stat output and the list of changed files for the
// worktree, comparing the worktree's index+working tree against BaseCommit so
// that uncommitted edits made by write-mode agents are captured. An empty diff
// yields ("", nil, nil), not an error.
func (m *Manager) Diff(ctx context.Context, h *Handle) (string, []string, error) {
	// --stat against BaseCommit, including staged + unstaged changes.
	statOut, err := runGitIn(ctx, h.Path, "diff", "--stat", h.BaseCommit)
	if err != nil {
		return "", nil, fmt.Errorf("diff --stat: %w", err)
	}
	// NUL-delimited name list (safe for paths with spaces/newlines).
	nameOut, err := runGitIn(ctx, h.Path, "diff", "--name-only", "-z", h.BaseCommit)
	if err != nil {
		return "", nil, fmt.Errorf("diff --name-only: %w", err)
	}
	var files []string
	for _, f := range strings.Split(nameOut, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return strings.TrimRight(statOut, "\n"), files, nil
}

// WriteInfo assembles the model.WriteInfo returned by pi_status for write jobs:
// branch, worktree path, diff --stat, and the changed-file list.
func (m *Manager) WriteInfo(ctx context.Context, h *Handle) (model.WriteInfo, error) {
	diffStat, files, err := m.Diff(ctx, h)
	if err != nil {
		return model.WriteInfo{}, err
	}
	return model.WriteInfo{
		Branch:       h.Branch,
		WorktreePath: h.Path,
		DiffStat:     diffStat,
		FilesChanged: files,
	}, nil
}

// Prune removes a single worktree+branch given only its path+branch (no Manager
// needed). It resolves the owning repo via `git -C <worktreePath> rev-parse
// --git-common-dir`, then runs worktree remove --force / branch -D / worktree
// prune from there. Idempotent: a missing worktree (path already gone) or branch
// is not an error, so the jobs pruner seam can call it on cancel/crash safely.
func Prune(ctx context.Context, worktreePath, branch string) error {
	repo := resolveCommonRepo(ctx, worktreePath)
	if repo == "" {
		// The worktree path is gone (already pruned) and we cannot resolve the
		// owning repo. Best-effort cleanup of any leftover dir, then no-op: there
		// is no repo to remove the branch from. Idempotent.
		if _, err := os.Stat(worktreePath); err == nil {
			_ = os.RemoveAll(worktreePath)
		}
		return nil
	}
	// Force-remove the worktree (ignore "is not a working tree" / not found).
	_, _ = runGitIn(ctx, repo, "worktree", "remove", "--force", worktreePath)
	if _, err := os.Stat(worktreePath); err == nil {
		_ = os.RemoveAll(worktreePath)
	}
	// Delete the branch (ignore "branch not found").
	_, _ = runGitIn(ctx, repo, "branch", "-D", branch)
	if _, err := runGitIn(ctx, repo, "worktree", "prune"); err != nil {
		return fmt.Errorf("worktree prune: %w", err)
	}
	return nil
}

// resolveCommonRepo returns a directory inside the main repo for a given worktree
// path by reading `git -C <worktreePath> rev-parse --git-common-dir` (the shared
// .git dir; its parent is the main work tree). Returns "" if it cannot resolve
// (e.g. the worktree path no longer exists).
func resolveCommonRepo(ctx context.Context, worktreePath string) string {
	out, err := runGitIn(ctx, worktreePath, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	commonDir := strings.TrimSpace(out)
	if commonDir == "" {
		return ""
	}
	// --git-common-dir may be relative to the worktree path.
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}
	// The main work tree is the parent of the common .git dir.
	return filepath.Dir(commonDir)
}

// runGit runs a git subcommand in repoCWD and returns combined output.
func (m *Manager) runGit(ctx context.Context, args ...string) (string, error) {
	return runGitIn(ctx, m.repoCWD, args...)
}

// runGitIn runs git in an arbitrary dir (used for worktree-local commands).
func runGitIn(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %v: %w (%s)", args, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
