package jobs

import (
	"context"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// Reconcile loads all rows from the shared DB and cleans up ONLY jobs whose
// owning server process is dead: it atomically claims each dead-owner non-terminal
// job as failed (SERVER_RESTARTED) and prunes its worktree (write mode). Jobs
// owned by a LIVE server (including another concurrent pi-mcp) are never touched —
// this is what makes the registry safe for N concurrent servers. The atomic claim
// ensures two starting servers never both prune the same job. Returns the number
// of rows seen.
func (r *Registry) Reconcile(ctx context.Context) (int, error) {
	recs, owners, err := r.store.AllJobs()
	if err != nil {
		return 0, err
	}
	for i := range recs {
		rec := recs[i]
		own := owners[i]
		if isTerminal(rec.Status) {
			continue
		}
		if pidAlive(own.Pid) {
			continue // a live server owns this job — hands off
		}
		// dead owner: atomically claim it terminal. Only the winner prunes.
		claimed, cerr := r.store.ClaimTerminal(rec.JobID, string(model.JobFailed),
			config.ErrServerRestarted, r.ownerPid, r.ownerStartedAt)
		if cerr != nil || !claimed {
			continue
		}
		if rec.Mode == model.ModeWrite && rec.WorktreePath != "" {
			if pidAlive(rec.PID) {
				// An orphaned pi child (reparented to init after the owning server
				// died abruptly) may still be writing this worktree. Pruning now would
				// lose data. The job is already claimed terminal; leave the worktree —
				// a later reconcile, once the child has exited, prunes it.
				continue
			}
			_ = r.pruner.Prune(rec.WorktreePath, rec.Branch)
		}
	}
	return len(recs), nil
}

func isTerminal(s model.JobStatus) bool {
	switch s {
	case model.JobCompleted, model.JobFailed, model.JobAborted:
		return true
	default:
		return false
	}
}
