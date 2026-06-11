// Package config holds pi-mcp's compile-time defaults and string contracts:
// the pi binary + invocation flags (§4), concurrency cap and liveness windows
// (§8), the EXACT forcing-prompt template + output-contract skeletons (§4.2/§5.4),
// and the §9 error-code string constants. Leaf package: no internal deps.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// ---- concurrency / liveness (§8) ----
const (
	// DefaultConcurrencyCap is the max simultaneous pi jobs (configurable).
	// Justified by provider rate limits, not a token budget (decision #12).
	DefaultConcurrencyCap = 4
)

// ForcedAgentTimeoutMs is the per-agent timeout pi-mcp injects into the forcing
// prompt (20 min) so coding/TDD agents are not killed by the 5-min default. The
// forcing-prompt template (ForcingPromptTemplate) hardcodes this literal as
// "1200000"; keep the two in sync. StaleThreshold MUST exceed this value.
const ForcedAgentTimeoutMs int64 = 1_200_000

// StaleThreshold: a non-terminal job whose updatedAt is older than this is
// treated as crashed (liveness override in livestatus.Derive + the dashboard
// blind-window path + the write-job worktree-activity window). It MUST exceed
// ForcedAgentTimeoutMs — a single healthy agent can run that long with a quiet
// run file, and must NOT be reported failed. Genuinely-dead jobs are reaped
// promptly by the periodic reconcile and the normal job lifecycle, so a generous
// threshold does not delay real failure detection. 20-min timeout + 10-min margin.
const StaleThreshold = time.Duration(ForcedAgentTimeoutMs)*time.Millisecond + 10*time.Minute

// EarlyInactivityWarn: a non-terminal run with no observed activity (run-file
// updatedAt, write-job worktree mtime) for this long wakes a pi_status wait
// ONCE per activity epoch, carrying the progress heartbeat — informational,
// the status stays running. Only StaleThreshold (~30min) flips the status to
// stalled. Must stay well under StaleThreshold and over typical agent bursts.
const EarlyInactivityWarn = 5 * time.Minute

// MaxAuthoringRetries is how many EXTRA times pi-mcp relaunches pi when a job
// fails BEFORE any run file is created (the workflow never started — e.g. the
// orchestrator authored an invalid script). Retrying is cheap (the agent fleet
// never ran) and usually succeeds because authoring is non-deterministic. A
// failure AFTER a run file exists (the fleet ran) is NOT retried. 2 -> up to 3
// total attempts.
const MaxAuthoringRetries = 2

// WaitCapDefault bounds the pi_status long-poll when PI_MCP_WAIT_CAP is unset.
// 5min (raised from 60s, spec 2026-06-11): with delta responses a quiet run
// costs one tiny round-trip per cap window, and event wakes (Phase 2) end the
// wait early on real change. DEPLOY PREREQUISITE: the MCP client's tool-call
// timeout must exceed this (Claude Code: MCP_TOOL_TIMEOUT) — verify before
// shipping; lower via env, never by rebuild.
const WaitCapDefault = 5 * time.Minute

// WaitCap returns the long-poll cap: PI_MCP_WAIT_CAP as a Go duration
// (e.g. "60s", "2m") when set and positive, else WaitCapDefault.
func WaitCap() time.Duration {
	if v := os.Getenv("PI_MCP_WAIT_CAP"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return WaitCapDefault
}

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

// ---- blind-window authoring preview (Task 1) ----
// MaxAuthoringPreviewBytes caps the live authoring preview written to
// <runsDir>/<jobID>.authoring. The watcher is in-memory only; only the latest
// tail is persisted, so a 6 KiB cap is plenty for a "what is the orchestrator
// thinking?" hint while still leaving room for several KB of free-form text
// even after UTF-8 tail-truncation.
const MaxAuthoringPreviewBytes = 6 * 1024

// ---- forcing prompt (§4.2) ----
// Source of truth: docs/research/fixtures/sample-forced-workflow-prompt.txt.
// Placeholders: {{CONTRACT}} (read|write skeleton, §5.4), {{TASK}}, {{CONTEXT}}.
// The CONTEXT line/block is included only when context is non-empty (the runner
// trims the "[CONTEXT: ...]" section when {{CONTEXT}} would be empty).
const ForcingPromptTemplate = `You MUST make exactly ONE call to the ` + "`workflow`" + ` tool with background:false. Do not answer
directly. Do not use background:true. Return the final synthesized result INLINE this turn.
Decompose the task as you see fit and fan out subagents in parallel.
Run the workflow to completion without throttling: pass tokenBudget:2000000000 (effectively
unlimited) to the workflow tool and do NOT set per-phase budgets — never stop or throttle the run
for token or cost reasons. Pass the large value EXPLICITLY; do not rely on omitting it.
Give agents room to finish: pass agentTimeoutMs:1200000 (20 minutes) to the workflow tool so
coding/TDD agents are not killed by the 5-minute default per-agent timeout. Do NOT set a
per-agent timeoutMs on any agent() call — it overrides agentTimeoutMs and re-introduces the
5-minute kill; rely solely on the single agentTimeoutMs above.
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

// ---- shared state-path resolution (Task 2) ----
// StateDir resolves pi-mcp's state base dir: $XDG_STATE_HOME, else
// $HOME/.local/state, else the OS temp dir. This is the single source of truth
// shared by the MCP server (internal/app) and the dashboard.
func StateDir() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return xdg
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state")
	}
	return os.TempDir()
}

// RegistryPathFor returns the SQLite registry DB path under an explicit state
// dir: <stateDir>/pi-mcp/registry.db. Any caller honoring a custom --state-dir
// MUST use this — never hand-build "registry.json" (the canonical registry is
// the .db; a .json path yields an empty/split-brain reader).
func RegistryPathFor(stateDir string) string {
	return filepath.Join(stateDir, "pi-mcp", "registry.db")
}

// RegistryPath is the SQLite job-registry DB under the default state dir.
func RegistryPath() string {
	return RegistryPathFor(StateDir())
}
