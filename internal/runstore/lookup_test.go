package runstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/config"
)

// runsDirWithFixtures builds a temp runs dir holding the canonical + partial
// fixtures (named by their runId), plus a .bak/.tmp that must be ignored.
func runsDirWithFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFile := func(src, dstName string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(dir, dstName), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", dstName, err)
		}
	}
	copyFile(filepath.Join("testdata", "sample-run.json"), "mq40rdpt-yij9hj.json")
	copyFile(filepath.Join("testdata", "sample-run-partial.json"), "pq71abcd-partial.json")
	copyFile(filepath.Join("testdata", "sample-run.json"), "ignored.json.bak")
	copyFile(filepath.Join("testdata", "sample-run.json"), "ignored.json.tmp")
	return dir
}

func TestRunIDForSession_Found(t *testing.T) {
	dir := runsDirWithFixtures(t)
	got, ok := RunIDForSession(dir, "019ea2fe-db76-7e31-85ad-9718d3fbc23a")
	if !ok {
		t.Fatal("RunIDForSession ok = false, want true")
	}
	if got != "mq40rdpt-yij9hj" {
		t.Errorf("runID = %q, want mq40rdpt-yij9hj", got)
	}
}

func TestRunIDForSession_PartialSession(t *testing.T) {
	dir := runsDirWithFixtures(t)
	got, ok := RunIDForSession(dir, "019ea2fe-aaaa-7e31-85ad-partialrun01")
	if !ok || got != "pq71abcd-partial" {
		t.Errorf("runID = %q ok=%v, want pq71abcd-partial true", got, ok)
	}
}

func TestRunIDForSession_NotFound(t *testing.T) {
	dir := runsDirWithFixtures(t)
	if got, ok := RunIDForSession(dir, "no-such-session"); ok || got != "" {
		t.Errorf("RunIDForSession = %q,%v want \"\",false (blind window)", got, ok)
	}
}

func TestRunIDForSession_EmptySessionNeverMatches(t *testing.T) {
	dir := runsDirWithFixtures(t)
	if _, ok := RunIDForSession(dir, ""); ok {
		t.Error("RunIDForSession(\"\") ok = true, want false")
	}
}

func TestRunIDForSession_MissingDir(t *testing.T) {
	if got, ok := RunIDForSession(filepath.Join(t.TempDir(), "nope"), "x"); ok || got != "" {
		t.Errorf("RunIDForSession missing dir = %q,%v want \"\",false", got, ok)
	}
}

func TestLoad_Found(t *testing.T) {
	dir := runsDirWithFixtures(t)
	r, err := Load(dir, "mq40rdpt-yij9hj")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.RunID != "mq40rdpt-yij9hj" || r.Status != "completed" {
		t.Errorf("Load = %+v, want runId mq40rdpt-yij9hj completed", r)
	}
}

func TestLoad_MissingReturnsErrRunNotFound(t *testing.T) {
	dir := runsDirWithFixtures(t)
	_, err := Load(dir, "does-not-exist")
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Load missing err = %v, want errors.Is ErrRunNotFound", err)
	}
}

func TestListItems_ResolvesCwdRunsDir(t *testing.T) {
	cwd := t.TempDir()
	runsDir := filepath.Join(cwd, config.RunsDirRel)
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "mq40rdpt-yij9hj.json"), b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	items, err := ListItems(cwd, 20)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || items[0].RunID != "mq40rdpt-yij9hj" {
		t.Errorf("ListItems = %+v, want one item mq40rdpt-yij9hj", items)
	}
}

func TestListItems_MissingCwdEmpty(t *testing.T) {
	items, err := ListItems(t.TempDir(), 20)
	if err != nil {
		t.Fatalf("ListItems on cwd with no runs dir: %v, want nil err", err)
	}
	if len(items) != 0 {
		t.Errorf("ListItems = %+v, want empty", items)
	}
}
