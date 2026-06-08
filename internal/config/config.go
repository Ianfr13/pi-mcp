// Package config holds pi-mcp's compile-time defaults and string contracts:
// the pi binary + invocation flags (§4), concurrency cap and liveness windows
// (§8), the EXACT forcing-prompt template + output-contract skeletons (§4.2/§5.4),
// and the §9 error-code string constants. Leaf package: no internal deps.
package config

import "time"

// ---- concurrency / liveness (§8) ----
const (
	// DefaultConcurrencyCap is the max simultaneous pi jobs (configurable).
	// Justified by provider rate limits, not a token budget (decision #12).
	DefaultConcurrencyCap = 4

	// DefaultAgentTimeoutMs is the engine's per-agent timeout (~5min). pi-mcp
	// does NOT impose its own job timeout (cancel-only); this mirrors the engine.
	DefaultAgentTimeoutMs int64 = 300000
)

// StaleThreshold: a disk status of "running" with UpdatedAt older than this is
// treated as crashed (post-restart liveness override). 300s == DefaultAgentTimeoutMs.
const StaleThreshold = 300 * time.Second

// MaxAuthoringRetries is how many EXTRA times pi-mcp relaunches pi when a job
// fails BEFORE any run file is created (the workflow never started — e.g. the
// orchestrator authored an invalid script). Retrying is cheap (the agent fleet
// never ran) and usually succeeds because authoring is non-deterministic. A
// failure AFTER a run file exists (the fleet ran) is NOT retried. 2 -> up to 3
// total attempts.
const MaxAuthoringRetries = 2

// WaitCap bounds pi_status long-poll (injectable for tests).
const WaitCap = 60 * time.Second

// ---- pi invocation contract (§4) ----
// Command: pi -p --mode json --no-session [--no-context-files] <PROMPT as single argv>
// stdin MUST be /dev/null; env = os.Environ() passthrough; cwd = project|worktree.
const (
	PiBinary           = "pi"
	NoContextFilesFlag = "--no-context-files"
)

// PiBaseFlags are always passed (the optional NoContextFilesFlag is appended by
// the runner based on config; default ON for code tasks per §4).
var PiBaseFlags = []string{"-p", "--mode", "json", "--no-session"}

// OrchestratorModel / OrchestratorThinking pin the model+effort of the pi agent
// that AUTHORS the workflow script. pi-mcp forces a strong author because a weak
// default (the user's ~/.pi defaultModel) can emit syntactically-invalid workflow
// JavaScript ("Unexpected token") that fails the whole run before any agent runs.
// This pins ONLY the orchestrator/author — pi still owns the FLEET's per-agent
// model selection via ~/.pi/workflows/model-tiers.json.
const (
	OrchestratorModel    = "openai-codex/gpt-5.5"
	OrchestratorThinking = "high"
)

// DefaultNoContextFiles: when true, the runner appends NoContextFilesFlag.
// Spec §4: default ON for code tasks (lets pi see AGENTS.md/CLAUDE.md). Note the
// flag literally DISABLES context files, so "default see project files" == do NOT
// pass it. Runner appends the flag only when this is false (hermetic delegation).
const DefaultNoContextFiles = false

// ---- worktree (§4.1) ----
const (
	// WorktreeBranchPrefix + jobID forms the fresh branch: pi-mcp/job-<jobId>.
	WorktreeBranchPrefix = "pi-mcp/job-"
	// WorktreeSubdir under $XDG_STATE_HOME (or temp): pi-mcp/worktrees/.
	WorktreeSubdir = "pi-mcp/worktrees"
)

// ---- runs dir layout (§7) ----
// Run files live at <launch-cwd>/.pi/workflows/runs/<runId>.json
const RunsDirRel = ".pi/workflows/runs"

// ---- result truncation (§5.2) ----
// Intermediate/journal results larger than this are returned as preview+truncated.
const MaxInlineResultBytes = 16 * 1024

// ---- forcing prompt (§4.2) ----
// Source of truth: docs/research/fixtures/sample-forced-workflow-prompt.txt.
// Placeholders: {{CONTRACT}} (read|write skeleton, §5.4), {{TASK}}, {{CONTEXT}}.
// The CONTEXT line/block is included only when context is non-empty (the runner
// trims the "[CONTEXT: ...]" section when {{CONTEXT}} would be empty).
const ForcingPromptTemplate = `You MUST make exactly ONE call to the ` + "`workflow`" + ` tool with background:false. Do not answer
directly. Do not use background:true. Return the final synthesized result INLINE this turn.
Decompose the task as you see fit and fan out subagents in parallel.
The workflow MUST return an object matching exactly this JSON shape:
{{CONTRACT}}

TASK:
{{TASK}}

[CONTEXT:
{{CONTEXT}}]`

// ReadContractSkeleton is the §5.4 read-mode default output shape (requested, not enforced).
const ReadContractSkeleton = `{ "summary": "string", "findings": [ { "title": "string", "detail": "string", "severity": "low|med|high" } ], "confidence": "string", "open_questions": ["string"] }`

// WriteContractSkeleton is the §5.4 write-mode default output shape.
const WriteContractSkeleton = `{ "summary": "string", "files_changed": ["string"], "diff_summary": "string", "tests_run": "string", "notes": "string" }`

// ---- error codes (§9) ----
// Engine WorkflowErrorCode values + pi-mcp-originated codes. String values match
// the engine vocabulary verbatim; surfaced in JobRecord.ErrorCode / StatusOutput.Error.
const (
	ErrModelRoutingError     = "MODEL_ROUTING_ERROR"   // tier/model not authenticated; strict config
	ErrAgentTimeout          = "AGENT_TIMEOUT"         // per-agent engine timeout
	ErrAgentExecutionError   = "AGENT_EXECUTION_ERROR" // agent run failure
	ErrWorkflowAborted       = "WORKFLOW_ABORTED"      // -> aborted
	ErrAgentLimitExceeded    = "AGENT_LIMIT_EXCEEDED"
	ErrTokenBudgetExhausted  = "TOKEN_BUDGET_EXHAUSTED"
	ErrScriptValidationError = "SCRIPT_VALIDATION_ERROR"
	ErrSchemaNoncompliance   = "SCHEMA_NONCOMPLIANCE"
	ErrPersistenceError      = "PERSISTENCE_ERROR"
	ErrUnknown               = "UNKNOWN"
	ErrServerRestarted       = "SERVER_RESTARTED" // job recovered after a restart but cannot be resumed (queued: Task/Context not persisted; running: process gone)

	// pi-mcp-originated (not engine codes):
	ErrNotAGitRepo   = "NOT_A_GIT_REPO"  // write cwd is not a git repo (fail before spawn)
	ErrNoWorkflowRun = "NO_WORKFLOW_RUN" // no tool_execution_end(workflow) — trigger not wired (no retry)
)
