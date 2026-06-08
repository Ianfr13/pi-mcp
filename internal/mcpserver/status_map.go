package mcpserver

import (
	"time"

	"pi-mcp/internal/config"
)

// mapDiskStatus maps run-file status -> MCP status. paused (non-terminal) -> running.
func mapDiskStatus(disk string) string {
	switch disk {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "aborted":
		return "aborted"
	default: // running, paused, or any unknown non-terminal
		return "running"
	}
}

func isTerminal(mcpStatus string) bool {
	switch mcpStatus {
	case "completed", "failed", "aborted":
		return true
	default:
		return false
	}
}

// liveStatus applies the §5.2 liveness override: a non-terminal status whose updatedAt is
// older than StaleThreshold, OR whose process PID is dead (same session), becomes "failed".
// pidAlive==true means "unknown or confirmed alive" (callers pass true when liveness is N/A,
// e.g. runId path with no owning job).
//
// worktreeActive overrides run-file staleness: a write-mode job that edits its
// worktree directly leaves the run file frozen (no fleet to bump updatedAt) while
// it is alive and progressing. A recently-modified worktree is direct evidence of
// liveness, so it keeps the job non-terminal even when the run file is stale. A
// confirmed-dead PID still wins (the process is gone regardless of file mtimes).
func liveStatus(disk string, updatedAt *time.Time, now time.Time, pidAlive, worktreeActive bool) string {
	mcp := mapDiskStatus(disk)
	if isTerminal(mcp) {
		return mcp
	}
	if !pidAlive {
		return "failed"
	}
	if worktreeActive {
		return mcp
	}
	if updatedAt != nil && now.Sub(*updatedAt) > config.StaleThreshold {
		return "failed"
	}
	return mcp
}
