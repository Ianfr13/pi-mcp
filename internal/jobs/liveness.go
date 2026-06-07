package jobs

import (
	"errors"
	"syscall"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// pidAlive reports whether a process with the given pid currently exists.
// Uses signal 0 (no signal delivered, existence/permission check only).
// pid <= 0 is never alive. EPERM means the process exists but is owned by
// another user -> alive.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// effectiveStatus applies the liveness override to a record's persisted status.
//   - Terminal statuses (completed/failed/aborted) and JobQueued are unchanged.
//   - For JobRunning:
//   - if updatedAt is older than config.StaleThreshold -> JobFailed (works
//     both same-session and post-restart);
//   - else, when sameSession is true and the PID is dead -> JobFailed;
//   - otherwise -> JobRunning.
//
// updatedAt is the registry-local last-transition time; now is the injectable
// clock. sameSession is true only when this server process is the one that
// launched the job (so its PID is meaningful); after a restart it is false and
// only the staleness heuristic applies.
func effectiveStatus(rec model.JobRecord, updatedAt time.Time, sameSession bool, now time.Time) model.JobStatus {
	switch rec.Status {
	case model.JobCompleted, model.JobFailed, model.JobAborted, model.JobQueued:
		return rec.Status
	case model.JobRunning:
		if !updatedAt.IsZero() && now.Sub(updatedAt) > config.StaleThreshold {
			return model.JobFailed
		}
		if sameSession && !pidAlive(rec.PID) {
			return model.JobFailed
		}
		return model.JobRunning
	default:
		return rec.Status
	}
}

// StatusOf returns the liveness-adjusted status for a job. sameSession should
// be true when this process launched the job (its PID is meaningful) and false
// for records recovered after a restart. Unknown jobID returns JobStatus("").
func (r *Registry) StatusOf(jobID string, sameSession bool) model.JobStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return model.JobStatus("")
	}
	return effectiveStatus(j.snapshot(), j.updatedAt, sameSession, r.now())
}
