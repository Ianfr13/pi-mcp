package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
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
	errorMessage string // JobRecord.ErrorMessage (§9 extraction order #1)
	errorCode    string // JobRecord.ErrorCode
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
		r.errorMessage, r.errorCode = rec.ErrorMessage, rec.ErrorCode
		if rec.RunsDir != "" {
			r.runsDir = rec.RunsDir
		}
	}
	return r, nil
}

func runsDirFor(cwd string) string {
	return cwd + "/" + config.RunsDirRel
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

	run, err := s.store.Load(tgt.runsDir, tgt.runID)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			if tgt.hasJob {
				// owning job exists but run file absent -> blind window
				out.Status = "running"
				out.BlindWindow = true
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

	out.Status = liveStatus(run.Status, run.UpdatedAt, s.now(), s.pidIsAlive(tgt))
	out.Intermediate = buildIntermediate(run, config.MaxInlineResultBytes)
	out.Metadata = buildMetadata(run)

	if out.Status == "completed" {
		res, _ := coerceResult(run.Result, out.Status)
		// res is the coerced json.RawMessage; unmarshal to `any` so the OUTPUT
		// struct carries a real object/array/scalar. The field is `any` (not
		// json.RawMessage) so go-sdk output-schema validation accepts it.
		out.Result = rawToAny(res)
	}
	if out.Status == "failed" || out.Status == "aborted" {
		out.Error = failureMessage(tgt, run)
	}

	if tgt.mode == model.ModeWrite && tgt.hasJob {
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
	}
	return out
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
//	else a generic code.
func failureMessage(tgt resolved, run *model.Run) string {
	if tgt.errorMessage != "" {
		return tgt.errorMessage
	}
	for _, a := range run.Agents {
		if a.Status == "error" && a.Error != nil && *a.Error != "" {
			return *a.Error
		}
	}
	if len(run.Logs) > 0 {
		return run.Logs[0] // verbatim; never reconstruct the mangled prefix
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
