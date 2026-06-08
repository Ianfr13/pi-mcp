// Package model defines the wire/domain types for pi-mcp: the pi run-file
// schema (spec §7), the pi --mode json stream events (§7), the MCP tool
// input/output structs (§5), and the persisted job registry record (§8).
//
// Rules (spec §7): optional run-file fields are POINTERS so absence is
// distinguishable from zero; arbitrary workflow output (run .result and
// journal[].result) is json.RawMessage (shape is workflow-defined); Cost is
// float64 read VERBATIM and never recomputed.
package model

import (
	"encoding/json"
	"time"
)

// ---------- pi run file: <launch-cwd>/.pi/workflows/runs/<runId>.json ----------

// Run is the on-disk pi workflow run file. runstore owns the canonical .Result
// when Status=="completed"; the stream parser is only an early/live fallback.
type Run struct {
	RunID        string          `json:"runId"`
	SessionID    string          `json:"sessionId"`
	WorkflowName string          `json:"workflowName"`
	Status       string          `json:"status"` // running|paused|completed|failed|aborted (disk vocabulary)
	CurrentPhase *string         `json:"currentPhase,omitempty"`
	Phases       []string        `json:"phases,omitempty"`
	Agents       []Agent         `json:"agents"`
	Journal      []JournalEntry  `json:"journal"`
	Logs         []string        `json:"logs,omitempty"`
	Script       string          `json:"script,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"` // workflow-defined shape; authoritative when completed
	StartedAt    *time.Time      `json:"startedAt,omitempty"`
	CompletedAt  *time.Time      `json:"completedAt,omitempty"` // omitted while running/failed
	UpdatedAt    *time.Time      `json:"updatedAt,omitempty"`   // liveness/staleness clock
	DurationMs   *int64          `json:"durationMs,omitempty"`  // only at end
	TokenUsage   *TokenUsage     `json:"tokenUsage,omitempty"`  // only at end
	Args         json.RawMessage `json:"args,omitempty"`        // null in headless; TASK is NOT here
}

// Agent is one entry of run.agents[]. id == callIndex+1. Join to journal by
// CallIndex == JournalEntry.Index (NEVER by array position, NEVER by id).
type Agent struct {
	ID            int        `json:"id"`        // == CallIndex+1
	CallIndex     int        `json:"callIndex"` // join key
	Label         string     `json:"label"`
	Model         string     `json:"model"`
	Phase         string     `json:"phase"`
	Prompt        string     `json:"prompt"`
	Status        string     `json:"status"`        // done|running|error|...
	ResultPreview string     `json:"resultPreview"` // truncated preview only
	Tokens        int64      `json:"tokens"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	Error         *string    `json:"error,omitempty"` // present on agent failure (§9 extraction order #2)
}

// JournalEntry is one entry of run.journal[]. Index == Agent.CallIndex.
// Result is the COMPLETE per-agent output (not a preview). Journal is ordered
// by COMPLETION, not by index (e.g. 0,2,1,3 in the canonical fixture).
type JournalEntry struct {
	Index  int             `json:"index"` // == Agent.CallIndex
	Hash   string          `json:"hash"`
	Result json.RawMessage `json:"result"` // complete per-agent result, workflow-defined shape
}

// TokenUsage is run.tokenUsage (present only at end). Cost is verbatim float64.
type TokenUsage struct {
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	Total      int64   `json:"total"`
	Cost       float64 `json:"cost"` // VERBATIM, never recomputed
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
}

// ---------- pi --mode json stream events (§7) ----------
// Line-delimited JSON; switch on .Type; ignore unknown types.

// StreamEvent is the generic envelope for one JSONL line.
type StreamEvent struct {
	Type    string `json:"type"`              // session|agent_start|agent_end|turn_start|turn_end|message_start|message_end|tool_execution_start|tool_execution_end|...
	ID      string `json:"id,omitempty"`      // on type=="session": == run.sessionId
	Version int    `json:"version,omitempty"` // session events
	CWD     string `json:"cwd,omitempty"`     // session events
	// tool_execution_* fields:
	ToolName   string          `json:"toolName,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	Result     *ToolResult     `json:"result,omitempty"`  // on tool_execution_end
	Args       json.RawMessage `json:"args,omitempty"`    // on tool_execution_start (workflow script etc.)
	Message    json.RawMessage `json:"message,omitempty"` // message_start/message_end payload (unparsed here)
}

