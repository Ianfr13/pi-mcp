// Package watch is a thin fsnotify wrapper: subscribe to a directory, receive
// coalesced change notifications. INVARIANT (spec 2026-06-11): events are
// HINTS, never correctness — every consumer re-reads authoritative state on
// wake and keeps a fallback ticker, so a dropped/missed event costs one
// fallback interval of latency, never a wrong answer.
package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce coalesces bursts (pi rewrites the run file frequently) into one
// notification per quiet gap.
const debounce = 50 * time.Millisecond

// Subscribe watches dir and returns a channel signaled after any
// create/write/rename/remove at or under it. When dir does not exist yet
// (late-born runsDir: pi creates it after launch), the nearest existing
// ancestor is watched and the precise watch is armed when dir appears.
// cancel is idempotent. A non-nil error means nothing could be watched at
// all; the caller stays on its fallback ticker.
func Subscribe(dir string) (<-chan struct{}, func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	target := filepath.Clean(dir)
	if err := addNearest(w, target); err != nil {
		_ = w.Close()
		return nil, nil, err
	}

	ch := make(chan struct{}, 1)
	done := make(chan struct{})
	go run(w, target, ch, done)

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			_ = w.Close()
		})
	}
	return ch, cancel, nil
}

// run owns the watcher goroutine: filter to the target subtree, debounce,
// re-arm the precise watch when an ancestor event creates the target.
func run(w *fsnotify.Watcher, target string, ch chan struct{}, done chan struct{}) {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-done:
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			// Re-arm on EVERY create: fsnotify is NOT recursive, so when the
			// target is late-born behind nested missing dirs (parent/a/b/runs),
			// each intermediate mkdir is visible only on the currently-watched
			// ancestor. addNearest walks the watch as deep as currently
			// possible — by the time the target exists it is watched directly.
			// Cheap no-op once armed; the consumer's fallback ticker covers
			// any residual race window (events-are-hints invariant).
			if ev.Op.Has(fsnotify.Create) {
				_ = addNearest(w, target)
				// If the target now exists, emit an immediate hint so the
				// consumer re-reads state. This covers the race where files
				// were written to the target before the watch was re-armed
				// (fsnotify does not deliver retroactive events).
				if _, err := os.Stat(target); err == nil {
					notify(ch)
				}
				// A create on the path TOWARD the target is itself a useful
				// hint (e.g. the runs dir appearing): fall through to within().
			}
			if !within(target, ev.Name) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			}
		case <-timerC:
			timer, timerC = nil, nil
			notify(ch)
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
			// Overflow/transient errors are survivable: emit a hint so the
			// consumer reconciles immediately instead of waiting for fallback.
			notify(ch)
		}
	}
}

// notify is the non-blocking buffered(1) send: a pending hint is enough.
func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// addNearest watches target or, when absent, its nearest existing ancestor.
// After finding the deepest existing ancestor, it walks back down toward
// target adding any subdirectories that now exist, so missed intermediate
// creation events do not leave the target unwatched.
func addNearest(w *fsnotify.Watcher, target string) error {
	p := target
	for {
		if err := w.Add(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return w.Add(p) // hit the root and even that failed: report it
		}
		p = parent
	}
	// Walk back down toward target, adding each existing subdirectory.
	// This repairs the race where an intermediate dir was created before
	// its parent was watched, so the creation event was missed.
	for p != target {
		rel, err := filepath.Rel(p, target)
		if err != nil || rel == "." {
			break
		}
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		p = filepath.Join(p, parts[0])
		if err := w.Add(p); err != nil {
			return nil // dir doesn't exist yet; we'll catch it when it appears
		}
	}
	return nil
}

// within reports whether name is target itself or inside its subtree.
func within(target, name string) bool {
	n := filepath.Clean(name)
	if n == target {
		return true
	}
	rel, err := filepath.Rel(target, n)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
