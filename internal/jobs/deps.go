package jobs

import "context"

// Launcher spawns the `pi` subprocess for a job and reports its PID plus the
// sessionId observed on the FIRST stream `session` event. The returned wait
// function blocks until the process exits and reports terminal success/failure.
// Implemented by the app package over internal/runner; mocked in tests.
//
// The 4-tuple return IS the launch handle; there is no separate handle type.
type Launcher interface {
	// Launch starts the process. It must return promptly once the PID is known
	// (it does NOT block for completion). sessionCh receives the sessionId from
	// the first `session` event (exactly once); it is closed if the process dies
	// before emitting one. wait blocks until exit and returns an error iff the
	// process failed. The error string, when present, is the §9 #1 failure text
	// (parser workflow isError content) and is stored on the record. Killing the
	// process is done by cancelling ctx.
	Launch(ctx context.Context, spec Spec) (pid int, sessionCh <-chan string, wait func() error, err error)
}

// Correlator resolves a sessionId to a runId by consulting the job's runs dir.
// Implemented by the app package over internal/runstore; mocked in tests.
type Correlator interface {
	// RunIDForSession scans runsDir for a run whose sessionId matches and returns
	// its runId. ok is false if no matching run file exists yet.
	RunIDForSession(runsDir, sessionID string) (runID string, ok bool)
}

// Pruner removes the worktree + branch for a write job. Implemented by the app
// package over internal/worktree; mocked in tests. No-op for read jobs.
type Pruner interface {
	// Prune removes the worktree at path and deletes branch (best-effort). It is
	// safe to call when the worktree is already gone.
	Prune(worktreePath, branch string) error
}
