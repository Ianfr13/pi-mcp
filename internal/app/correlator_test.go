package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readSessionID decodes the file's sessionId field with encoding/json.
func readSessionID(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read run file: %v", err)
	}
	var doc struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("decode run file: %v", err)
	}
	return doc.SessionID
}

func TestRealCorrelator_FindsRunBySession(t *testing.T) {
	runs := t.TempDir()
	b, err := os.ReadFile(filepath.Join("..", "runstore", "testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runs, "mq40rdpt-yij9hj.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sid := readSessionID(t, filepath.Join(runs, "mq40rdpt-yij9hj.json"))
	got, ok := realCorrelator{}.RunIDForSession(runs, sid)
	if !ok || got != "mq40rdpt-yij9hj" {
		t.Fatalf("RunIDForSession = %q,%v", got, ok)
	}
}

func TestRealCorrelator_MissReturnsFalse(t *testing.T) {
	runs := t.TempDir()
	got, ok := realCorrelator{}.RunIDForSession(runs, "no-such-session")
	if ok || got != "" {
		t.Fatalf("RunIDForSession = %q,%v; want \"\",false", got, ok)
	}
}
