package mcpserver

import (
	"testing"
	"time"
)

func TestDeltaTracker_TakeAdvancesPerKey(t *testing.T) {
	d := newDeltaTracker()

	if got := d.take("job-a", 3, false); got != 0 {
		t.Fatalf("first call delivers from 0, got %d", got)
	}
	if got := d.take("job-a", 5, false); got != 3 {
		t.Fatalf("second call delivers from 3, got %d", got)
	}
	if got := d.take("job-b", 2, false); got != 0 {
		t.Fatalf("independent key starts at 0, got %d", got)
	}
	// from_start resets to 0 but still advances to the new length
	if got := d.take("job-a", 5, true); got != 0 {
		t.Fatalf("from_start re-delivers from 0, got %d", got)
	}
	if got := d.take("job-a", 5, false); got != 5 {
		t.Fatalf("after from_start, position is len(journal), got %d", got)
	}
	// journal shrank (authoring retry rewrote the run): start over, never panic
	if got := d.take("job-a", 1, false); got != 0 {
		t.Fatalf("shrunken journal resets to 0, got %d", got)
	}
}

func TestDeltaTracker_WarnOncePerActivityEpoch(t *testing.T) {
	d := newDeltaTracker()
	t0 := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	if !d.shouldWarn("job-a", t0) {
		t.Fatalf("first crossing must warn")
	}
	if d.shouldWarn("job-a", t0) {
		t.Fatalf("same activity epoch must NOT re-warn")
	}
	if !d.shouldWarn("job-a", t0.Add(time.Minute)) {
		t.Fatalf("advanced activity epoch re-arms the warning")
	}
	if d.shouldWarn("job-a", t0) {
		t.Fatalf("older epoch never re-warns")
	}
}
