package jobs

import (
	"fmt"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// Cancel aborts a job: it kills the pi process (via the launch context), marks
// the job aborted, prunes the worktree for write jobs, and persists. It is
// idempotent on terminal jobs (returns the existing terminal snapshot, no
// prune, no kill). Unknown jobID is an error. The return is the live snapshot.
func (r *Registry) Cancel(jobID string) (model.JobRecord, error) {
	r.mu.Lock()
	j, ok := r.jobs[jobID]
	if !ok {
		r.mu.Unlock()
		return model.JobRecord{}, fmt.Errorf("unknown job %q", jobID)
	}

	switch j.Record.Status {
	case model.JobCompleted, model.JobFailed, model.JobAborted:
		// Already terminal: no-op.
		snap := j.snapshot()
		r.mu.Unlock()
		return snap, nil
	}

	isWrite := j.Record.Mode == model.ModeWrite
	worktree := j.Record.WorktreePath
	branch := j.Record.Branch

	switch j.Record.Status {
	case model.JobQueued:
		// Remove from the FIFO queue and mark aborted; it never launched.
		r.removeFromQueueUnlocked(j)
		j.markUnlocked(model.JobAborted, r.now())
		j.Record.ErrorCode = config.ErrWorkflowAborted
		_ = r.flushUnlocked()
		close(j.done)
		snap := j.snapshot()
		r.mu.Unlock()

		if isWrite {
			_ = r.pruner.Prune(worktree, branch)
		}
		return snap, nil

	case model.JobRunning:
		// Mark aborted FIRST so the wait()-goroutine attributes the kill to a
		// cancel rather than a failure. Then cancel the context to kill the pid;
		// the wait-goroutine calls finish(...,JobAborted,...) and releases the
		// slot + promotes the queue (and closes done). The worktree prune for
		// write jobs we launched happens in finish() AFTER the process exits —
		// never here, while the process may still hold the worktree.
		j.markUnlocked(model.JobAborted, r.now())
		j.Record.ErrorCode = config.ErrWorkflowAborted
		cancel := j.cancel
		_ = r.flushUnlocked()
		snap := j.snapshot()
		r.mu.Unlock()

		if cancel != nil {
			// A job we launched: kill the process; finish() prunes after it exits.
			cancel()
		} else if isWrite {
			// A reconcile-recovered running job: cancel==nil, done is already
			// closed, it holds no slot, and finish() will never run for it — so
			// prune the worktree synchronously (there is no process we own that
			// could still be writing it).
			_ = r.pruner.Prune(worktree, branch)
		}
		return snap, nil
	}

	// Unreachable: all non-terminal states handled above.
	snap := j.snapshot()
	r.mu.Unlock()
	return snap, nil
}

// removeFromQueueUnlocked drops target from the FIFO queue if present. Caller
// holds mu.
func (r *Registry) removeFromQueueUnlocked(target *Job) {
	out := r.queue[:0]
	for _, j := range r.queue {
		if j != target {
			out = append(out, j)
		}
	}
	r.queue = out
}
