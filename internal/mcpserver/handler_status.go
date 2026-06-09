package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
)

const (
	defaultWaitCap      = config.WaitCap
	defaultPollInterval = 250 * time.Millisecond
)

// snapshot captures the long-poll wake inputs. running<->paused is collapsed via mapDiskStatus
// (both -> "running") so flapping never triggers a wake.
type snapshot struct {
	journalLen int
	agentsLen  int
	phase      string
	terminal   bool
}

func snapshotOf(run *model.Run) snapshot {
	phase := ""
	if run.CurrentPhase != nil {
		phase = *run.CurrentPhase
	}
	return snapshot{
		journalLen: len(run.Journal),
		agentsLen:  len(run.Agents),
		phase:      phase,
		terminal:   isTerminal(mapDiskStatus(run.Status)),
	}
}

// wakeChanged reports whether the long-poll should return: terminal reached, OR journal grew,
// OR agents grew, OR phase changed. (running<->paused is excluded by construction.)
func wakeChanged(prev, cur snapshot) bool {
	if cur.terminal {
		return true
	}
	if cur.journalLen > prev.journalLen {
		return true
	}
	if cur.agentsLen > prev.agentsLen {
		return true
	}
	if cur.phase != prev.phase {
		return true
	}
	return false
}

// resolved identifies where the run file lives and whether an owning job exists.
type resolved struct {
	runsDir      string
	runID        string
	jobID        string
	mode         model.JobMode
	pid          int
	hasJob       bool
	worktree     string
	branch       string
	jobStatus    model.JobStatus // JobRecord.Status (terminal -> surface even with no run file)
	startedAt    time.Time       // JobRecord.StartedAt (elapsed-time heartbeat)
	errorMessage string          // JobRecord.ErrorMessage (§9 extraction order #1)
	errorCode    string          // JobRecord.ErrorCode
}

// resolveTarget turns StatusInput into a runsDir+runID via the jobId path or the runId+cwd path.
func (s *Server) resolveTarget(in model.StatusInput) (resolved, error) {
	if in.JobID != "" {
		rec, ok := s.jobs.Lookup(in.JobID)
		if !ok {
			return resolved{}, errors.New("unknown jobId: " + in.JobID)
		}
		return resolved{
			runsDir: rec.RunsDir, runID: rec.RunID, jobID: rec.JobID, mode: rec.Mode,
			pid: rec.PID, hasJob: true, worktree: rec.WorktreePath, branch: rec.Branch,
			jobStatus: rec.Status, startedAt: rec.StartedAt,
			errorMessage: rec.ErrorMessage, errorCode: rec.ErrorCode,
		}, nil
	}
	if in.RunID == "" || in.CWD == "" {
		return resolved{}, errors.New("provide jobId, or runId+cwd")
	}
	cwd, err := validateCWD(in.CWD)
	if err != nil {
		return resolved{}, err
	}
	r := resolved{runsDir: runsDirFor(cwd), runID: in.RunID}
	if rec, ok := s.jobs.LookupByRun(in.RunID, cwd); ok {
		r.jobID, r.mode, r.pid, r.hasJob = rec.JobID, rec.Mode, rec.PID, true
		r.worktree, r.branch = rec.WorktreePath, rec.Branch
		r.jobStatus, r.startedAt = rec.Status, rec.StartedAt
		r.errorMessage, r.errorCode = rec.ErrorMessage, rec.ErrorCode
		if rec.RunsDir != "" {
			r.runsDir = rec.RunsDir
		}
	}
	return r, nil
}

func runsDirFor(cwd string) string {
	return runstore.RunsDir(cwd)
}

func (s *Server) handleStatus(ctx context.Context, _ *mcp.CallToolRequest, in model.StatusInput) (*mcp.CallToolResult, model.StatusOutput, error) {
	tgt, err := s.resolveTarget(in)
	if err != nil {
		return nil, model.StatusOutput{}, err
	}

	if in.Wait {
		s.waitForChange(ctx, tgt)
	}

	out := s.buildStatus(tgt)
	return nil, out, nil
}

