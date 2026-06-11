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
// whose process is confirmed dead becomes "failed". A non-terminal status whose
// updatedAt is older than config.StaleThreshold (and whose worktree is idle)
// becomes "stalled" — NON-terminal: the run may resume, and callers (pi_status
// waits, dashboard) treat it as a wake/display signal, not an exit. A
// recently-active worktree overrides run-file staleness. pidAlive==true means
// "alive or unknown".
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
		return "stalled"
	}
	return mapped
}
