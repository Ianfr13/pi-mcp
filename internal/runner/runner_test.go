package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// installFakePi copies the fake-pi.sh script to a temp dir under the name "pi"
// and prepends that dir to PATH for the duration of the test, so that
// exec.LookPath("pi") resolves to the fake. Returns the sentinel file path.
func installFakePi(t *testing.T) (sentinel string) {
	t.Helper()
	srcAbs, err := filepath.Abs("testdata/fake-pi.sh")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	src, err := os.ReadFile(srcAbs)
	if err != nil {
		t.Fatalf("read fake-pi.sh: %v", err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "pi")
	if err := os.WriteFile(dst, src, 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	fixtureAbs, err := filepath.Abs("testdata/fake-pi-fixture.jsonl")
	if err != nil {
		t.Fatalf("abs fixture: %v", err)
	}
	sentinel = filepath.Join(dir, "sentinel.out")

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PI_SENTINEL", sentinel)
	t.Setenv("FAKE_PI_FIXTURE", fixtureAbs)
	t.Setenv("FAKE_PI_PROBE", "probe-value-123")
	return sentinel
}

func TestSpawnHappyPath(t *testing.T) {
	sentinel := installFakePi(t)
	workdir := t.TempDir()

	proc, err := Spawn(context.Background(), SpawnConfig{
		Prompt:         "RENDERED PROMPT\nline2",
		NoContextFiles: false,
		CWD:            workdir,
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Drain stdout fully (parser would do this; here we assert bytes flow).
	stdout, err := io.ReadAll(proc.Stdout)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if werr := proc.Wait(); werr != nil {
		t.Fatalf("Wait: %v", werr)
	}

	// stdout carried the fixture stream.
	if !strings.Contains(string(stdout), `"type":"session"`) {
		t.Errorf("stdout missing fixture stream:\n%s", stdout)
	}

	// Inspect the sentinel the fake pi wrote.
	sb, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	s := string(sb)

	// stdin was /dev/null (empty).
	if !strings.Contains(s, "STDIN_EMPTY_OK") {
		t.Errorf("fake pi did not see empty stdin (stdin != /dev/null):\n%s", s)
	}
	if strings.Contains(s, "STDIN_NOT_EMPTY") {
		t.Errorf("fake pi saw bytes on stdin:\n%s", s)
	}

	// cwd was the workdir we asked for.
	if !strings.Contains(s, "CWD="+workdir) {
		t.Errorf("cwd mismatch; want CWD=%s\n%s", workdir, s)
	}

	// argv: base flags present, NO --no-context-files, prompt is one element.
	for _, want := range []string{"ARG=-p", "ARG=--mode", "ARG=json", "ARG=--no-session", "ARG=RENDERED PROMPT\nline2"} {
		if !strings.Contains(s, want) {
			t.Errorf("argv missing %q\n%s", want, s)
		}
	}
	if strings.Contains(s, "ARG=--no-context-files") {
		t.Errorf("unexpected --no-context-files in argv\n%s", s)
	}

	// env passthrough: os.Environ() carried FAKE_PI_PROBE through to the child.
	if !strings.Contains(s, "ENV_FAKE_PI_PROBE=probe-value-123") {
		t.Errorf("env not passed through (FAKE_PI_PROBE missing)\n%s", s)
	}
}

func TestSpawnNoContextFilesFlag(t *testing.T) {
	sentinel := installFakePi(t)
	workdir := t.TempDir()

	proc, err := Spawn(context.Background(), SpawnConfig{
		Prompt:         "P",
		NoContextFiles: true,
		CWD:            workdir,
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, err := io.ReadAll(proc.Stdout); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	sb, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if !strings.Contains(string(sb), "ARG=--no-context-files") {
		t.Errorf("expected --no-context-files in argv\n%s", sb)
	}
}

func TestSpawnContextKill(t *testing.T) {
	installFakePi(t)
	workdir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	// __HANG__ token makes the fake pi block forever until killed.
	proc, err := Spawn(ctx, SpawnConfig{
		Prompt:         "ignored __HANG__ token",
		NoContextFiles: false,
		CWD:            workdir,
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if proc.PID() <= 0 {
		t.Fatalf("expected a valid PID, got %d", proc.PID())
	}

	// Cancel → CommandContext kills the process.
	cancel()

	// Wait must return (a kill surfaces as a non-nil error). The test would
	// hang here forever if cancel did not kill the child — that is the assertion.
	done := make(chan error, 1)
	go func() {
		_, _ = io.ReadAll(proc.Stdout)
		done <- proc.Wait()
	}()

	select {
	case werr := <-done:
		if werr == nil {
			t.Fatal("expected non-nil error from killed process, got nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("process was not killed within 10s of context cancel")
	}
}

func TestSpawnEmptyCWD(t *testing.T) {
	if _, err := Spawn(context.Background(), SpawnConfig{
		Prompt: "P",
		CWD:    "",
		Stderr: io.Discard,
	}); err == nil {
		t.Fatal("expected error for empty cwd, got nil")
	}
}

func TestSpawnNilStderr(t *testing.T) {
	if _, err := Spawn(context.Background(), SpawnConfig{
		Prompt: "P",
		CWD:    t.TempDir(),
		Stderr: nil,
	}); err == nil {
		t.Fatal("expected error for nil stderr, got nil")
	}
}

func TestSpawnNewProcessGroup(t *testing.T) {
	installFakePi(t)
	workdir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// __HANG__ keeps the fake pi alive so we can inspect its process group.
	proc, err := Spawn(ctx, SpawnConfig{
		Prompt: "ignored __HANG__ token",
		CWD:    workdir,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	pid := proc.PID()
	if pid <= 0 {
		t.Fatalf("bad pid %d", pid)
	}
	// Setpgid makes the child its own process-group leader: pgid == pid. Without
	// it the child inherits the test runner's group (pgid != pid).
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(%d): %v", pid, err)
	}
	if pgid != pid {
		t.Errorf("child not in its own process group: pgid=%d pid=%d", pgid, pid)
	}

	// Cancel must group-kill and let Wait return promptly.
	cancel()
	go func() { _, _ = io.ReadAll(proc.Stdout) }()
	done := make(chan struct{})
	go func() { _ = proc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("group kill did not terminate the process within 10s")
	}
}
