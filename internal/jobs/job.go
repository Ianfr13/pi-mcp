// Package jobs implements the pi-mcp job registry: a concurrency-capped,
// disk-persisted map of jobID -> running pi workflow, with startup
// reconciliation, PID/staleness liveness, and cancel.
package jobs

import (
	"context"
	"time"

	"github.com/google/uuid"

	"pi-mcp/internal/model"
)

// Spec is the immutable launch request for a job. Task and Context are kept
// in-memory only (the Launcher renders them into the forcing prompt); they are
// NEVER persisted to model.JobRecord because they may contain secrets (spec §1).
type Spec struct {
	Mode          model.JobMode
	CWD           string
	RunsDir       string
	Task          string // the user task (rendered into the forcing prompt by the Launcher)
	Context       string // optional extra context
	Worktree      string // write mode only
	Branch        string // write mode only
	PreassignedID string // when non-empty, NewJob uses this as JobID instead of minting
}

// Job is the in-memory, mutable wrapper around a persisted model.JobRecord plus
// runtime handles. All field access is guarded by the owning Registry's mutex
// except the done channel (which is closed exactly once when the job goes
// terminal).
type Job struct {
	Record     model.JobRecord
	task       string          // in-memory only (not persisted; may be secret)
	context    string          // in-memory only (not persisted; may be secret)
	updatedAt  time.Time       // registry-local last-transition clock (not persisted as a field)
	ctx        context.Context // the launch context (installed atomically with cancel under mu); nil until running
	cancel     func()          // cancels the launch context (kills the pi process); nil until running
	done       chan struct{}   // closed when the job reaches a terminal state
	correlated chan struct{}   // closed when the correlate goroutine exits; nil if never started
}

// NewID returns a fresh uuidv4 string. The app uses it to pre-mint a jobID when
// it must create a write worktree (branch pi-mcp/job-<id>) before Submit.
func NewID() string { return uuid.NewString() }

// NewJob mints a fresh queued job. The jobID is spec.PreassignedID when set,
// otherwise a fresh uuidv4. Task/Context are copied onto the in-memory Job only.
func NewJob(spec Spec) *Job {
	now := time.Now()
	id := spec.PreassignedID
	if id == "" {
		id = uuid.NewString()
	}
	return &Job{
		Record: model.JobRecord{
			JobID:        id,
			Mode:         spec.Mode,
			CWD:          spec.CWD,
			RunsDir:      spec.RunsDir,
			WorktreePath: spec.Worktree,
			Branch:       spec.Branch,
			Status:       model.JobQueued,
			StartedAt:    now,
		},
		task:      spec.Task,
		context:   spec.Context,
		updatedAt: now,
		done:      make(chan struct{}),
	}
}

// snapshot returns a copy of the persisted record (caller holds registry lock).
func (j *Job) snapshot() model.JobRecord { return j.Record }

// markUnlocked transitions the job's status and bumps the local clock.
// Caller MUST hold the registry lock.
func (j *Job) markUnlocked(status model.JobStatus, now time.Time) {
	j.Record.Status = status
	j.updatedAt = now
}
