package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"pi-mcp/internal/model"
	"pi-mcp/internal/watch"
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

	// subscribe is the fsnotify seam. When nil, Run falls back to the ticker.
	subscribe func(dir string) (<-chan struct{}, func(), error)

	mu         sync.Mutex
	latest     DashboardState
	latestJSON []byte // serialized once per Tick; /api/state and new SSE clients reuse it
	lastHash   [32]byte
	primed     bool

	wmu  sync.Mutex        // guards subs (Tick is public: tests call it while Run is live)
	subs map[string]*subHandle // watched dir -> handle
	wake chan struct{}     // fan-in of all subscriptions
}

type subHandle struct {
	cancel func()
	done   chan struct{}
	once   sync.Once
}

func (h *subHandle) stop() {
	h.once.Do(func() {
		h.cancel()
		close(h.done)
	})
}

// NewPoller builds a Poller with production defaults (5s fallback ticker, real
// fsnotify subscriber, real readers).
func NewPoller(registryPath, stateDir string, sink Sink) *Poller {
	return &Poller{
		registryPath: registryPath,
		stateDir:     stateDir,
		sink:         sink,
		interval:     5 * time.Second,
		now:          time.Now,
		readRegistry: ReadRegistry,
		subscribe:    watch.Subscribe,
		subs:         map[string]*subHandle{},
		wake:         make(chan struct{}, 1),
	}
}

// Run ticks until ctx is done. fsnotify wakes (registry DB dir + active jobs'
// runs dirs) carry the latency; the 5s ticker reconciles dropped events. The
// first tick happens immediately.
func (p *Poller) Run(ctx context.Context) {
	p.Tick()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			p.refreshWatches(nil) // cancel everything
			return
		case <-t.C:
		case <-p.wake:
		}
		p.Tick()
	}
}

// refreshWatches reconciles the subscription set to EXACTLY want (plus the
// registry DB dir, always wanted while running): new dirs subscribe, dropped
// dirs cancel — a long-lived dashboard never accumulates stale watches.
// nil want cancels everything (shutdown). Guarded by wmu: Tick is public and
// tests call it while Run is live.
func (p *Poller) refreshWatches(want map[string]bool) {
	if p.subscribe == nil {
		return
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	for dir, h := range p.subs {
		if want == nil || !want[dir] {
			h.stop()
			delete(p.subs, dir)
		}
	}
	if want == nil {
		return
	}
	for dir := range want {
		if dir == "" {
			continue
		}
		if _, ok := p.subs[dir]; ok {
			continue
		}
		ch, cancel, err := p.subscribe(dir)
		if err != nil {
			continue // fallback ticker covers it; retried on the next Tick
		}
		done := make(chan struct{})
		h := &subHandle{cancel: cancel, done: done}
		p.subs[dir] = h
		go func() {
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
					select {
					case p.wake <- struct{}{}:
					default:
					}
				case <-done:
					return
				}
			}
		}()
	}
}

// Tick rebuilds the snapshot and broadcasts if it changed. A registry read error
// keeps the last good snapshot (never blanks the UI).
func (p *Poller) Tick() {
	recs, err := p.readRegistry(p.registryPath)
	if err != nil {
		return // keep last good state
	}

	want := map[string]bool{filepath.Dir(p.registryPath): true}
	for i := range recs {
		st := string(recs[i].Status)
		if st == "running" || st == "queued" {
			want[recs[i].RunsDir] = true
		}
	}
	p.refreshWatches(want)

	st := BuildState(recs, p.stateDir, p.now())

	// Hash everything except the wall clock so identical fleets do not push.
	hashable := st
	hashable.GeneratedAt = time.Time{}
	b, _ := json.Marshal(hashable)
	sum := sha256.Sum256(b)

	full, _ := json.Marshal(st)

	p.mu.Lock()
	p.latest = st
	p.latestJSON = full
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

// LatestJSON returns the most recent snapshot already serialized (the bytes
// Tick produced — no per-request re-marshal). Before the first successful
// Tick it falls back to marshaling the zero state.
func (p *Poller) LatestJSON() []byte {
	p.mu.Lock()
	b := p.latestJSON
	p.mu.Unlock()
	if b == nil {
		b, _ = json.Marshal(p.Latest())
	}
	return b
}
