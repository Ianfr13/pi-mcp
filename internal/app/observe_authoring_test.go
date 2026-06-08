package app

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/model"
	"pi-mcp/internal/parser"
	"pi-mcp/internal/runstore"
)

// authoringStream: user forcing prompt (must be EXCLUDED), assistant thinking,
// then the workflow tool start carrying the script, then the result.
const authoringStream = `{"type":"session","id":"sess-xyz"}
{"type":"agent_start"}
{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"You MUST make exactly ONE call to the workflow tool SECRETPROMPT"}]}}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"You MUST make exactly ONE call to the workflow tool SECRETPROMPT"}]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Formulating agent script: a Recon phase then fan out."}]}}
{"type":"tool_execution_start","toolName":"workflow","args":{"script":"phase('Recon'); parallel([agent('git status')])"}}
{"type":"tool_execution_end","toolName":"workflow","isError":false,"result":{"content":[{"type":"text","text":"done"}]}}
`

func TestObserveAuthoring_WritesPreviewAndDeletes(t *testing.T) {
	dir := t.TempDir()
	spec := jobs.Spec{JobID: "job-A", RunsDir: dir}
	sessionCh := make(chan string, 1)

	done := make(chan struct{})
	go func() { observeAuthoring(strings.NewReader(authoringStream), sessionCh, spec); close(done) }()

	select {
	case sid := <-sessionCh:
		if sid != "sess-xyz" {
			t.Fatalf("sessionId = %q want sess-xyz", sid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sessionId never pushed")
	}
	<-done

	// after return the file must be DELETED
	if _, err := os.Stat(runstore.AuthoringPath(dir, "job-A")); !os.IsNotExist(err) {
		t.Errorf("authoring file should be deleted on observer return, stat err=%v", err)
	}
}

func TestObserveAuthoring_PreviewContentExcludesPrompt(t *testing.T) {
	dir := t.TempDir()
	spec := jobs.Spec{JobID: "job-B", RunsDir: dir}
	sessionCh := make(chan string, 1)

	// Keep the pipe OPEN (no EOF) so the file persists while we inspect it.
	pr, pw := io.Pipe()
	go observeAuthoring(pr, sessionCh, spec)
	if _, err := io.WriteString(pw, authoringStream); err != nil {
		t.Fatal(err)
	}
	<-sessionCh

	got := waitForAuthoring(t, dir, "job-B", time.Now().Add(2*time.Second))
	if !got.Done {
		t.Errorf("expected done=true after workflow tool start; got %+v", got)
	}
	if got.Model != config.OrchestratorModel {
		t.Errorf("model = %q want %q", got.Model, config.OrchestratorModel)
	}
	if got.Preview == "" {
		t.Fatal("preview is empty")
	}
	if strings.Contains(got.Preview, "SECRETPROMPT") {
		t.Errorf("preview must NOT contain the role:user forcing prompt: %q", got.Preview)
	}
	if !strings.Contains(got.Preview, "Recon") {
		t.Errorf("preview should contain the assistant thinking/script: %q", got.Preview)
	}
	_ = pw.Close() // let the observer reach EOF + clean up
}

// waitForAuthoring polls the authoring file until done=true (the writer goroutine
// is async) or the deadline passes.
func waitForAuthoring(t *testing.T, dir, jobID string, deadline time.Time) model.AuthoringInfo {
	t.Helper()
	for time.Now().Before(deadline) {
		if a, ok := runstore.ReadAuthoring(dir, jobID); ok && a.Done {
			return *a
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a, ok := runstore.ReadAuthoring(dir, jobID); ok {
		return *a // return whatever we have for a useful failure message
	}
	t.Fatal("authoring file never appeared")
	return model.AuthoringInfo{}
}

// TestObserveAuthoring_TeeNoDeadlock replicates Launch's tee wiring: observeAuthoring
// and parser.ParseStream both drain the same stream via io.Pipe+TeeReader. If the
// observer early-returned at the workflow tool start, the pipe writer would block and
// ParseStream would never return. A large tool_execution_end AFTER the workflow start
// stresses the "must read to EOF" invariant.
func TestObserveAuthoring_TeeNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	spec := jobs.Spec{JobID: "job-C", RunsDir: dir}
	big := strings.Repeat("x", 200000)
	stream := `{"type":"session","id":"s1"}
{"type":"tool_execution_start","toolName":"workflow","args":{"script":"phase('x')"}}
{"type":"tool_execution_end","toolName":"workflow","isError":false,"result":{"content":[{"type":"text","text":"` + big + `"}]}}
`
	pr, pw := io.Pipe()
	tee := io.TeeReader(strings.NewReader(stream), pw)
	sessionCh := make(chan string, 1)

	go observeAuthoring(pr, sessionCh, spec)

	parsed := make(chan parser.Result, 1)
	go func() {
		res, _ := parser.ParseStream(context.Background(), tee)
		_ = pw.Close()
		parsed <- res
	}()

	select {
	case <-parsed:
		// ParseStream returned -> no deadlock (observeAuthoring drained to EOF).
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: ParseStream did not return — observeAuthoring likely early-returned")
	}
}
