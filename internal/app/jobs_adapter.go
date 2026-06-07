package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"

	"pi-mcp/internal/config"
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/mcpserver"
	"pi-mcp/internal/model"
	"pi-mcp/internal/worktree"
)

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
		RunsDir: filepath.Join(in.CWD, config.RunsDirRel),
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
		spec.RunsDir = filepath.Join(h.Path, config.RunsDirRel)
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
