package dashboard

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/livestatus"
	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
)

// runFileExists cheaply reports whether the on-disk run file (or its .bak) is
// present, so the detail handler can skip the snapshot DB read when the live
// file will be used anyway (the common case: running / recently-finished jobs).
func runFileExists(runsDir, runID string) bool {
	if runID == "" {
		return false
	}
	p := filepath.Join(runsDir, runID+".json")
	if _, err := os.Stat(p); err == nil {
		return true
	}
	_, err := os.Stat(p + ".bak")
	return err == nil
}

// Counts is the aggregate job tally for the overview.
type Counts struct {
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Aborted   int `json:"aborted"`
	Total     int `json:"total"`
}

// JobSummary is the light per-job row pushed over SSE.
type JobSummary struct {
	JobID        string         `json:"jobId"`
	Mode         string         `json:"mode"`
	Status       string         `json:"status"` // displayed (liveness-adjusted)
	WorkflowName string         `json:"workflowName,omitempty"`
	CWD          string         `json:"cwd"`
	WorktreePath string         `json:"worktreePath,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	RunID        string         `json:"runId,omitempty"`
	StartedAt    time.Time      `json:"startedAt"`
	CompletedAt  *time.Time     `json:"completedAt,omitempty"`
	BlindWindow  bool           `json:"blindWindow"`
	Phase        string         `json:"phase,omitempty"`
	AgentsDone   int            `json:"agentsDone"`
	AgentsTotal  int            `json:"agentsTotal"`
	FleetByModel map[string]int `json:"fleetByModel,omitempty"`
	LiveTokens   int64          `json:"liveTokens"`
	Cost         *float64       `json:"cost,omitempty"`
	DurationMs   *int64         `json:"durationMs,omitempty"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
}

// DashboardState is the top-level light snapshot pushed over SSE.
type DashboardState struct {
	GeneratedAt time.Time    `json:"generatedAt"`
	StateDir    string       `json:"stateDir"`
	Counts      Counts       `json:"counts"`
	Jobs        []JobSummary `json:"jobs"`
}

