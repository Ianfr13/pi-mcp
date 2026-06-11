package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// expectSignal waits up to 3s for a notification (debounce is 50ms).
func expectSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("no notification for %s", what)
	}
}

func TestSubscribe_WakesOnCreateAndWrite(t *testing.T) {
	dir := t.TempDir()
	ch, cancel, err := Subscribe(dir)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	p := filepath.Join(dir, "run.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectSignal(t, ch, "file create")

	if err := os.WriteFile(p, []byte(`{"a":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectSignal(t, ch, "file write")
}

func TestSubscribe_CoalescesBursts(t *testing.T) {
	dir := t.TempDir()
	ch, cancel, err := Subscribe(dir)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	p := filepath.Join(dir, "run.json")
	for i := 0; i < 20; i++ {
		_ = os.WriteFile(p, []byte{byte(i)}, 0o644)
	}
	expectSignal(t, ch, "burst")
	// drain whatever coalesced frames exist, then assert quiet
	deadline := time.After(300 * time.Millisecond)
	n := 0
	for {
		select {
		case <-ch:
			n++
			if n > 5 {
				t.Fatalf("burst not coalesced: %d notifications", n)
			}
		case <-deadline:
			return
		}
	}
}

func TestSubscribe_LateBornTargetDir(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "a", "b", "runs")

	ch, cancel, err := Subscribe(target) // target does NOT exist yet
	if err != nil {
		t.Fatalf("subscribe on missing dir must fall back to ancestor: %v", err)
	}
	defer cancel()

	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "r1.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	expectSignal(t, ch, "file inside late-born target")
}

func TestSubscribe_CancelStops(t *testing.T) {
	dir := t.TempDir()
	ch, cancel, err := Subscribe(dir)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cancel()
	cancel() // idempotent
	_ = os.WriteFile(filepath.Join(dir, "x.json"), []byte(`1`), 0o644)
	select {
	case _, open := <-ch:
		if open {
			// a buffered pre-cancel frame is acceptable; a SECOND would not be
			select {
			case _, open2 := <-ch:
				if open2 {
					t.Fatalf("notifications after cancel")
				}
			case <-time.After(200 * time.Millisecond):
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
}
