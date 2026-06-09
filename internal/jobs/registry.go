package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// correlatePollInterval is how often correlate() re-scans the runs dir for the
// late-arriving run file after the session event (the run file lands ~20-26s
// later). Kept off the hot path; never busy-spins.
var correlatePollInterval = 500 * time.Millisecond

// Config configures a Registry.
type Config struct {
	Cap          int              // max concurrent running jobs; <=0 -> DefaultConcurrencyCap
	PersistPath  string           // path to the registry JSON file
	WorktreeRoot string           // root under which pi-mcp/job-* worktrees live (reconcile scan)
	Now          func() time.Time // injectable clock; nil -> time.Now
	// SnapshotRun reads the final run file (runsDir,runID) and returns its
	// canonical JSON for persistence into the registry at terminal time, or nil
	// when there is no readable run file. Injected by the app (runstore-backed);
	// nil disables snapshotting (tests).
	SnapshotRun func(runsDir, runID string) []byte
}

// Registry is the concurrency-capped, disk-persisted job map.
type Registry struct {
	mu     sync.Mutex
	jobs   map[string]*Job // jobID -> Job
	queue  []*Job          // FIFO of queued jobs awaiting a slot
	slots  chan struct{}   // buffered to cap; a token == an occupied running slot
	cap    int
	now    func() time.Time
	closed bool
	wg     sync.WaitGroup // tracks runAttempts goroutines; Close waits on it before store.Close

	launcher   Launcher
	correlator Correlator
	pruner     Pruner

	// hasRunFile reports whether a run file exists in runsDir (the fleet started).
	// It is the authoring-retry oracle: a failure with NO run file is an authoring
	// failure (cheap to retry); a failure WITH one is an execution failure (don't
	// re-run the fleet). Injectable so tests decide deterministically.
	hasRunFile func(runsDir string) bool

	store          *regStore
	ownerPid       int
	ownerStartedAt string

	// snapshotRun captures the final run file into the registry at terminal time
	// (nil disables). See Config.SnapshotRun.
	snapshotRun func(runsDir, runID string) []byte
}

// NewRegistry builds a Registry over a SQLite store at cfg.PersistPath. cfg.Cap<=0
// uses config.DefaultConcurrencyCap. Returns an error if the store cannot open.
func NewRegistry(cfg Config, l Launcher, c Correlator, p Pruner) (*Registry, error) {
	capN := cfg.Cap
	if capN <= 0 {
		capN = config.DefaultConcurrencyCap
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	store, err := OpenStore(cfg.PersistPath)
	if err != nil {
		return nil, err
	}
	return &Registry{
		jobs:           make(map[string]*Job),
		slots:          make(chan struct{}, capN),
		cap:            capN,
		now:            now,
		launcher:       l,
		correlator:     c,
		pruner:         p,
		hasRunFile:     runFileExists,
		store:          store,
		ownerPid:       os.Getpid(),
		ownerStartedAt: now().UTC().Format(time.RFC3339Nano),
		snapshotRun:    cfg.SnapshotRun,
	}, nil
}

// recordsUnlocked returns persisted-shape records for all jobs. Caller holds mu.
func (r *Registry) recordsUnlocked() []model.JobRecord {
	out := make([]model.JobRecord, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j.snapshot())
	}
	return out
}

// flushUnlocked persists THIS server's jobs (owner-stamped) to the shared DB.
// It only upserts rows this server owns, so concurrent servers never clobber
// each other. Caller holds mu.
func (r *Registry) flushUnlocked() error {
	return r.store.UpsertJobs(r.recordsUnlocked(), r.ownerPid, r.ownerStartedAt)
}

// Submit admits a new job if a slot is free (Status=running) or queues it
// (Status=queued). It returns the initial persisted record immediately and
// never blocks on job completion (no MCP-level timeout). The pi process is run
// by a per-job goroutine that correlates sessionId->runId and persists every
// state transition. ctx is accepted for the consumer seam; admission does not
// block on it (each job spawns its own context internally).
func (r *Registry) Submit(ctx context.Context, spec Spec) (model.JobRecord, error) {
	j := NewJob(spec)
	r.mu.Lock()
	r.jobs[j.Record.JobID] = j
	admitted := r.tryAdmitUnlocked(j)
	// The initial Submit flush is mandatory: if we cannot persist the new job we
	// must not proceed (an un-persisted running job would be lost on restart).
	// Roll back tryAdmitUnlocked exactly under the SAME held lock, then fail.
	// (Later flushes in start/correlate/finish stay best-effort for now.)
	if flushErr := r.flushUnlocked(); flushErr != nil {
		delete(r.jobs, j.Record.JobID)
		if admitted {
			select {
			case <-r.slots:
			default:
			}
			if j.cancel != nil {
				j.cancel()
			}
		} else {
			r.removeFromQueueUnlocked(j)
		}
		r.mu.Unlock()
		return model.JobRecord{}, fmt.Errorf("%s: %w", config.ErrPersistenceError, flushErr)
	}
	r.mu.Unlock()

	if admitted {
		r.start(j) // synchronous up to PID-known; sets j.Record.PID
	}

	// Re-snapshot so the returned record reflects the PID (and running status)
	// captured during start. Queued jobs are returned with PID 0 / queued.
	r.mu.Lock()
	rec := j.snapshot()
	r.mu.Unlock()
	return rec, nil
}

