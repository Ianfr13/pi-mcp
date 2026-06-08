package mcpserver

import (
	"time"

	"pi-mcp/internal/livestatus"
)

// mapDiskStatus maps run-file status -> MCP status. paused (non-terminal) -> running.
func mapDiskStatus(disk string) string { return livestatus.MapDisk(disk) }

func isTerminal(mcpStatus string) bool { return livestatus.IsTerminal(mcpStatus) }

// liveStatus applies the §5.2 liveness override. See livestatus.Derive.
func liveStatus(disk string, updatedAt *time.Time, now time.Time, pidAlive, worktreeActive bool) string {
	return livestatus.Derive(disk, updatedAt, now, pidAlive, worktreeActive)
}
