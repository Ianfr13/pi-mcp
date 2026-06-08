package mcpserver

import (
	"context"
	"errors"
	"time"

	"pi-mcp/internal/model"
)

// JobSpec is the validated input handed to the jobs layer by pi_workflow.
// The JobSpec -> jobs.Spec mapping (worktree/runsDir orchestration) is done by the
// app adapter, NOT here; mcpserver only validates and forwards Task/Mode/CWD/Context.
type JobSpec struct {
	Task    string
	Mode    model.JobMode
	CWD     string // validated: absolute, exists, symlinks resolved
	Context string
}

// JobsService is the subset of the jobs registry the handlers need.
// The concrete jobs.Registry must satisfy this (adapted in the app layer if names differ).
type JobsService interface {
	Submit(ctx context.Context, spec JobSpec) (model.JobRecord, error)
	Lookup(jobID string) (model.JobRecord, bool)
	LookupByRun(runID, cwd string) (model.JobRecord, bool)
	Cancel(jobID string) (model.JobRecord, error)
	// WriteInfoFor returns the write-mode delivery block (branch/worktree/diff/files)
	// for a job; ok=false when the job is unknown or not a write job.
	WriteInfoFor(jobID string) (model.WriteInfo, bool)
	// WorktreeActivity reports a NON-mutating liveness/progress signal for a write
	// job: the count of agent-written files present in the worktree and the time of
	// the most recent change. Unlike WriteInfoFor it never stages (no git add -A).
	// ok=false for unknown or non-write jobs, or an absent worktree.
	WorktreeActivity(jobID string) (files int, lastModified time.Time, ok bool)
}

// RunStore is the subset of the runstore the handlers need.
// Load returns ErrRunNotFound (wrapped) when the run file does not exist yet (blind window).
type RunStore interface {
	Load(runsDir, runID string) (*model.Run, error)
	// ListItems returns ListItem rows for <cwd>/.pi/workflows/runs, newest-first, capped to limit.
	ListItems(cwd string, limit int) ([]model.ListItem, error)
}

// ErrRunNotFound is the sentinel handlers test with errors.Is. RunStore.Load wraps it
// when the run file is absent. The runstore adapter in internal/app maps
// runstore.ErrRunNotFound onto this sentinel.
var ErrRunNotFound = errors.New("run file not found")