// startingUnlocked installs the launch context+cancel AND marks the job running
// atomically under r.mu, so a running job never coexists with a nil cancel (the
// cancel-vs-launch race). Caller holds mu.
func (r *Registry) startingUnlocked(j *Job) {
	ctx, cancel := context.WithCancel(context.Background())
	j.ctx = ctx
	j.cancel = cancel
	j.markUnlocked(model.JobRunning, r.now())
}

// tryAdmitUnlocked grabs a slot for j if one is free, marking it running.
// Otherwise it enqueues j (queued). Returns true iff admitted. Caller holds mu.
func (r *Registry) tryAdmitUnlocked(j *Job) bool {
	select {
	case r.slots <- struct{}{}:
		r.startingUnlocked(j)
		return true
	default:
		j.markUnlocked(model.JobQueued, r.now())
		r.queue = append(r.queue, j)
		return false
	}
}

// start launches the pi process for an admitted (running) job. The FIRST launch
// is synchronous so Submit returns with the PID known; the per-attempt correlate,
// the wait, and any authoring retries run in a background goroutine. The launch
// ctx/cancel were installed under r.mu at admission time (by startingUnlocked),
// so start() only reads them — it never creates its own.
func (r *Registry) start(j *Job) {
	r.mu.Lock()
	ctx := j.ctx
	cancel := j.cancel
	spec := Spec{
		Mode:     j.Record.Mode,
		CWD:      j.Record.CWD,
		RunsDir:  j.Record.RunsDir,
		Task:     j.task,
		Context:  j.context,
		Worktree: j.Record.WorktreePath,
		Branch:   j.Record.Branch,
		JobID:    j.Record.JobID,
	}
	r.mu.Unlock()

	pid, sessionCh, wait, err := r.launcher.Launch(ctx, spec)
	if err != nil {
		cancel()
		r.finish(j, model.JobFailed, config.ErrAgentExecutionError, err.Error())
		return
	}

	r.mu.Lock()
	if r.closed {
		// Close() raced ahead: do not spawn a new job goroutine (its terminal flush
		// could hit a closed store). The job is already flushed by Close; cancel its
		// context so the just-launched pi process (group) is reaped.
		r.mu.Unlock()
		cancel()
		return
	}
	j.Record.PID = pid
	j.updatedAt = r.now()
	_ = r.flushUnlocked()
	r.wg.Add(1) // under mu, while !closed, so Close's Wait() observes every started goroutine
	r.mu.Unlock()

	go r.runAttempts(j, ctx, cancel, spec, sessionCh, wait)
}

