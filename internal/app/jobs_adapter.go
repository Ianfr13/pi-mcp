package app

import (
	"context"
	"encoding/json"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"pi-mcp/internal/jobs"
	"pi-mcp/internal/mcpserver"
	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
	"pi-mcp/internal/worktree"
)

// maxSnapshotBytes caps the run-file snapshot persisted into the registry at
// terminal time. Run files are normally a few KB; an enormous one (huge fleet
// with large journal results) is skipped rather than bloating the registry DB —
// the job simply has no post-cleanup detail, same as before this feature.
const maxSnapshotBytes = 4 << 20 // 4 MiB

// snapshotRunFile reads the final run file (runsDir,runID) and returns its
// canonical JSON for persistence into the registry (jobs.Config.SnapshotRun).
// nil when there is no readable run file or it exceeds maxSnapshotBytes. Uses
// runstore.ReadRun so the sibling .bak snapshot is used if the primary is gone.
func snapshotRunFile(runsDir, runID string) []byte {
	run, err := runstore.ReadRun(filepath.Join(runsDir, runID+".json"))
	if err != nil {
		return nil
	}
	b, err := json.Marshal(run)
	if err != nil || len(b) > maxSnapshotBytes {
		return nil
	}
	return b
}

// newJobID mints a fresh job id (== the registry's id scheme) so the worktree
// branch created BEFORE submit (pi-mcp/job-<id>) matches the registry record.
func newJobID() string { return jobs.NewID() }

// worktreePruner implements jobs.Pruner using the free worktree.Prune function,
// which resolves the owning repo from the worktree path (no live Manager needed).
type worktreePruner struct{}

func (worktreePruner) Prune(worktreePath, branch string) error {
	return worktree.Prune(context.Background(), worktreePath, branch)
}

// writeHandle remembers the worktree Manager + Handle for a write job so
// WriteInfoFor can stage and diff the worktree after the agent runs.
type writeHandle struct {
	mgr *worktree.Manager
	h   *worktree.Handle
}

// jobsAdapter implements mcpserver.JobsService over a *jobs.Registry, adding
// create-on-submit worktree orchestration for write mode.
type jobsAdapter struct {
	reg *jobs.Registry

	mu      sync.Mutex
	handles map[string]writeHandle
}

func (a *jobsAdapter) remember(id string, mgr *worktree.Manager, h *worktree.Handle) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.handles == nil {
		a.handles = make(map[string]writeHandle)
	}
	a.handles[id] = writeHandle{mgr: mgr, h: h}
}

func (a *jobsAdapter) handle(id string) (writeHandle, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	wh, ok := a.handles[id]
	return wh, ok
}

// Submit does create-on-submit worktree orchestration for write mode, then builds
// the jobs.Spec and delegates to the registry.
func (a *jobsAdapter) Submit(ctx context.Context, in mcpserver.JobSpec) (model.JobRecord, error) {
	spec := jobs.Spec{
		Mode:    in.Mode,
		CWD:     in.CWD,
		Task:    in.Task,
		Context: in.Context,
		RunsDir: runstore.RunsDir(in.CWD),
	}
	if in.Mode == model.ModeWrite {
		mgr, err := worktree.New(ctx, in.CWD) // fail-before-spawn: NOT_A_GIT_REPO
		if err != nil {
			return model.JobRecord{}, err
		}
		// The branch name needs the job id up front (pi-mcp/job-<id>); mint it here
		// and hand it to both worktree.Create and the registry via PreassignedID.
		id := newJobID()
		h, err := mgr.Create(ctx, id)
		if err != nil {
			return model.JobRecord{}, err
		}
		spec.PreassignedID = id
		spec.Worktree = h.Path
		spec.Branch = h.Branch
		spec.CWD = h.Path
		spec.RunsDir = runstore.RunsDir(h.Path)
		a.remember(id, mgr, h)
	}
	return a.reg.Submit(ctx, spec)
}

func (a *jobsAdapter) Lookup(jobID string) (model.JobRecord, bool) { return a.reg.Lookup(jobID) }

func (a *jobsAdapter) LookupByRun(runID, cwd string) (model.JobRecord, bool) {
	return a.reg.LookupByRun(runID, cwd)
}

func (a *jobsAdapter) Cancel(jobID string) (model.JobRecord, error) { return a.reg.Cancel(jobID) }

// WriteInfoFor returns the write-mode delivery block (branch/worktree/diff/files)
// for a job. It stages the worktree (git add -A, so untracked agent-created files
// are captured) and diffs against the base commit. ok=false for unknown or
// non-write jobs.
func (a *jobsAdapter) WriteInfoFor(jobID string) (model.WriteInfo, bool) {
	wh, ok := a.handle(jobID)
	if !ok {
		return model.WriteInfo{}, false
	}
	ctx := context.Background()
	// Stage everything so untracked files made by the write-mode agent are
	// captured by `git diff --stat <baseCommit>`.
	add := exec.CommandContext(ctx, "git", "add", "-A")
	add.Dir = wh.h.Path
	_ = add.Run() // best-effort; an empty worktree just yields an empty diff
	wi, err := wh.mgr.WriteInfo(ctx, wh.h)
	if err != nil {
		return model.WriteInfo{}, false
	}
	return wi, true
}

// WorktreeActivity walks the write job's worktree (NON-mutating — no git add -A,
// unlike WriteInfoFor) and reports how many files it contains (the HEAD checkout
// plus the agent's additions) and the most recent modification time. This is the
// liveness/progress signal for a
// write job that edits files directly (its run file goes stale while it works).
// It resolves the worktree path from the registry record, so it works for jobs
// recovered after a restart too (no in-memory handle required). ok=false for
// unknown or non-write jobs, or when the worktree has no files yet.
func (a *jobsAdapter) WorktreeActivity(jobID string) (int, time.Time, bool) {
	rec, ok := a.reg.Lookup(jobID)
	if !ok || rec.Mode != model.ModeWrite || rec.WorktreePath == "" {
		return 0, time.Time{}, false
	}
	files, newest := scanWorktreeActivity(rec.WorktreePath)
	if files == 0 {
		return 0, time.Time{}, false
	}
	return files, newest, true
}

// scanWorktreeActivity counts regular files under root and returns the newest
// mtime, skipping the .git and .pi bookkeeping entries (which churn independently
// of the agent's work). NOTE: pi-mcp creates a LINKED git worktree, where .git is
// a FILE (a gitdir pointer), not a dir — so the skip matches both. The count is
// "files present in the checkout + the agent's additions", not agent-only. The
// walk is NOT bounded by a file cap: the newest mtime is the liveness signal, so
// it must observe every file (a cap could miss the freshest write and false-fail a
// live job). It is a stat-only read; cost is O(files) but worktrees are project-
// sized. Errors are swallowed: a partial walk still yields a useful signal, and a
// missing worktree simply yields (0, zero time). Symlinks/non-regular entries are
// not counted (their lstat mtime is not the agent's work).
func scanWorktreeActivity(root string) (files int, newest time.Time) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; keep walking
		}
		// Skip pi/git bookkeeping whether it is a dir or a linked-worktree .git FILE.
		if name := d.Name(); path != root && (name == ".git" || name == ".pi") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // dirs, symlinks, devices, sockets: not counted
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		files++
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return files, newest
}
