package dashboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunCache_ParsesOnceUntilFileChanges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r1.json")
	if err := os.WriteFile(p, []byte(`{"runId":"r1","status":"running","agents":[],"journal":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newRunCache()
	parses := 0
	c.parse = func(path string) (run *parsedRun, err error) {
		parses++
		return defaultParse(path)
	}
	// NOTE: if the implementation wraps runstore.ReadRun directly without a
	// parse seam, count via a package-level test hook instead — the assertion
	// that matters is the parse COUNT, adapt the seam to the implementation.

	r1, err := c.read(dir, "r1")
	if err != nil || r1 == nil || r1.RunID != "r1" {
		t.Fatalf("first read: %v %+v", err, r1)
	}
	r2, _ := c.read(dir, "r1")
	if r2 == nil || parses != 1 {
		t.Fatalf("unchanged file must be served from cache: parses=%d", parses)
	}

	// rewrite with different size -> reparse
	if err := os.WriteFile(p, []byte(`{"runId":"r1","status":"completed","agents":[],"journal":[],"durationMs":5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r3, _ := c.read(dir, "r1")
	if r3 == nil || r3.Status != "completed" || parses != 2 {
		t.Fatalf("changed file must reparse: parses=%d run=%+v", parses, r3)
	}
}

func TestRunCache_EvictsEntriesUnusedPastTTL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r1.json")
	if err := os.WriteFile(p, []byte(`{"runId":"r1","status":"completed","agents":[],"journal":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newRunCache()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	if _, err := c.read(dir, "r1"); err != nil {
		t.Fatalf("seed read: %v", err)
	}
	if len(c.m) != 1 {
		t.Fatalf("entry cached: got %d", len(c.m))
	}

	// A read of ANOTHER key past the TTL sweeps the stale entry: a job that
	// left the registry is never read again, so only sweeping evicts it.
	p2 := filepath.Join(dir, "r2.json")
	if err := os.WriteFile(p2, []byte(`{"runId":"r2","status":"running","agents":[],"journal":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(cacheEntryTTL + time.Minute)
	if _, err := c.read(dir, "r2"); err != nil {
		t.Fatalf("second read: %v", err)
	}
	c.mu.Lock()
	_, stale := c.m[p]
	_, fresh := c.m[p2]
	c.mu.Unlock()
	if stale {
		t.Fatalf("entry unused past TTL must be evicted by the sweep")
	}
	if !fresh {
		t.Fatalf("freshly used entry must survive the sweep")
	}
}
