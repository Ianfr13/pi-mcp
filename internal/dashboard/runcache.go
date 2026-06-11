package dashboard

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"pi-mcp/internal/model"
)

// parsedRun aliases model.Run for the cache seam.
type parsedRun = model.Run

// runCache memoizes parsed run files by (mtime, size). The dashboard poller
// re-reads EVERY job's run file on every tick; almost all of them (terminal
// jobs, idle actives) have not changed. A stat is ~1µs; the JSON parse it
// replaces is the actual cost. Stat-miss (file gone -> .bak fallback path)
// bypasses the cache entirely so the recovery semantics of readRun stay
// untouched.
type runCache struct {
	mu    sync.Mutex
	m     map[string]runCacheEntry
	parse func(path string) (*parsedRun, error) // seam (tests count parses)
}

type runCacheEntry struct {
	mtime time.Time
	size  int64
	run   *parsedRun
}

func newRunCache() *runCache {
	return &runCache{m: map[string]runCacheEntry{}, parse: defaultParse}
}

// defaultParse is the uncached single-file loader (no .bak handling here —
// the cache only fronts the stat-hit fast path).
func defaultParse(path string) (*parsedRun, error) {
	return readRunFile(path)
}

// read returns the parsed run for <runsDir>/<runID>.json, reparsing only when
// the stat key changed. A missing primary file returns (nil, fs.ErrNotExist)
// so the caller falls back to the full readRun (.bak recovery).
func (c *runCache) read(runsDir, runID string) (*parsedRun, error) {
	if runID == "" {
		return nil, os.ErrNotExist
	}
	path := filepath.Join(runsDir, runID+".json")
	st, err := os.Stat(path)
	if err != nil {
		// File gone: DROP the entry so a later reappearance with a
		// coincidentally-equal stat key can never serve the old parse
		// (review finding #10).
		c.mu.Lock()
		delete(c.m, path)
		c.mu.Unlock()
		return nil, err
	}
	key := path
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && e.mtime.Equal(st.ModTime()) && e.size == st.Size() {
		return e.run, nil
	}
	run, err := c.parse(path)
	if err != nil {
		return nil, err // do not cache failures (mid-write); next tick retries
	}
	c.mu.Lock()
	c.m[key] = runCacheEntry{mtime: st.ModTime(), size: st.Size(), run: run}
	c.mu.Unlock()
	return run, nil
}