// buildStatus loads the run file once and assembles StatusOutput (no waiting).
func (s *Server) buildStatus(tgt resolved) model.StatusOutput {
	out := model.StatusOutput{JobID: tgt.jobID}

	now := s.now() // capture once so the skew math and heartbeat agree

	// Non-mutating worktree liveness/progress (write jobs only): a write job that
	// edits files directly leaves the run file frozen while it works, so a recently
	// modified worktree is the authoritative "still alive" signal. The mtime must be
	// within +/- StaleThreshold of now: a small future mtime is plausible clock skew
	// (networked FS / container drift) and still counts as activity, but a mtime far
	// in the future is corrupt, not liveness, and must not mask a wedged job.
	wtFiles, wtLast, wtOK := 0, time.Time{}, false
	if tgt.mode == model.ModeWrite && tgt.hasJob {
		wtFiles, wtLast, wtOK = s.jobs.WorktreeActivity(tgt.jobID)
	}
	worktreeActive := wtOK && now.Sub(wtLast).Abs() <= config.StaleThreshold

	run, err := s.store.Load(tgt.runsDir, tgt.runID)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			if tgt.hasJob {
				if isTerminal(string(tgt.jobStatus)) {
					// terminal job whose run file never appeared -> surface the
					// real terminal status (wire vocabulary already), not blind.
					out.Status = string(tgt.jobStatus)
					out.BlindWindow = false
					if tgt.jobStatus == model.JobFailed || tgt.jobStatus == model.JobAborted {
						out.Error = failureMessage(tgt, nil)
					}
					if tgt.mode == model.ModeWrite {
						out.Write = s.writeBlock(tgt, nil)
					}
					return out
				}
				// owning job exists but run file absent -> blind window (pi is still
				// authoring the workflow). Surface an elapsed heartbeat so a long
				// authoring phase is never an opaque silence.
				out.Status = "running"
				out.BlindWindow = true
				if a, ok := s.store.ReadAuthoring(tgt.runsDir, tgt.jobID); ok {
					out.Authoring = a
				}
				if tgt.mode == model.ModeWrite {
					out.Write = s.writeBlock(tgt, nil)
				}
				out.Progress = progressBlock(tgt, now, wtFiles, wtLast, wtOK)
				return out
			}
			// runId path for a run that does not exist yet -> queued/pending, NOT an error
			out.Status = "queued"
			return out
		}
		// genuine read/parse error
		out.Status = "failed"
		out.Error = config.ErrPersistenceError
		return out
	}

	rid := run.RunID
	out.RunID = &rid
	if run.CurrentPhase != nil {
		ph := *run.CurrentPhase
		out.Phase = &ph
	}

	diskStatus := mapDiskStatus(run.Status)
	out.Status = liveStatus(run.Status, run.UpdatedAt, now, s.pidIsAlive(tgt), worktreeActive)
	out.Intermediate = runstore.Intermediates(run, config.MaxInlineResultBytes)
	md := runstore.Metadata(run)
	out.Metadata = &md

	if out.Status == "completed" {
		res, _ := coerceResult(run.Result, out.Status)
		// res is the coerced json.RawMessage; unmarshal to `any` so the OUTPUT
		// struct carries a real object/array/scalar. The field is `any` (not
		// json.RawMessage) so go-sdk output-schema validation accepts it.
		out.Result = runstore.RawToAny(res)
	}
	if out.Status == "failed" || out.Status == "aborted" {
		out.Error = failureMessage(tgt, run)
	}

	if tgt.mode == model.ModeWrite && tgt.hasJob {
		if isTerminal(diskStatus) && diskStatus != "aborted" {
			// process actually exited and worktree intact -> safe to stage+diff.
			if wi, ok := s.jobs.WriteInfoFor(tgt.jobID); ok {
				w := wi
				out.Write = &w
			} else {
				// fall back to what the JobRecord already knows (branch/worktree)
				out.Write = &model.WriteInfo{
					Branch:       tgt.branch,
					WorktreePath: tgt.worktree,
				}
			}
		} else {
			// still running/paused/blind, or aborted (worktree pruned): emit the
			// branch (and worktree, if intact) WITHOUT staging via git add -A.
			out.Write = s.writeBlock(tgt, run)
		}
	}

	// Heartbeat for a still-running job: elapsed time + (write) worktree activity,
	// so callers can tell a slow-but-working job from a wedged one.
	if !isTerminal(out.Status) {
		out.Progress = progressBlock(tgt, now, wtFiles, wtLast, wtOK)
	}
	return out
}

