package mcpserver

import (
	"sync"
	"time"
)

// deltaTracker remembers, per delta key, (a) how many journal positions have
// already been delivered to a pi_status caller and (b) the activity epoch of
// the last early-inactivity warning. In-memory ONLY and deliberately so:
// pi-mcp is a stdio server — one process per Claude Code session — so this is
// naturally per-session state. A server restart loses it and the next call
// re-delivers all events once (accepted in the spec). There is no
// client-managed cursor.
type deltaTracker struct {
	mu        sync.Mutex
	delivered map[string]int       // key -> journal positions already delivered
	warned    map[string]time.Time // key -> lastActivity epoch at warn time
}

func newDeltaTracker() *deltaTracker {
	return &deltaTracker{delivered: map[string]int{}, warned: map[string]time.Time{}}
}

// take returns the journal position to deliver from and advances the position
// to journalLen. fromStart resets to 0 first. A position beyond journalLen
// (journal shrank — e.g. an authoring retry rewrote the run) resets to 0
// rather than erroring: re-delivery is always safe, silence is not.
func (d *deltaTracker) take(key string, journalLen int, fromStart bool) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	from := d.delivered[key]
	if fromStart || from > journalLen {
		from = 0
	}
	d.delivered[key] = journalLen
	return from
}

// shouldWarn reports whether the early-inactivity warning should fire for this
// activity epoch, arming it as a side effect. It re-arms only when observed
// activity ADVANCES past the previously warned epoch, so each quiet stretch
// warns exactly once (per server lifetime; a restart may repeat one warning).
func (d *deltaTracker) shouldWarn(key string, lastActivity time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if prev, ok := d.warned[key]; ok && !lastActivity.After(prev) {
		return false
	}
	d.warned[key] = lastActivity
	return true
}

// deltaKey identifies the tracked job: jobID when owned, else runsDir+runID
// (the runId+cwd query path for external runs).
func deltaKey(tgt resolved) string {
	if tgt.jobID != "" {
		return tgt.jobID
	}
	return tgt.runsDir + "/" + tgt.runID
}
