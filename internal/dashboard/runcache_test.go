package dashboard

import (
	"os"
	"path/filepath"
	"testing"
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
