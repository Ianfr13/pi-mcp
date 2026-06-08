package runstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReadAuthoring_RoundTrip: writing an AuthoringInfo JSON file and reading it
// back yields the same JobID/Model/Chars/Preview/Done. UpdatedAt is compared
// leniently (parses to the same instant) because JSON round-trips may shift
// sub-nanosecond precision.
func TestReadAuthoring_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	jobID := "job-auth-1"
	path := AuthoringPath(dir, jobID)
	payload := `{"jobId":"job-auth-1","model":"openai-codex/gpt-5.5","chars":42,` +
		`"preview":"hello world","done":true,"updatedAt":"2026-06-08T12:34:56.789Z"}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, ok := ReadAuthoring(dir, jobID)
	if !ok {
		t.Fatalf("ReadAuthoring: ok=false, want true")
	}
	if got.JobID != "job-auth-1" {
		t.Errorf("JobID = %q, want job-auth-1", got.JobID)
	}
	if got.Model != "openai-codex/gpt-5.5" {
		t.Errorf("Model = %q, want openai-codex/gpt-5.5", got.Model)
	}
	if got.Chars != 42 {
		t.Errorf("Chars = %d, want 42", got.Chars)
	}
	if got.Preview != "hello world" {
		t.Errorf("Preview = %q, want hello world", got.Preview)
	}
	if !got.Done {
		t.Errorf("Done = false, want true")
	}
	tt, err := time.Parse(time.RFC3339Nano, got.UpdatedAt)
	if err != nil {
		t.Fatalf("parse UpdatedAt: %v", err)
	}
	want := time.Date(2026, 6, 8, 12, 34, 56, 789_000_000, time.UTC)
	if !tt.Equal(want) {
		t.Errorf("UpdatedAt = %s, want %s", tt, want)
	}
}

// TestReadAuthoring_MissingAndCorrupt: missing file, empty jobID, and corrupt
// JSON all return (nil, false) — the consumer treats the authoring as not
// available rather than as a hard error. AuthoringPath on empty jobID must
// also be safe (it still returns a path; the file simply will not exist).
func TestReadAuthoring_MissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()

	// missing
	if got, ok := ReadAuthoring(dir, "no-such-job"); got != nil || ok {
		t.Errorf("missing file: got=%+v ok=%v, want (nil, false)", got, ok)
	}

	// empty jobID — must not panic, must not synthesize a path under the runs dir
	if got, ok := ReadAuthoring(dir, ""); got != nil || ok {
		t.Errorf("empty jobID: got=%+v ok=%v, want (nil, false)", got, ok)
	}

	// corrupt JSON
	bad := AuthoringPath(dir, "job-bad")
	if err := os.WriteFile(bad, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if got, ok := ReadAuthoring(dir, "job-bad"); got != nil || ok {
		t.Errorf("corrupt file: got=%+v ok=%v, want (nil, false)", got, ok)
	}
}

// TestListRuns_IgnoresAuthoring: the <jobID>.authoring file lives in the same
// runs dir as the *.json run files, but it must NOT show up in pi_list
// (otherwise authoring would pollute the run list and confuse operators).
func TestListRuns_IgnoresAuthoring(t *testing.T) {
	dir := writeRunsDir(t) // 2 valid run files + 1 .bak + 1 .tmp
	// add an authoring file alongside
	auth := `{"jobId":"job-x","model":"openai-codex/gpt-5.5","chars":3,` +
		`"preview":"hi","done":false,"updatedAt":"2026-06-08T12:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "job-x.authoring"), []byte(auth), 0o644); err != nil {
		t.Fatalf("write authoring: %v", err)
	}

	out, err := ListRuns(dir, 20)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(out.Runs) != 2 {
		t.Errorf("len(Runs) = %d, want 2 (authoring must not count)", len(out.Runs))
	}
	for _, r := range out.Runs {
		if strings.HasSuffix(r.RunID, ".authoring") || strings.Contains(r.RunID, ".authoring") {
			t.Errorf("authoring leaked into list: %+v", r)
		}
	}
}
