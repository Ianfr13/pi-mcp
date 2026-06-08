package dashboard

import (
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
	p := NewPoller("testdata/registry.json", "/state", sink)
	p.now = func() time.Time { return nowFresh }

	p.Tick() // first build -> broadcast
	if sink.count() != 1 {
		t.Fatalf("first tick broadcasts once, got %d", sink.count())
	}
	p.Tick() // identical -> no broadcast
	if sink.count() != 1 {
		t.Errorf("unchanged tick must not broadcast, got %d", sink.count())
	}
	_ = recs
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