// progressBlock builds the elapsed-time heartbeat plus, for write jobs, the
// worktree file count and the age of the newest change. Durations are clamped at
// zero so clock skew (a startedAt or worktree mtime ahead of now) never surfaces a
// nonsensical negative. Returns nil when there is no owning job / no start time
// (e.g. the runId path for an external run).
func progressBlock(tgt resolved, now time.Time, wtFiles int, wtLast time.Time, wtOK bool) *model.Progress {
	if !tgt.hasJob || tgt.startedAt.IsZero() {
		return nil
	}
	p := &model.Progress{ElapsedSeconds: clampSeconds(now.Sub(tgt.startedAt))}
	if wtOK {
		p.WorktreeFiles = wtFiles
		la := clampSeconds(now.Sub(wtLast))
		p.LastActivitySeconds = &la
	}
	return p
}

// clampSeconds floors a duration at zero and returns whole seconds.
func clampSeconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// writeBlock emits a NON-mutating write block: branch always, worktree only when
// the worktree is intact (i.e. the job is not aborted, whose worktree was pruned).
// It never runs git add -A; use the WriteInfoFor path for staged diffs.
func (s *Server) writeBlock(tgt resolved, run *model.Run) *model.WriteInfo {
	wi := &model.WriteInfo{Branch: tgt.branch}
	aborted := tgt.jobStatus == model.JobAborted
	if run != nil && mapDiskStatus(run.Status) == "aborted" {
		aborted = true
	}
	if !aborted {
		wi.WorktreePath = tgt.worktree
	}
	return wi
}

func (s *Server) pidIsAlive(tgt resolved) bool {
	if !tgt.hasJob || tgt.pid == 0 || s.pidAlive == nil {
		return true // no liveness signal -> assume alive (do not falsely fail external runs)
	}
	return s.pidAlive(tgt.pid)
}

// failureMessage follows §9 extraction order:
//
//	#1 JobRecord.ErrorMessage (the WORKFLOW isError text captured at submit/launch),
//	#2 run agents[].error,
//	#3 run logs[0] verbatim,
//	then JobRecord.ErrorCode, else a generic code. run may be nil (no run file).
func failureMessage(tgt resolved, run *model.Run) string {
	if tgt.errorMessage != "" {
		return tgt.errorMessage
	}
	if run != nil {
		for _, a := range run.Agents {
			if a.Status == "error" && a.Error != nil && *a.Error != "" {
				return *a.Error
			}
		}
		if len(run.Logs) > 0 {
			return run.Logs[0] // verbatim; never reconstruct the mangled prefix
		}
	}
	if tgt.errorCode != "" {
		return tgt.errorCode
	}
	return config.ErrUnknown
}

// waitForChange long-polls until the wake predicate fires or the wait cap elapses.
func (s *Server) waitForChange(ctx context.Context, tgt resolved) {
	deadline := s.now().Add(s.waitCap)
	var base snapshot
	haveBase := false

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		run, err := s.store.Load(tgt.runsDir, tgt.runID)
		if err == nil {
			cur := snapshotOf(run)
			if !haveBase {
				base, haveBase = cur, true
				if cur.terminal {
					return
				}
			} else if wakeChanged(base, cur) {
				return
			}
		}
		if !s.now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
