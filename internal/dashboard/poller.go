package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"pi-mcp/internal/model"
)

// Sink receives serialized state snapshots (implemented by *Hub).
type Sink interface {
	Broadcast([]byte)
}

// Poller rebuilds the light DashboardState on an interval, caches the latest
// snapshot for one-shot reads (/api/state, new SSE clients), and broadcasts to
// the Sink only when the snapshot changed (the hash excludes the wall clock, so
// an idle fleet does not push every second).
type Poller struct {
	registryPath string
	stateDir     string
	sink         Sink
	interval     time.Duration

	now          func() time.Time
	readRegistry func(string) ([]model.JobRecord, error)

	mu       sync.Mutex
	latest   DashboardState
	lastHash [32]byte
	primed   bool
}

// NewPoller builds a Poller with production defaults (1s interval, real readers).
func NewPoller(registryPath, stateDir string, sink Sink) *Poller {
	return &Poller{
		registryPath: registryPath,
		stateDir:     stateDir,
		sink:         sink,
		interval:     time.Second,
		now:          time.Now,
		readRegistry: ReadRegistry,
	}
}

// Run ticks until ctx is done. The first tick happens immediately.
func (p *Poller) Run(ctx context.Context) {
	p.Tick()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Tick()
		}
	}
}

// Tick rebuilds the snapshot and broadcasts if it changed. A registry read error
// keeps the last good snapshot (never blanks the UI).
func (p *Poller) Tick() {
	recs, err := p.readRegistry(p.registryPath)
	if err != nil {
		return // keep last good state
	}
	st := BuildState(recs, p.stateDir, p.now())

	// Hash everything except the wall clock so identical fleets do not push.
	hashable := st
	hashable.GeneratedAt = time.Time{}
	b, _ := json.Marshal(hashable)
	sum := sha256.Sum256(b)

	full, _ := json.Marshal(st)

	p.mu.Lock()
	p.latest = st
	changed := !p.primed || sum != p.lastHash
	p.lastHash = sum
	p.primed = true
	p.mu.Unlock()

	if changed {
		p.sink.Broadcast(full)
	}
}

// Latest returns the most recent snapshot (for /api/state and new SSE clients).
func (p *Poller) Latest() DashboardState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest
}

// LatestJSON returns the most recent snapshot already serialized.
func (p *Poller) LatestJSON() []byte {
	b, _ := json.Marshal(p.Latest())
	return b
}