// runAttempts runs the correlate+wait cycle for the current pi process and, on an
// AUTHORING failure (the process failed before any run file was created — the
// fleet never ran), relaunches up to config.MaxAuthoringRetries times. A failure
// AFTER a run file exists (the fleet ran, so RunID resolved) is NOT retried — that
// would re-run the expensive fleet — and neither is an aborted or ctx-canceled
// job. It calls finish() exactly once. sessionCh/wait belong to the current
// attempt; the first pair comes from start().
func (r *Registry) runAttempts(j *Job, ctx context.Context, cancel context.CancelFunc, spec Spec, sessionCh <-chan string, wait func() error) {
	defer r.wg.Done()
	for attempt := 0; ; attempt++ {
		// Per-attempt correlate context: stops this attempt's poll without tearing
		// down the job ctx (which a retry reuses for the next launch).
		attemptCtx, attemptCancel := context.WithCancel(ctx)
		r.mu.Lock()
		correlated := make(chan struct{})
		j.correlated = correlated
		_ = r.flushUnlocked()
		r.mu.Unlock()

		go func(sc <-chan string) {
			defer close(correlated)
			r.correlate(attemptCtx, j, sc)
		}(sessionCh)

		werr := wait()

		if werr == nil {
			attemptCancel()
			cancel()
			r.finish(j, model.JobCompleted, "", "")
			return
		}

		r.mu.Lock()
		aborted := j.Record.Status == model.JobAborted
		r.mu.Unlock()
		if aborted {
			attemptCancel()
			cancel()
			r.finish(j, model.JobAborted, "", "")
			return
		}

		// Retry ONLY an authoring failure: the process failed before any run file
		// was created (the fleet never ran), so relaunching is cheap. A run file
		// present -> execution failure -> do NOT re-run the fleet. The decision is
		// a deterministic disk check, NOT the async correlate's resolved RunID (a
		// premature cancel could leave it unresolved and trigger a bogus retry).
		if attempt < config.MaxAuthoringRetries && ctx.Err() == nil && !r.hasRunFile(spec.RunsDir) {
			attemptCancel()
			<-correlated // serialize: this attempt's correlate must finish before the next
			r.mu.Lock()
			j.Record.RunID = ""
			j.Record.SessionID = ""
			j.updatedAt = r.now()
			_ = r.flushUnlocked()
			r.mu.Unlock()

			npid, nsc, nwait, nerr := r.launcher.Launch(ctx, spec)
			if nerr != nil {
				cancel()
				r.finish(j, model.JobFailed, config.ErrAgentExecutionError, nerr.Error())
				return
			}
			r.mu.Lock()
			j.Record.PID = npid
			j.updatedAt = r.now()
			_ = r.flushUnlocked()
			r.mu.Unlock()
			sessionCh, wait = nsc, nwait
			continue
		}

		attemptCancel()
		cancel()
		r.finish(j, model.JobFailed, config.ErrAgentExecutionError, werr.Error())
		return
	}
}

// runFileExists reports whether runsDir contains any *.json run file (the fleet
// started). A missing/empty dir -> false (an authoring failure never wrote one).
func runFileExists(runsDir string) bool {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			return true
		}
	}
	return false
}

// correlate consumes the sessionId from the first session event and resolves
// the runId. The run file is written by pi ~20-26s AFTER the session event, so a
// single lookup almost always misses (blind window) and would leave RunID=""
// forever. Instead we push the sessionId immediately, then POLL
// RunIDForSession on an interval until it resolves, the launch ctx is canceled
// (process exited / Close()), or the session channel closed without a sessionId.
// The poll is bounded by the job lifetime: ctx.Done() fires when wait() returns,
// before finish() blocks on `correlated`, so there is no deadlock and no leak.
func (r *Registry) correlate(ctx context.Context, j *Job, sessionCh <-chan string) {
	var sid string
	select {
	case s, ok := <-sessionCh:
		if !ok || s == "" {
			return
		}
		sid = s
	case <-ctx.Done():
		return
	}

	// Push the sessionId immediately so it is persisted even if the run file
	// never appears (resolved == false below).
	r.mu.Lock()
	j.Record.SessionID = sid
	resolved := r.tryResolveRunUnlocked(j, sid)
	j.updatedAt = r.now()
	_ = r.flushUnlocked()
	r.mu.Unlock()
	if resolved {
		return
	}

	// Poll for the late-arriving run file until it resolves or the job ends.
	ticker := time.NewTicker(correlatePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			resolved := r.tryResolveRunUnlocked(j, sid)
			if resolved {
				j.updatedAt = r.now()
				_ = r.flushUnlocked()
			}
			r.mu.Unlock()
			if resolved {
				return
			}
		}
	}
}

// tryResolveRunUnlocked sets j.Record.RunID from the correlator if the run file
// for sid now exists. Returns true once RunID is populated. Caller holds mu.
func (r *Registry) tryResolveRunUnlocked(j *Job, sid string) bool {
	if j.Record.RunID != "" {
		return true
	}
	if runID, found := r.correlator.RunIDForSession(j.Record.RunsDir, sid); found {
		j.Record.RunID = runID
		return true
	}
	return false
}

