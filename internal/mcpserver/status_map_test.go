package mcpserver

import (
	"testing"
	"time"

	"pi-mcp/internal/config"
)

func TestMapDiskStatus(t *testing.T) {
	tests := []struct {
		disk string
		want string
	}{
		{"running", "running"},
		{"paused", "running"}, // paused is non-terminal
		{"completed", "completed"},
		{"failed", "failed"},
		{"aborted", "aborted"},
		{"weird", "running"}, // unknown non-terminal -> running (live)
	}
	for _, tt := range tests {
		if got := mapDiskStatus(tt.disk); got != tt.want {
			t.Fatalf("mapDiskStatus(%q)=%q want %q", tt.disk, got, tt.want)
		}
	}
}

func TestStalenessOverride(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-(config.StaleThreshold + time.Second))

	// running + fresh updatedAt + pid alive => running
	if got := liveStatus("running", &fresh, now, true); got != "running" {
		t.Fatalf("fresh running want running got %q", got)
	}
	// running + stale updatedAt => failed
	if got := liveStatus("running", &stale, now, true); got != "failed" {
		t.Fatalf("stale running want failed got %q", got)
	}
	// running + pid dead (same session) => failed even if fresh
	if got := liveStatus("running", &fresh, now, false); got != "failed" {
		t.Fatalf("dead pid want failed got %q", got)
	}
	// terminal stays terminal regardless of staleness/pid
	if got := liveStatus("completed", &stale, now, false); got != "completed" {
		t.Fatalf("completed want completed got %q", got)
	}
	// nil updatedAt + alive => running (no staleness data)
	if got := liveStatus("running", nil, now, true); got != "running" {
		t.Fatalf("nil updatedAt alive want running got %q", got)
	}
}
