package jobs

import (
	"context"
	"sync"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

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
	_ = r.flushUnlocked()
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

// tryAdmitUnlocked grabs a slot for j if one is free, marking it running.
// Otherwise it enqueues j (queued). Returns true iff admitted. Caller holds mu.
func (r *Registry) tryAdmitUnlocked(j *Job) bool {
	select {
	case r.slots <- struct{}{}:
		j.markUnlocked(model.JobRunning, r.now())
		return true
	default:
		j.markUnlocked(model.JobQueued, r.now())
		r.queue = append(r.queue, j)
		return false
	}
}

// start launches the pi process for an admitted (running) job in a goroutine.
func (r *Registry) start(j *Job) {
	ctx, cancel := context.WithCancel(context.Background())

	r.mu.Lock()
	j.cancel = cancel
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
	go func() {
		defer close(correlated)
		r.correlate(j, sessionCh)
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
// the runId (best-effort; the run file may not exist yet — blind window).
func (r *Registry) correlate(j *Job, sessionCh <-chan string) {
	sid, ok := <-sessionCh
	if !ok || sid == "" {
		return
	}
	r.mu.Lock()
	j.Record.SessionID = sid
	if runID, found := r.correlator.RunIDForSession(j.Record.RunsDir, sid); found {
		j.Record.RunID = runID
	}
	j.updatedAt = r.now()
	_ = r.flushUnlocked()
	r.mu.Unlock()
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
	wasRunningSlot := j.Record.Status == model.JobRunning || j.Record.Status == model.JobAborted
	j.markUnlocked(status, r.now())
	if errCode != "" {
		j.Record.ErrorCode = errCode
	}
	if errMsg != "" {
		j.Record.ErrorMessage = errMsg
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
				next.markUnlocked(model.JobRunning, r.now())
			default:
				// Should not happen (we just freed one); requeue defensively.
				next.markUnlocked(model.JobQueued, r.now())
				r.queue = append([]*Job{next}, r.queue...)
				next = nil
			}
		}
	}
	_ = r.flushUnlocked()
	close(j.done)
	r.mu.Unlock()

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
