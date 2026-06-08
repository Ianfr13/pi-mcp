package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// Reconcile rebuilds the in-memory registry from the persisted file after a
// restart, applies the post-restart liveness heuristic (running + stale ->
// failed; PID is NOT trusted across restarts), GCs orphan worktrees
// (<worktreeRoot>/<WorktreeSubdir>/job-* with no live, non-terminal record),
// and returns the count of records loaded/reconciled. ctx is accepted for the
// app seam (reconciliation is local I/O and does not block on it).
// TODO(Task 3/4): replaced by owner-scoped reconcile using store.AllJobs.
func (r *Registry) Reconcile(ctx context.Context) (int, error) {
	// Temporary stub: no records loaded until Task 3 wires the store.
	records := []model.JobRecord{}
	now := r.now()

	r.mu.Lock()
	live := make(map[string]bool) // jobID -> still non-terminal after reconcile
	for _, rec := range records {
		rec := rec
		// Post-restart: sameSession=false; updatedAt unknown, approximate with
		// StartedAt (documented heuristic).
		orig := rec.Status
		eff := effectiveStatus(rec, rec.StartedAt, false, now)
		rec.Status = eff
		switch {
		case orig == model.JobQueued:
			// A recovered queued job cannot be relaunched (Task/Context are never
			// persisted), so terminalize it instead of leaving it stuck.
			rec.Status = model.JobFailed
			rec.ErrorCode = config.ErrServerRestarted
		case orig == model.JobRunning && eff == model.JobFailed:
			// Stale running -> failed: surface a code (for finding-#5) but never
			// clobber a persisted engine error code.
			if rec.ErrorCode == "" {
				rec.ErrorCode = config.ErrServerRestarted
			}
		case eff == model.JobRunning && rec.SessionID != "" && rec.RunID == "":
			// Still running and uncorrelated: try a one-shot, non-blocking resume
			// of the sessionId->runId mapping.
			if runID, ok := r.correlator.RunIDForSession(rec.RunsDir, rec.SessionID); ok {
				rec.RunID = runID
			}
		}
		j := &Job{
			Record:    rec,
			updatedAt: now,
			done:      make(chan struct{}),
		}
		// Recovered jobs are not running under this process; close done so
		// waiters do not hang on terminal/stale records.
		close(j.done)
		r.jobs[rec.JobID] = j
		if !isTerminal(rec.Status) {
			live[rec.JobID] = true
		}
	}
	_ = r.flushUnlocked()
	r.mu.Unlock()

	// GC orphan worktrees.
	if r.worktreeRoot != "" {
		r.gcOrphanWorktrees(live)
	}
	return len(records), nil
}

// gcOrphanWorktrees prunes any <worktreeRoot>/<WorktreeSubdir>/job-<id>
// directory whose jobID is not in the live (non-terminal) set.
func (r *Registry) gcOrphanWorktrees(live map[string]bool) {
	base := filepath.Join(r.worktreeRoot, config.WorktreeSubdir)
	entries, err := os.ReadDir(base)
	if err != nil {
		return // base dir absent -> nothing to GC
	}
	const prefix = "job-"
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		jobID := strings.TrimPrefix(e.Name(), prefix)
		if live[jobID] {
			continue // active job: keep its worktree
		}
		wtPath := filepath.Join(base, e.Name())
		branch := config.WorktreeBranchPrefix + jobID // "pi-mcp/job-<id>"
		_ = r.pruner.Prune(wtPath, branch)
	}
}

func isTerminal(s model.JobStatus) bool {
	switch s {
	case model.JobCompleted, model.JobFailed, model.JobAborted:
		return true
	default:
		return false
	}
}