// finish marks a job terminal, releases its slot, admits the next queued job,
// persists, and signals waiters. errCode/errMsg populate the record's error
// fields (empty for success / abort-without-error).
func (r *Registry) finish(j *Job, status model.JobStatus, errCode, errMsg string) {
	// Wait for the correlate goroutine (if any) to finish first, so its flush
	// cannot race a caller that observes j.done. Read the channel under the lock
	// to avoid a data race on the field, then wait outside the lock.
	r.mu.Lock()
	correlated := j.correlated
	r.mu.Unlock()
	if correlated != nil {
		<-correlated
	}

	r.mu.Lock()
	// Read the OLD status FIRST for the slot-release decision (a Cancel that set
	// aborted still owns a running slot to release).
	wasRunningSlot := j.Record.Status == model.JobRunning || j.Record.Status == model.JobAborted
	// COMPARE-AND-SET: only write status/error when not already terminal, so a
	// clean-exit finish(JobCompleted) preserves an 'aborted' that Cancel set.
	if !isTerminal(j.Record.Status) {
		j.markUnlocked(status, r.now())
		if errCode != "" {
			j.Record.ErrorCode = errCode
		}
		if errMsg != "" {
			j.Record.ErrorMessage = errMsg
		}
	}
	var next *Job
	if wasRunningSlot {
		// Free the slot.
		select {
		case <-r.slots:
		default:
		}
		// Do NOT promote a queued job once Close has begun: a goroutine started here
		// (via r.start(next) below) could flush after store.Close(). Queued jobs are
		// left for the next server's reconcile.
		if !r.closed {
			next = r.dequeueUnlocked()
			if next != nil {
				select {
				case r.slots <- struct{}{}:
					r.startingUnlocked(next)
				default:
					// Should not happen (we just freed one); requeue defensively.
					next.markUnlocked(model.JobQueued, r.now())
					r.queue = append([]*Job{next}, r.queue...)
					next = nil
				}
			}
		}
	}
	// Capture the write-prune inputs and the post-CAS final status under the lock;
	// the actual prune runs after Unlock so it never blocks the registry, but
	// before close(done) so the prune happens-before any waiter observes done.
	isWrite := j.Record.Mode == model.ModeWrite
	worktree := j.Record.WorktreePath
	branch := j.Record.Branch
	finalStatus := j.Record.Status
	jobID := j.Record.JobID
	runsDir := j.Record.RunsDir
	runID := j.Record.RunID
	_ = r.flushUnlocked()
	r.mu.Unlock()

	// Best-effort: snapshot the final run file into the registry so the dashboard
	// can still render this job after the on-disk run file is gone (temp cwd
	// cleaned / worktree pruned). Captured BEFORE any worktree prune below so an
	// aborted write job's run file is still readable.
	if r.snapshotRun != nil && runID != "" {
		if snap := r.snapshotRun(runsDir, runID); len(snap) > 0 {
			_ = r.store.SaveSnapshot(jobID, snap, r.ownerPid)
		}
	}

	if isWrite && finalStatus == model.JobAborted {
		_ = r.pruner.Prune(worktree, branch)
	}
	close(j.done)

	if next != nil {
		r.start(next)
	}
}

// dequeueUnlocked pops the head of the FIFO queue (or nil). Caller holds mu.
func (r *Registry) dequeueUnlocked() *Job {
	if len(r.queue) == 0 {
		return nil
	}
	j := r.queue[0]
	r.queue = r.queue[1:]
	return j
}

// Lookup returns a snapshot of the job's persisted record. ok is false if
// unknown.
func (r *Registry) Lookup(jobID string) (model.JobRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok {
		return model.JobRecord{}, false
	}
	return j.snapshot(), true
}

// LookupByRun finds a job by (runID, cwd). It matches a record whose
// RunID==runID AND (CWD==cwd OR WorktreePath==cwd) — pi_status passes the launch
// cwd for read jobs and the worktree_path for write jobs (spec §5.2/§5.3).
func (r *Registry) LookupByRun(runID, cwd string) (model.JobRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.Record.RunID == runID && (j.Record.CWD == cwd || j.Record.WorktreePath == cwd) {
			return j.snapshot(), true
		}
	}
	return model.JobRecord{}, false
}

// waitJob blocks until the job reaches a terminal state (test/helper use).
func (r *Registry) waitJob(jobID string) {
	r.mu.Lock()
	j, ok := r.jobs[jobID]
	r.mu.Unlock()
	if !ok {
		return
	}
	<-j.done
}

// Close cancels every running job's context (best-effort kill; no status
// change) and flushes the registry to disk. It is idempotent.
func (r *Registry) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	cancels := make([]func(), 0, len(r.jobs))
	for _, j := range r.jobs {
		if j.cancel != nil {
			cancels = append(cancels, j.cancel)
		}
	}
	err := r.flushUnlocked()
	r.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	r.wg.Wait() // every job goroutine has now flushed its terminal state; safe to close the store
	_ = r.store.Close()
	return err
}
