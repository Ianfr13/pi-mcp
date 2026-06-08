// Package livestatus is the single source of truth for mapping a pi run-file
// status to the MCP/dashboard status vocabulary and applying the liveness
// override (staleness + worktree activity). Both internal/mcpserver and
// internal/dashboard use it so the two readers never drift.
package livestatus

import (
	"time"

	"pi-mcp/internal/config"
)

// MapDisk maps a run-file status to the surfaced status. paused (non-terminal)
// collapses to running.
func MapDisk(disk string) string {
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

// IsTerminal reports whether a mapped status is terminal.
func IsTerminal(mapped string) bool {
	switch mapped {
	case "completed", "failed", "aborted":
		return true
	default:
		return false
	}
}

// Derive applies the liveness override to a disk status. A non-terminal status
// whose updatedAt is older than config.StaleThreshold, OR whose process is
// confirmed dead, becomes "failed". A recently-active worktree overrides
// run-file staleness (a direct-editing write job freezes its run file while
// alive). A confirmed-dead pid still wins. pidAlive==true means "alive or
// unknown" (callers with no liveness signal pass true).
func Derive(disk string, updatedAt *time.Time, now time.Time, pidAlive, worktreeActive bool) string {
	mapped := MapDisk(disk)
	if IsTerminal(mapped) {
		return mapped
	}
	if !pidAlive {
		return "failed"
	}
	if worktreeActive {
		return mapped
	}
	if updatedAt != nil && now.Sub(*updatedAt) > config.StaleThreshold {
		return "failed"
	}
	return mapped
}