// AgentView is one fleet card in the heavy detail.
type AgentView struct {
	Label         string     `json:"label"`
	Model         string     `json:"model"`
	Phase         string     `json:"phase"`
	Status        string     `json:"status"`
	Tokens        int64      `json:"tokens"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	Error         string     `json:"error,omitempty"`
	Prompt        string     `json:"prompt,omitempty"`
	ResultPreview string     `json:"resultPreview,omitempty"`
}

// JobDetail is the heavy per-job view fetched on demand.
type JobDetail struct {
	JobSummary
	Phases       []string                   `json:"phases,omitempty"`
	Agents       []AgentView                `json:"agents"`
	Intermediate []model.IntermediateResult `json:"intermediate"`
	TokenUsage   *model.TokenUsage          `json:"tokenUsage,omitempty"`
	Result       any                        `json:"result,omitempty"`
	Authoring    *model.AuthoringInfo       `json:"authoring,omitempty"` // blind-window live plan
}

// readRun is the run-file loader seam (overridable in tests). It builds the path
// <runsDir>/<runId>.json and decodes it with runstore.ReadRun, which transparently
// recovers from the sibling .bak snapshot when the primary .json is missing or
// corrupt (pi writes .bak alongside). NOTE: do NOT swap this for runstore.Load —
// Load short-circuits on os.Stat(.json) and never reaches the .bak fallback, so a
// .json-missing/.bak-present run would wrongly read as "no run data".
var readRun = func(runsDir, runID string) (*model.Run, error) {
	if runID == "" {
		return nil, fs.ErrNotExist
	}
	return runstore.ReadRun(filepath.Join(runsDir, runID+".json"))
}

// BuildState derives the light snapshot from the registry records.
func BuildState(recs []model.JobRecord, stateDir string, now time.Time) DashboardState {
	st := DashboardState{GeneratedAt: now, StateDir: stateDir, Jobs: make([]JobSummary, 0, len(recs))}
	for i := range recs {
		js := summarize(recs[i], now)
		st.Jobs = append(st.Jobs, js)
		st.Counts.Total++
		switch js.Status {
		case "running":
			st.Counts.Running++
		case "queued":
			st.Counts.Queued++
		case "completed":
			st.Counts.Completed++
		case "failed":
			st.Counts.Failed++
		case "aborted":
			st.Counts.Aborted++
		}
	}
	sort.SliceStable(st.Jobs, func(a, b int) bool {
		ta, tb := isTerminalStatus(st.Jobs[a].Status), isTerminalStatus(st.Jobs[b].Status)
		if ta != tb {
			return !ta // active (non-terminal) first
		}
		return st.Jobs[a].StartedAt.After(st.Jobs[b].StartedAt)
	})
	return st
}

func isTerminalStatus(s string) bool { return livestatus.IsTerminal(s) }

// summarize derives one JobSummary, mirroring mcpserver.buildStatus precedence:
// run file present -> livestatus.Derive(run.Status,...); run file absent ->
// registry status (terminal surfaced; running -> blind with StartedAt staleness;
// queued -> queued).
func summarize(rec model.JobRecord, now time.Time) JobSummary {
	js := JobSummary{
		JobID: rec.JobID, Mode: string(rec.Mode), CWD: rec.CWD,
		WorktreePath: rec.WorktreePath, Branch: rec.Branch, RunID: rec.RunID,
		StartedAt: rec.StartedAt, ErrorCode: rec.ErrorCode, ErrorMessage: rec.ErrorMessage,
	}
	worktreeActive := rec.Mode == model.ModeWrite && WorktreeActive(rec.WorktreePath, now)

	run, err := readRun(rec.RunsDir, rec.RunID)
	if err != nil || run == nil {
		// On-disk run file gone: fall back to the snapshot persisted at terminal
		// time (populated only on the on-demand detail read; nil in the list path).
		run = decodeSnapshot(rec.RunSnapshot)
	}
	if run == nil {
		// No run file and no snapshot. Surface registry status.
		switch rec.Status {
		case model.JobQueued:
			js.Status = "queued"
		case model.JobRunning:
			if !worktreeActive && now.Sub(rec.StartedAt) > config.StaleThreshold {
				js.Status = "failed"
				if js.ErrorCode == "" {
					js.ErrorCode = config.ErrServerRestarted
				}
			} else {
				js.Status = "running"
				js.BlindWindow = true
			}
		default: // terminal
			js.Status = string(rec.Status)
		}
		return js
	}

	// Run file present.
	js.RunID = run.RunID
	js.WorkflowName = run.WorkflowName
	if run.CurrentPhase != nil {
		js.Phase = *run.CurrentPhase
	}
	js.Status = livestatus.Derive(run.Status, run.UpdatedAt, now, true, worktreeActive)
	js.AgentsTotal = len(run.Agents)
	for i := range run.Agents {
		if run.Agents[i].Status == "done" {
			js.AgentsDone++
		}
		js.LiveTokens += run.Agents[i].Tokens
	}
	fleet := runstore.ModelHistogram(run)
	if len(fleet) > 0 {
		js.FleetByModel = fleet
	}
	js.CompletedAt = run.CompletedAt
	js.DurationMs = run.DurationMs
	if run.TokenUsage != nil {
		c := run.TokenUsage.Cost
		js.Cost = &c
	}
	return js
}

// BuildDetail derives the heavy per-job view. ok is false only for an unknown
// record; a blind-window job (no run file yet) still returns ok with a
// summary-only detail.
func BuildDetail(rec model.JobRecord, now time.Time) (JobDetail, bool) {
	d := JobDetail{JobSummary: summarize(rec, now), Agents: []AgentView{}, Intermediate: []model.IntermediateResult{}}
	run, err := readRun(rec.RunsDir, rec.RunID)
	if err != nil || run == nil {
		// On-disk run file gone: render from the snapshot persisted at terminal time.
		run = decodeSnapshot(rec.RunSnapshot)
	}
	if run == nil {
		if d.BlindWindow {
			if a, ok := runstore.ReadAuthoring(rec.RunsDir, rec.JobID); ok {
				d.Authoring = a
			}
		}
		return d, true // blind / no run file / no snapshot: summary (+authoring) only
	}
	d.Phases = run.Phases
	d.Agents = make([]AgentView, 0, len(run.Agents))
	for i := range run.Agents {
		a := run.Agents[i]
		av := AgentView{
			Label: a.Label, Model: a.Model, Phase: a.Phase, Status: a.Status,
			Tokens: a.Tokens, StartedAt: a.StartedAt, EndedAt: a.EndedAt,
			Prompt: a.Prompt, ResultPreview: a.ResultPreview,
		}
		if a.Error != nil {
			av.Error = *a.Error
		}
		d.Agents = append(d.Agents, av)
	}
	d.Intermediate = runstore.Intermediates(run, config.MaxInlineResultBytes)
	d.TokenUsage = run.TokenUsage
	if livestatus.IsTerminal(d.Status) && d.Status == "completed" {
		d.Result = runstore.RawToAny(run.Result)
	}
	return d, true
}

// jsonMarshal is a thin wrapper so server.go need not import encoding/json
// directly for one call.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// decodeSnapshot decodes a persisted run snapshot (model.JobRecord.RunSnapshot —
// the run-file JSON pi-mcp captured at terminal time) into a *model.Run, or nil
// when absent or invalid. This is what lets the dashboard render a terminal job
// whose on-disk run file has since been removed (temp cwd cleaned / worktree pruned).
func decodeSnapshot(b []byte) *model.Run {
	if len(b) == 0 {
		return nil
	}
	var run model.Run
	if json.Unmarshal(b, &run) != nil {
		return nil
	}
	return &run
}