// ToolResult is tool_execution_end(toolName=="workflow").result. The workflow
// final result lives in Content[0].Text as: header line + a ```json``` block.
// NOTE (validated §7): it does NOT carry details.result/details.runId — extract
// from Content[0].Text by stripping the "✓ Workflow ... finished" header and
// parsing the fenced json block (fallback: raw text).
type ToolResult struct {
	Content []ContentBlock  `json:"content"`
	Details json.RawMessage `json:"details,omitempty"`
}

// ContentBlock is one block of a ToolResult.Content / message content.
type ContentBlock struct {
	Type string `json:"type"` // text|thinking|toolCall|...
	Text string `json:"text,omitempty"`
}

// ---------- job registry record, persisted to disk (§8) ----------

// JobStatus is the pi-mcp-level job lifecycle status surfaced to MCP clients.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobAborted   JobStatus = "aborted"
)

// JobMode is the isolation+delivery mode (NOT a capability restriction, §4.1).
type JobMode string

const (
	ModeRead  JobMode = "read"
	ModeWrite JobMode = "write"
)

// JobRecord is persisted (jobId -> record) so write-job runs (inside the
// worktree) can be located and jobs recovered after a restart (§8).
type JobRecord struct {
	JobID        string    `json:"jobId"`     // UUIDv4; registry key + write branch name
	RunID        string    `json:"runId"`     // correlated via sessionId; "" until known
	SessionID    string    `json:"sessionId"` // from 1st stream "session" event
	Mode         JobMode   `json:"mode"`
	CWD          string    `json:"cwd"`                    // resolved absolute launch cwd
	RunsDir      string    `json:"runsDir"`                // <cwd|worktree>/.pi/workflows/runs
	WorktreePath string    `json:"worktreePath,omitempty"` // iff write
	Branch       string    `json:"branch,omitempty"`       // iff write: pi-mcp/job-<jobId>
	PID          int       `json:"pid"`                    // os process pid (liveness, same session only)
	Status       JobStatus `json:"status"`
	StartedAt    time.Time `json:"startedAt"`
	ErrorCode    string    `json:"errorCode,omitempty"` // one of config error-code consts
	ErrorMessage string    `json:"errorMessage,omitempty"`
}

// ---------- MCP tool I/O structs (§5) ----------
// Field json names ARE the wire contract. jsonschema tags become the schema
// descriptions consumed by mcp.AddTool reflection.

// --- pi_workflow (§5.1) ---

type WorkflowInput struct {
	Task    string `json:"task" jsonschema:"the task to delegate to the pi dynamic-workflow engine (required)"`
	Mode    string `json:"mode" jsonschema:"isolation+delivery mode: read (in-cwd) or write (git worktree) (required)"`
	CWD     string `json:"cwd" jsonschema:"absolute existing path; rejects '..'; symlinks resolved; never the server cwd (required)"`
	Context string `json:"context,omitempty" jsonschema:"optional extra context appended to the prompt"`
}

type WorkflowOutput struct {
	JobID        string `json:"jobId" jsonschema:"server UUIDv4; registry key and write-branch name"`
	Status       string `json:"status" jsonschema:"queued or running"`
	Mode         string `json:"mode" jsonschema:"read or write"`
	CWD          string `json:"cwd"`
	WorktreePath string `json:"worktree_path,omitempty" jsonschema:"present iff mode=write"`
	StartedAt    string `json:"started_at" jsonschema:"RFC3339 UTC"`
}

// --- pi_status (§5.2) ---

type StatusInput struct {
	JobID string `json:"jobId,omitempty" jsonschema:"job id from pi_workflow"`
	RunID string `json:"runId,omitempty" jsonschema:"alternative: external/post-restart run id (requires cwd)"`
	CWD   string `json:"cwd,omitempty" jsonschema:"required when querying by runId; resolves the runs dir"`
	Wait  bool   `json:"wait,omitempty" jsonschema:"long-poll until a terminal/new-agent/new-phase change (cap 60s)"`
}

