package config

import (
	"strings"
	"testing"
	"time"
)

func TestConstants(t *testing.T) {
	if DefaultConcurrencyCap != 4 {
		t.Errorf("cap = %d, want 4", DefaultConcurrencyCap)
	}
	if StaleThreshold != 30*time.Minute {
		t.Errorf("StaleThreshold = %v, want 30m", StaleThreshold)
	}
	if ForcedAgentTimeoutMs != 1_200_000 {
		t.Errorf("ForcedAgentTimeoutMs = %d, want 1200000", ForcedAgentTimeoutMs)
	}
	if StaleThreshold <= time.Duration(ForcedAgentTimeoutMs)*time.Millisecond {
		t.Errorf("StaleThreshold (%v) must exceed the injected agent timeout (%dms)", StaleThreshold, ForcedAgentTimeoutMs)
	}
	if DefaultAgentTimeoutMs != 300000 {
		t.Errorf("DefaultAgentTimeoutMs = %d", DefaultAgentTimeoutMs)
	}
	if WaitCap != 60*time.Second {
		t.Errorf("WaitCap = %v, want 60s", WaitCap)
	}
	if MaxAuthoringRetries != 2 {
		t.Errorf("MaxAuthoringRetries = %d, want 2", MaxAuthoringRetries)
	}
}

func TestPiInvocation(t *testing.T) {
	if PiBinary != "pi" {
		t.Errorf("PiBinary = %q", PiBinary)
	}
	want := []string{"-p", "--mode", "json", "--no-session"}
	if strings.Join(PiBaseFlags, " ") != strings.Join(want, " ") {
		t.Errorf("PiBaseFlags = %v, want %v", PiBaseFlags, want)
	}
	if NoContextFilesFlag != "--no-context-files" {
		t.Errorf("NoContextFilesFlag = %q", NoContextFilesFlag)
	}
}

func TestForcingPromptTemplate(t *testing.T) {
	// Must contain the load-bearing constraints and the placeholders.
	for _, sub := range []string{
		"exactly ONE call",
		"`workflow`",
		"background:false",
		"Do not use background:true",
		"INLINE",
		"tokenBudget", // orchestrator must NOT cap the run by tokens (avoids TOKEN_BUDGET_EXHAUSTED)
		"{{CONTRACT}}",
		"{{TASK}}",
		"{{CONTEXT}}",
	} {
		if !strings.Contains(ForcingPromptTemplate, sub) {
			t.Errorf("ForcingPromptTemplate missing %q", sub)
		}
	}
	// Contracts must be non-empty and JSON-ish skeletons.
	if !strings.Contains(ReadContractSkeleton, "findings") {
		t.Errorf("ReadContractSkeleton = %q", ReadContractSkeleton)
	}
	if !strings.Contains(WriteContractSkeleton, "files_changed") {
		t.Errorf("WriteContractSkeleton = %q", WriteContractSkeleton)
	}
}

func TestErrorCodes(t *testing.T) {
	if ErrModelRoutingError != "MODEL_ROUTING_ERROR" {
		t.Errorf("ErrModelRoutingError = %q", ErrModelRoutingError)
	}
	if ErrNotAGitRepo != "NOT_A_GIT_REPO" {
		t.Errorf("ErrNotAGitRepo = %q", ErrNotAGitRepo)
	}
	if ErrNoWorkflowRun != "NO_WORKFLOW_RUN" {
		t.Errorf("ErrNoWorkflowRun = %q", ErrNoWorkflowRun)
	}
}

// TestAllErrorCodes pins every §9 error-code string const the contract requires.
func TestAllErrorCodes(t *testing.T) {
	cases := map[string]string{
		ErrModelRoutingError:     "MODEL_ROUTING_ERROR",
		ErrAgentTimeout:          "AGENT_TIMEOUT",
		ErrAgentExecutionError:   "AGENT_EXECUTION_ERROR",
		ErrWorkflowAborted:       "WORKFLOW_ABORTED",
		ErrAgentLimitExceeded:    "AGENT_LIMIT_EXCEEDED",
		ErrTokenBudgetExhausted:  "TOKEN_BUDGET_EXHAUSTED",
		ErrScriptValidationError: "SCRIPT_VALIDATION_ERROR",
		ErrSchemaNoncompliance:   "SCHEMA_NONCOMPLIANCE",
		ErrPersistenceError:      "PERSISTENCE_ERROR",
		ErrUnknown:               "UNKNOWN",
		ErrServerRestarted:       "SERVER_RESTARTED",
		ErrNotAGitRepo:           "NOT_A_GIT_REPO",
		ErrNoWorkflowRun:         "NO_WORKFLOW_RUN",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("error code const = %q, want %q", got, want)
		}
	}
}

func TestStateDir_XDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got := StateDir(); got != "/xdg/state" {
		t.Errorf("StateDir()=%q want /xdg/state", got)
	}
}

func TestStateDir_HomeFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	want := "/home/u/.local/state"
	if got := StateDir(); got != want {
		t.Errorf("StateDir()=%q want %q", got, want)
	}
}

func TestRegistryPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	want := "/xdg/state/pi-mcp/registry.db"
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath()=%q want %q", got, want)
	}
}
