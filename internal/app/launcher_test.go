package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pi-mcp/internal/jobs"
	"pi-mcp/internal/model"
)

// writeFixture writes a JSONL stream fixture to a temp file and returns its path.
func writeFixture(t *testing.T, stream string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.jsonl")
	if err := os.WriteFile(p, []byte(stream), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

// installFakePi copies the runner's fake pi onto PATH as "pi" and points it at a
// stream fixture that emits a session line + a workflow tool_execution_end.
func installFakePi(t *testing.T, fixture string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "runner", "testdata", "fake-pi.sh"))
	if err != nil {
		t.Fatalf("read fake pi: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pi"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PI_SENTINEL", filepath.Join(dir, "sentinel.out"))
	t.Setenv("FAKE_PI_FIXTURE", fixture)
}

func TestRealLauncher_SuccessSurfacesSessionAndCompletes(t *testing.T) {
	fix := writeFixture(t, ``+
		`{"type":"session","id":"sess-launch-1","cwd":"/tmp"}`+"\n"+
		`{"type":"tool_execution_end","toolName":"workflow","isError":false,`+
		`"result":{"content":[{"type":"text","text":"ok"}]}}`+"\n")
	installFakePi(t, fix)

	pid, sessionCh, wait, err := realLauncher{}.Launch(context.Background(), jobs.Spec{
		Mode: model.ModeRead, CWD: t.TempDir(), Task: "judge", RunsDir: "/x",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("expected pid, got %d", pid)
	}

	// The session id must be pushed during the "blind window" (before wait() returns).
	select {
	case sid := <-sessionCh:
		if sid != "sess-launch-1" {
			t.Fatalf("sessionCh = %q, want sess-launch-1", sid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session id on sessionCh")
	}

	if err := wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestRealLauncher_NoWorkflowRunSurfacesError(t *testing.T) {
	// A direct answer with no workflow tool_execution_end -> NO_WORKFLOW_RUN.
	fix := writeFixture(t, ``+
		`{"type":"session","id":"sess-nowf"}`+"\n"+
		`{"type":"text","text":"a direct answer, no workflow"}`+"\n")
	installFakePi(t, fix)

	_, sessionCh, wait, err := realLauncher{}.Launch(context.Background(), jobs.Spec{
		Mode: model.ModeRead, CWD: t.TempDir(), Task: "judge", RunsDir: "/x",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// session id still surfaces during the blind window.
	select {
	case sid := <-sessionCh:
		if sid != "sess-nowf" {
			t.Fatalf("sessionCh = %q, want sess-nowf", sid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session id")
	}

	werr := wait()
	if werr == nil {
		t.Fatal("expected NO_WORKFLOW_RUN error from wait(), got nil")
	}
	if !strings.Contains(werr.Error(), "NO_WORKFLOW_RUN") {
		t.Fatalf("wait() error = %q, want it to contain NO_WORKFLOW_RUN", werr.Error())
	}
}

func TestRealLauncher_WorkflowIsErrorSurfacesText(t *testing.T) {
	// A workflow that ran but failed (isError:true) -> the isError text is surfaced.
	fix := writeFixture(t, ``+
		`{"type":"session","id":"sess-err"}`+"\n"+
		`{"type":"tool_execution_end","toolName":"workflow","isError":true,`+
		`"result":{"content":[{"type":"text","text":"WORKFLOW_ABORTED: boom"}]}}`+"\n")
	installFakePi(t, fix)

	_, _, wait, err := realLauncher{}.Launch(context.Background(), jobs.Spec{
		Mode: model.ModeRead, CWD: t.TempDir(), Task: "judge", RunsDir: "/x",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	werr := wait()
	if werr == nil {
		t.Fatal("expected isError text surfaced from wait(), got nil")
	}
	if !strings.Contains(werr.Error(), "boom") {
		t.Fatalf("wait() error = %q, want it to contain the isError text 'boom'", werr.Error())
	}
}