// IntermediateResult is one completed agent surfaced live (join index==callIndex).
// Result is `any` (NOT json.RawMessage) because go-sdk v1.0.0 reflects
// json.RawMessage as schema type "null|array" and VALIDATES outgoing tool
// output, which rejects a JSON object. `any` reflects to an unconstrained schema
// so arbitrary workflow-defined JSON (object/array/scalar) passes validation.
type IntermediateResult struct {
	Label     string `json:"label"`
	Model     string `json:"model"`
	Phase     string `json:"phase"`
	Result    any    `json:"result,omitempty" jsonschema:"the agent's full result as arbitrary JSON (object, array, or scalar); preview field is used instead when truncated"` // full journal result; or preview if truncated
	Preview   string `json:"resultPreview,omitempty"`                                                                                                                          // present when Truncated
	Truncated bool   `json:"truncated"`
}

// StatusMetadata mirrors §5.2 metadata; cost/tokens only at end.
type StatusMetadata struct {
	ByModel    map[string]int `json:"by_model"`
	AgentCount int            `json:"agentCount"`
	TokenUsage *TokenUsage    `json:"tokenUsage,omitempty"`
	DurationMs *int64         `json:"durationMs,omitempty"`
}

// Progress is a heartbeat/liveness signal for a still-running job: how long it
// has been running and (for write jobs) how much it has written and how recently.
// It lets callers distinguish a slow-but-working job from a wedged one instead of
// staring at an opaque "running" — and keeps a directly-editing write job from
// looking dead when its run file goes stale.
type Progress struct {
	ElapsedSeconds      int64  `json:"elapsed_seconds"`                 // seconds since the job started
	WorktreeFiles       int    `json:"worktree_files,omitempty"`        // write mode: agent-written files present in the worktree
	LastActivitySeconds *int64 `json:"last_activity_seconds,omitempty"` // write mode: age of the newest worktree change (small = actively working)
}

// WriteInfo is the write-mode delivery block (§4.1/§5.2), present iff write.
type WriteInfo struct {
	Branch       string   `json:"branch"`
	WorktreePath string   `json:"worktree_path"`
	DiffStat     string   `json:"diff_stat"`
	FilesChanged []string `json:"files_changed"`
}

type StatusOutput struct {
	JobID        string               `json:"jobId"`
	RunID        *string              `json:"runId"`                                                                                                                               // null during blind window
	Status       string               `json:"status"`                                                                                                                              // queued|running|completed|failed|aborted (mapped)
	Phase        *string              `json:"phase"`                                                                                                                               // == run.currentPhase; null if unknown
	BlindWindow  bool                 `json:"blind_window"`                                                                                                                        // true while run file does not yet exist
	Intermediate []IntermediateResult `json:"intermediate"`                                                                                                                        // grows each poll
	Result       any                  `json:"result,omitempty" jsonschema:"the synthesized workflow result as arbitrary JSON, coerced to the §5.4 contract object when completed"` // any + jsonschema tag => object schema ({description}) that accepts any JSON and validates in strict MCP clients (Claude Code)
	Metadata     *StatusMetadata      `json:"metadata,omitempty"`
	Write        *WriteInfo           `json:"write,omitempty"`    // iff write
	Progress     *Progress            `json:"progress,omitempty"` // heartbeat for non-terminal jobs (elapsed + worktree activity)
	Error        string               `json:"error,omitempty"`    // failed/aborted message
}

// --- pi_list (§5.3) ---

type ListInput struct {
	CWD   string `json:"cwd" jsonschema:"required; resolves the runs dir; for write jobs use the worktree_path"`
	Limit int    `json:"limit,omitempty" jsonschema:"max rows (default 20)"`
}

type ListItem struct {
	RunID        string         `json:"runId"`
	WorkflowName string         `json:"workflowName"`
	Status       string         `json:"status"`
	AgentCount   int            `json:"agentCount"`
	ByModel      map[string]int `json:"by_model"`
	Cost         *float64       `json:"cost,omitempty"` // verbatim; nil until end
	DurationMs   *int64         `json:"durationMs,omitempty"`
	CompletedAt  *time.Time     `json:"completedAt,omitempty"`
}

type ListOutput struct {
	Runs []ListItem `json:"runs"`
}

// --- pi_cancel (§5.5) ---

type CancelInput struct {
	JobID string `json:"jobId" jsonschema:"job id to cancel (required)"`
}

type CancelOutput struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"` // aborted
}
