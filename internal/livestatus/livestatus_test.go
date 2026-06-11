package livestatus

import (
	"testing"
	"time"

	"pi-mcp/internal/config"
)

func TestMapDisk(t *testing.T) {
	cases := map[string]string{
		"completed": "completed", "failed": "failed", "aborted": "aborted",
		"running": "running", "paused": "running", "weird": "running",
	}
	for disk, want := range cases {
		if got := MapDisk(disk); got != want {
			t.Errorf("MapDisk(%q)=%q want %q", disk, got, want)
		}
	}
}

func TestDerive(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-2 * config.StaleThreshold)

	// terminal disk status passes through.
	if got := Derive("completed", &fresh, now, true, false); got != "completed" {
		t.Errorf("completed -> %q", got)
	}
	// running + fresh + alive -> running.
	if got := Derive("running", &fresh, now, true, false); got != "running" {
		t.Errorf("fresh running -> %q want running", got)
	}
	// running + stale + alive -> stalled (NON-terminal: may resume).
	if got := Derive("running", &stale, now, true, false); got != "stalled" {
		t.Errorf("stale running -> %q want stalled", got)
	}
	// running + stale BUT worktree active -> running.
	if got := Derive("running", &stale, now, true, true); got != "running" {
		t.Errorf("stale+worktreeActive -> %q want running", got)
	}
	// dead pid wins over everything non-terminal.
	if got := Derive("running", &fresh, now, false, true); got != "failed" {
		t.Errorf("dead pid -> %q want failed", got)
	}
}

func TestDerive_LongRunningAgentNotStale(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	// A single agent can run up to the injected 20-min agentTimeoutMs with a quiet
	// run file. With a live (or unknown) pid and no worktree activity it MUST stay
	// running — not flip to "failed" — because StaleThreshold now exceeds 20 min.
	longRun := now.Add(-20 * time.Minute)
	if got := Derive("running", &longRun, now, true, false); got != "running" {
		t.Fatalf("20-min-old running agent -> %q want running (StaleThreshold must exceed the agent timeout)", got)
	}
}

func TestDerive_StaleAliveBecomesStalledNotFailed(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-(config.StaleThreshold + time.Minute))

	// pid alive (or unknown) + stale run file -> stalled (non-terminal, may resume)
	if got := Derive("running", &old, now, true, false); got != "stalled" {
		t.Fatalf("stale+alive: want stalled, got %q", got)
	}
	if IsTerminal("stalled") {
		t.Fatalf("stalled must be NON-terminal")
	}
	// confirmed-dead pid still wins -> failed
	if got := Derive("running", &old, now, false, false); got != "failed" {
		t.Fatalf("dead pid: want failed, got %q", got)
	}
	// active worktree still overrides staleness -> running
	if got := Derive("running", &old, now, true, true); got != "running" {
		t.Fatalf("active worktree: want running, got %q", got)
	}
}
