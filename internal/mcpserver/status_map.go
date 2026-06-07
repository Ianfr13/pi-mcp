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
func liveStatus(disk string, updatedAt *time.Time, now time.Time, pidAlive bool) string {
	mcp := mapDiskStatus(disk)
	if isTerminal(mcp) {
		return mcp
	}
	if !pidAlive {
		return "failed"
	}
	if updatedAt != nil && now.Sub(*updatedAt) > config.StaleThreshold {
		return "failed"
	}
	return mcp
}
