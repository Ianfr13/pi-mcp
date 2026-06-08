package jobs

import (
	"context"
	"fmt"
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
}

// Registry is the concurrency-capped, disk-persisted job map.
type Registry struct {
	mu           sync.Mutex
	jobs         map[string]*Job // jobID -> Job
	queue        []*Job          // FIFO of queued jobs awaiting a slot
	slots        chan struct{}   // buffered to cap; a token == an occupied running slot
	cap          int
	persistPath  string
	worktreeRoot string
	now          func() time.Time
	closed       bool

	launcher   Launcher
	correlator Correlator
	pruner     Pruner
}

// NewRegistry builds a Registry. cfg.Cap<=0 uses config.DefaultConcurrencyCap.
// Dependency injection happens here: the app builds the real Launcher/
// Correlator/Pruner and passes them in. There is no jobs.New / jobs.Options.
func NewRegistry(cfg Config, l Launcher, c Correlator, p Pruner) *Registry {
	capN := cfg.Cap
	if capN <= 0 {
		capN = config.DefaultConcurrencyCap
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Registry{
		jobs:         make(map[string]*Job),
		slots:        make(chan struct{}, capN),
		cap:          capN,
		persistPath:  cfg.PersistPath,
		worktreeRoot: cfg.WorktreeRoot,
		now:          now,
		launcher:     l,
		correlator:   c,
		pruner:       p,
	}
}

// recordsUnlocked returns persisted-shape records for all jobs. Caller holds mu.
func (r *Registry) recordsUnlocked() []model.JobRecord {
	out := make([]model.JobRecord, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j.snapshot())
	}
	return out
}

// flushUnlocked persists the current registry. Caller holds mu. Persistence
// errors are returned (callers log-and-continue; a launch never blocks on a
// persistence error).
func (r *Registry) flushUnlocked() error {
	return persist(r.persistPath, r.recordsUnlocked())
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

// start launches the pi process for an admitted (running) job in a goroutine.
// The launch ctx/cancel were installed under r.mu at admission time (by
// startingUnlocked), so start() only reads them — it never creates its own.
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
	}
	r.mu.Unlock()

	pid, sessionCh, wait, err := r.launcher.Launch(ctx, spec)
	if err != nil {
		cancel()
		r.finish(j, model.JobFailed, config.ErrAgentExecutionError, err.Error())
		return
	}

	r.mu.Lock()
	j.Record.PID = pid
	j.updatedAt = r.now()
	correlated := make(chan struct{})
	j.correlated = correlated
	_ = r.flushUnlocked()
	r.mu.Unlock()

	// Correlate jobId -> runId via the first session event. Closing correlated
	// lets finish() wait for this goroutine, so no late flush races test cleanup.
	// ctx is the launch context; the wait()-goroutine cancels it the moment the
	// process exits (before finish() blocks on `correlated`), which bounds the
	// correlation poll without deadlocking against finish().
	go func() {
		defer close(correlated)
		r.correlate(ctx, j, sessionCh)
	}()

	go func() {
		werr := wait()
		cancel()
		if werr != nil {
			// Distinguish a cancel-induced kill (already aborted) from a failure.
			r.mu.Lock()
			aborted := j.Record.Status == model.JobAborted
			r.mu.Unlock()
			if aborted {
				r.finish(j, model.JobAborted, "", "")
			} else {
				r.finish(j, model.JobFailed, config.ErrAgentExecutionError, werr.Error())
			}
			return
		}
		r.finish(j, model.JobCompleted, "", "")
	}()
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
		// Free the slot, then promote the next queued job (reuse the same slot).
		select {
		case <-r.slots:
		default:
		}
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
	// Capture the write-prune inputs and the post-CAS final status under the lock;
	// the actual prune runs after Unlock so it never blocks the registry, but
	// before close(done) so the prune happens-before any waiter observes done.
	isWrite := j.Record.Mode == model.ModeWrite
	worktree := j.Record.WorktreePath
	branch := j.Record.Branch
	finalStatus := j.Record.Status
	_ = r.flushUnlocked()
	r.mu.Unlock()

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
	return err
}
