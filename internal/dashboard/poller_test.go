package dashboard

import (
	"context"
	"sync"
	"testing"
	"time"

	"pi-mcp/internal/model"
)

type captureSink struct {
	mu sync.Mutex
	n  int
}

func (c *captureSink) Broadcast([]byte) { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *captureSink) count() int       { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func TestPoller_TickBroadcastsOnChange(t *testing.T) {
	recs := recsForTest()
	sink := &captureSink{}
	p := NewPoller("unused.db", "/state", sink)
	p.readRegistry = func(string) ([]model.JobRecord, error) { return recs, nil }
	p.now = func() time.Time { return nowFresh }

	p.Tick() // first build -> broadcast
	if sink.count() != 1 {
		t.Fatalf("first tick broadcasts once, got %d", sink.count())
	}
	p.Tick() // identical -> no broadcast
	if sink.count() != 1 {
		t.Errorf("unchanged tick must not broadcast, got %d", sink.count())
	}
}

func TestPoller_LatestState(t *testing.T) {
	p := NewPoller("testdata/registry.json", "/state", &captureSink{})
	// registry.json points runsDir at a placeholder; override the reader so the
	// builder sees our deterministic records.
	p.readRegistry = func(string) ([]model.JobRecord, error) { return recsForTest(), nil }
	p.now = func() time.Time { return nowFresh }
	p.Tick()
	got := p.Latest()
	if got.Counts.Total != 4 {
		t.Errorf("latest total=%d want 4", got.Counts.Total)
	}
}

// TestPoller_EventWakeTicksWithoutInterval: interval is 1h, so only the
// injected fsnotify wake can produce the second broadcast.
func TestPoller_EventWakeTicksWithoutInterval(t *testing.T) {
	sink := &captureSink{}
	p := NewPoller("unused.db", "/state", sink)
	p.interval = time.Hour
	wake := make(chan struct{}, 1)
	p.subscribe = func(string) (<-chan struct{}, func(), error) { return wake, func() {}, nil }

	calls := 0
	p.readRegistry = func(string) ([]model.JobRecord, error) {
		calls++
		recs := recsForTest()
		if calls > 1 {
			recs = recs[:len(recs)-1] // shrink the fleet so the woken tick broadcasts
		}
		return recs, nil
	}
	p.now = func() time.Time { return nowFresh }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitCount := func(n int, what string) {
		deadline := time.Now().Add(2 * time.Second)
		for sink.count() < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if sink.count() < n {
			t.Fatalf("%s: broadcasts=%d want >=%d", what, sink.count(), n)
		}
	}
	waitCount(1, "initial tick")
	wake <- struct{}{}
	waitCount(2, "event wake (interval is 1h)")
}
