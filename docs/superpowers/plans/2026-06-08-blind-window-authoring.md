# Blind-Window Authoring Transparency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the ~20s blind window (before the run file exists, while `pi`'s pinned orchestrator authors the workflow script) transparent — surface `✍ writing the workflow plan…` + a live elapsed timer + the model immediately, and reveal the authored plan when it lands — to both `pi_status` and the dashboard. MCP-side only; no pi-engine change.

**Architecture:** The launcher already tees `pi` stdout (one copy → `parser.ParseStream`, one → `peekSessionID`). Replace `peekSessionID` with `observeAuthoring`, which (a) still pushes the sessionId, (b) accumulates the orchestrator's assistant text/thinking + the workflow script into a per-job file `<RunsDir>/<jobID>.authoring` via a **decoupled writer goroutine** (so disk I/O never back-pressures the parser), and (c) deletes the file when it returns. `pi_status` and the dashboard read that file **only while in the blind window** via a shared `runstore.ReadAuthoring`.

**Tech Stack:** Go 1.26, stdlib `testing` only (no testify), per-package `testdata/`. Spec: `docs/superpowers/specs/2026-06-08-blind-window-authoring-design.md`.

---

## Critical correctness constraints (from the adversarial review — do NOT violate)

1. **`observeAuthoring` MUST drain its reader to EOF unconditionally.** It shares an `io.Pipe`/`io.TeeReader` with `parser.ParseStream`; if it returns early (e.g. at the workflow tool start), the pipe writer blocks forever and `wait()` deadlocks. Setting `done=true` does NOT stop reading.
2. **No synchronous disk I/O in the read loop.** File writes happen in a separate writer goroutine fed a latest-wins snapshot; the read loop never blocks on the filesystem.
3. **`MkdirAll` once**, before the loop — not per line.
4. **`gpt-5.5`/codex does not stream** — the preview populates in one shot at `message_end` (~1s before the run file). The immediate value is the timer+model+spinner (client-side); the preview is a late reveal. Do not build UI that assumes progressive fill.

---

## Conventions

- Module `pi-mcp`. Stdlib `testing` only. Run: `go test -race ./...`. Build: `go build ./...`.
- Commit after each task with the shown message. You are on branch `build/dashboard`.

---

## File Structure

```
internal/config/config.go                # +MaxAuthoringPreviewBytes const                 (Task 1)
internal/model/model.go                  # +AuthoringInfo; +StatusOutput.Authoring          (Task 1)
internal/runstore/authoring.go (new)     # AuthoringPath + ReadAuthoring (+ test)           (Task 2)
internal/jobs/job.go                     # +Spec.JobID                                      (Task 3)
internal/jobs/registry.go                # start(): set spec.JobID                          (Task 3)
internal/app/launcher.go                 # peekSessionID -> observeAuthoring + writer        (Task 4)
internal/app/observe_authoring_test.go (new)  # observer tests + tee-no-deadlock            (Task 4)
internal/mcpserver/ports.go              # RunStore +ReadAuthoring                          (Task 5)
internal/mcpserver/handler_status.go     # blind branch: out.Authoring                      (Task 5)
internal/app/runstore_adapter.go         # adapter +ReadAuthoring                           (Task 5)
internal/dashboard/state.go              # JobDetail.Authoring; BuildDetail blind read      (Task 6)
internal/dashboard/web/app.js, app.css   # blind render: timer+model+preview                (Task 7)
```

---

## Task 1: Data types — `AuthoringInfo`, `StatusOutput.Authoring`, config const

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/model/model.go`

- [ ] **Step 1: Add the config const**

Append to `internal/config/config.go` (near `MaxInlineResultBytes`):
```go
// MaxAuthoringPreviewBytes caps the authoring preview persisted to
// <RunsDir>/<jobID>.authoring (the orchestrator's in-flight plan). Smaller than
// MaxInlineResultBytes (16KB, for finished results) — this is a live snippet.
const MaxAuthoringPreviewBytes = 6 * 1024
```

- [ ] **Step 2: Add the `AuthoringInfo` type**

Append to `internal/model/model.go` (after `TokenUsage`):
```go
// AuthoringInfo is the live blind-window authoring snapshot, persisted to
// <RunsDir>/<jobID>.authoring by the launcher and read by pi_status + the
// dashboard ONLY while the run file is absent (blind window). All-string/scalar
// fields: it is also an MCP output (StatusOutput.Authoring), so no `any` field.
type AuthoringInfo struct {
	JobID     string `json:"jobId"`
	Model     string `json:"model"`             // the pinned orchestrator (config.OrchestratorModel)
	Chars     int    `json:"chars"`             // total assistant chars observed (progress hint)
	Preview   string `json:"preview"`           // accumulated plan text, tail-truncated
	Done      bool   `json:"done"`              // true once the workflow tool starts (authoring finished)
	UpdatedAt string `json:"updatedAt,omitempty"` // RFC3339, last write
}
```

- [ ] **Step 3: Add the `Authoring` field to `StatusOutput`**

In `internal/model/model.go`, in the `StatusOutput` struct, add the field right after the `Progress` field:
```go
	Authoring    *AuthoringInfo       `json:"authoring,omitempty"` // blind-window: live authoring snapshot
```

- [ ] **Step 4: Build**

Run: `go build ./internal/config/ ./internal/model/`
Expected: no output (compiles). (Pure type/const additions — behavioral tests come via Tasks 2/4/5.)

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/model/model.go
git commit -m "feat(model): AuthoringInfo type + StatusOutput.Authoring + preview cap const"
```

---

## Task 2: `runstore.AuthoringPath` + `ReadAuthoring`

**Files:**
- Create: `internal/runstore/authoring.go`
- Create: `internal/runstore/authoring_test.go`

- [ ] **Step 1: Write the failing test**

`internal/runstore/authoring_test.go`:
```go
package runstore

import (
	"os"
	"path/filepath"
	"testing"

	"pi-mcp/internal/model"
)

func TestReadAuthoring_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := AuthoringPath(dir, "job-1")
	if filepath.Base(p) != "job-1.authoring" {
		t.Fatalf("AuthoringPath base = %q want job-1.authoring", filepath.Base(p))
	}
	if err := os.WriteFile(p, []byte(`{"jobId":"job-1","model":"openai-codex/gpt-5.5","chars":42,"preview":"phase('Recon')","done":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a, ok := ReadAuthoring(dir, "job-1")
	if !ok {
		t.Fatal("ReadAuthoring ok=false")
	}
	if a.JobID != "job-1" || a.Model != "openai-codex/gpt-5.5" || a.Chars != 42 || a.Preview != "phase('Recon')" || a.Done {
		t.Errorf("decoded = %+v", a)
	}
}

func TestReadAuthoring_MissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadAuthoring(dir, "nope"); ok {
		t.Error("missing file should be ok=false")
	}
	if _, ok := ReadAuthoring(dir, ""); ok {
		t.Error("empty jobID should be ok=false")
	}
	if err := os.WriteFile(AuthoringPath(dir, "bad"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadAuthoring(dir, "bad"); ok {
		t.Error("corrupt file should be ok=false")
	}
}

func TestListRuns_IgnoresAuthoring(t *testing.T) {
	dir := t.TempDir()
	// a real run file + an authoring file in the same dir
	if err := os.WriteFile(filepath.Join(dir, "r1.json"), []byte(`{"runId":"r1","status":"completed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(AuthoringPath(dir, "job-1"), []byte(`{"jobId":"job-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Runs) != 1 || out.Runs[0].RunID != "r1" {
		t.Errorf("ListRuns = %+v, want only r1 (authoring ignored)", out.Runs)
	}
	_ = model.AuthoringInfo{}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runstore/ -run Authoring`
Expected: FAIL (AuthoringPath/ReadAuthoring undefined).

- [ ] **Step 3: Write the implementation**

`internal/runstore/authoring.go`:
```go
package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"

	"pi-mcp/internal/model"
)

// AuthoringPath is the per-job blind-window authoring file:
// <runsDir>/<jobID>.authoring. The .authoring extension (not .json) keeps it out
// of ListRuns / pi_list and away from ReadRun.
func AuthoringPath(runsDir, jobID string) string {
	return filepath.Join(runsDir, jobID+".authoring")
}

// ReadAuthoring decodes the authoring file for jobID. Missing, empty-jobID, or
// corrupt -> (nil,false): callers fall back to a generic "authoring…" state and
// never error. Used by both internal/mcpserver and internal/dashboard so the two
// readers cannot drift.
func ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool) {
	if jobID == "" {
		return nil, false
	}
	b, err := os.ReadFile(AuthoringPath(runsDir, jobID))
	if err != nil {
		return nil, false
	}
	var a model.AuthoringInfo
	if json.Unmarshal(b, &a) != nil {
		return nil, false
	}
	return &a, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runstore/ -run Authoring`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runstore/authoring.go internal/runstore/authoring_test.go
git commit -m "feat(runstore): AuthoringPath + ReadAuthoring (shared blind-window reader)"
```

---

## Task 3: Plumb `Spec.JobID` to the launcher

**Files:**
- Modify: `internal/jobs/job.go`
- Modify: `internal/jobs/registry.go`

- [ ] **Step 1: Add the field**

In `internal/jobs/job.go`, add to the `Spec` struct (after `Branch`):
```go
	JobID         string // the registry job id (for the <RunsDir>/<jobID>.authoring file)
```

- [ ] **Step 2: Set it in `start()`**

In `internal/jobs/registry.go`, in `start()`, the `spec := Spec{...}` literal — add `JobID`:
```go
	spec := Spec{
		Mode:     j.Record.Mode,
		CWD:      j.Record.CWD,
		RunsDir:  j.Record.RunsDir,
		Task:     j.task,
		Context:  j.context,
		Worktree: j.Record.WorktreePath,
		Branch:   j.Record.Branch,
		JobID:    j.Record.JobID,
	}
```

- [ ] **Step 3: Verify no regression**

Run: `go build ./... && go test ./internal/jobs/`
Expected: builds; existing jobs tests PASS. (The field is consumed by Task 4's observer; its behavior is verified there and by the real e2e.)

- [ ] **Step 4: Commit**

```bash
git add internal/jobs/job.go internal/jobs/registry.go
git commit -m "feat(jobs): carry JobID on Spec for the authoring file"
```

---

## Task 4: `observeAuthoring` (replace `peekSessionID`)

**Files:**
- Modify: `internal/app/launcher.go`
- Create: `internal/app/observe_authoring_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/app/observe_authoring_test.go`:
```go
package app

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/parser"
	"pi-mcp/internal/runstore"
)

// A minimal authoring stream: user forcing prompt (must be EXCLUDED), assistant
// thinking, then the workflow tool start carrying the script, then the result.
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
	// capture the file content WHILE the observer runs by reading after it returns
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

	// Pause the stream before EOF so the file still exists when we inspect it:
	pr, pw := io.Pipe()
	go observeAuthoring(pr, sessionCh, spec)
	// write everything EXCEPT the final EOF (keep the pipe open)
	if _, err := io.WriteString(pw, authoringStream); err != nil {
		t.Fatal(err)
	}
	<-sessionCh
	// give the writer goroutine a moment to flush the last snapshot
	deadline := time.Now().Add(2 * time.Second)
	var a *config.Placeholder // replaced below
	_ = a
	var got = waitForAuthoring(t, dir, "job-B", deadline)
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

// waitForAuthoring polls the authoring file until present (the writer goroutine is
// async) or the deadline passes.
func waitForAuthoring(t *testing.T, dir, jobID string, deadline time.Time) model.AuthoringInfo {
	t.Helper()
	for time.Now().Before(deadline) {
		if a, ok := runstore.ReadAuthoring(dir, jobID); ok {
			return *a
		}
		time.Sleep(20 * time.Millisecond)
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

	go func() { observeAuthoring(pr, sessionCh, spec); }()

	parsed := make(chan parser.Result, 1)
	go func() {
		res, _ := parser.ParseStream(nil, tee)
		_ = pw.Close()
		parsed <- res
	}()

	select {
	case <-parsed:
		// ParseStream returned -> no deadlock. (observeAuthoring drained to EOF.)
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: ParseStream did not return — observeAuthoring likely early-returned")
	}
}
```

NOTE: the first test file uses `model.AuthoringInfo` and `config.OrchestratorModel`; add imports `"pi-mcp/internal/model"` and keep `"pi-mcp/internal/config"`. Remove the stray `var a *config.Placeholder` line — it is illustrative; the real helper is `waitForAuthoring`. (Final imports for the test: `io, os, strings, testing, time, pi-mcp/internal/config, pi-mcp/internal/jobs, pi-mcp/internal/model, pi-mcp/internal/parser, pi-mcp/internal/runstore`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/app/ -run ObserveAuthoring`
Expected: FAIL (observeAuthoring undefined).

- [ ] **Step 3: Implement `observeAuthoring` + writer; wire into `Launch`**

In `internal/app/launcher.go`: update imports to add `"strings"`, `"time"`, `"pi-mcp/internal/config"`, `"pi-mcp/internal/model"`, `"pi-mcp/internal/runstore"` (keep existing `bufio, bytes, context, encoding/json, fmt, io, os, pi-mcp/internal/jobs, pi-mcp/internal/parser, pi-mcp/internal/runner`).

In `Launch`, replace the line `go peekSessionID(pr, sessionCh)` with:
```go
	go observeAuthoring(pr, sessionCh, spec)
```

DELETE the entire `peekSessionID` function and ADD:
```go
// authoringLine is the minimal stream shape observeAuthoring decodes.
type authoringLine struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	ToolName string          `json:"toolName"`
	Message  json.RawMessage `json:"message"`
	Args     json.RawMessage `json:"args"`
}

// observeAuthoring drains r (a teed copy of pi stdout) to EOF — ALWAYS, never
// early-returns (it shares an io.Pipe with parser.ParseStream; an early return
// deadlocks the pipe writer). It pushes the first sessionId, accumulates the
// orchestrator's authoring (assistant text/thinking + the workflow script) and
// hands latest-wins snapshots to a decoupled writer goroutine that persists
// <RunsDir>/<jobID>.authoring (so disk I/O never back-pressures the parser). The
// file is deleted exactly once when this function returns (clean EOF or the child
// dying and EOFing the stream).
func observeAuthoring(r io.Reader, sessionCh chan<- string, spec jobs.Spec) {
	var (
		snapCh     chan model.AuthoringInfo
		writerDone = make(chan struct{})
		path       string
		persist    = spec.JobID != "" && spec.RunsDir != ""
	)
	if persist {
		path = runstore.AuthoringPath(spec.RunsDir, spec.JobID)
		_ = os.MkdirAll(spec.RunsDir, 0o755) // once, before the loop
		snapCh = make(chan model.AuthoringInfo, 1)
		go authoringWriter(path, snapCh, writerDone)
	} else {
		close(writerDone)
	}
	defer func() {
		if persist {
			close(snapCh)   // writer drains its last snapshot, then exits
			<-writerDone    // ...so the delete below can't be clobbered
			_ = os.Remove(path)
		}
	}()

	br := bufio.NewReader(r)
	pushed := false
	var preview []byte
	chars := 0
	snapshot := func(done bool) {
		if !persist {
			return
		}
		info := model.AuthoringInfo{
			JobID: spec.JobID, Model: config.OrchestratorModel, Chars: chars,
			Preview: string(preview), Done: done,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		select { // latest-wins: drop a stale pending snapshot first
		case <-snapCh:
		default:
		}
		select {
		case snapCh <- info:
		default:
		}
	}
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimRight(bytes.TrimRight(line, "\n"), "\r"); len(t) > 0 {
			var ev authoringLine
			if json.Unmarshal(t, &ev) == nil {
				switch {
				case ev.Type == "session" && !pushed && ev.ID != "":
					sessionCh <- ev.ID
					pushed = true
				case strings.HasPrefix(ev.Type, "message") && len(ev.Message) > 0:
					if txt := assistantText(ev.Message); txt != "" {
						chars += len(txt)
						preview = appendPreview(preview, txt)
						snapshot(false)
					}
				case ev.Type == "tool_execution_start" && ev.ToolName == "workflow":
					if len(ev.Args) > 0 {
						s := "\n--- workflow script ---\n" + string(ev.Args)
						chars += len(s)
						preview = appendPreview(preview, s)
					}
					snapshot(true) // authoring done; keep reading to EOF
				}
			}
		}
		if err != nil {
			return // EOF / read error — defer flushes the writer and deletes the file
		}
	}
}

// authoringWriter performs the (atomic tmp+rename) disk writes off the read loop,
// so a slow filesystem can never stall the parser sharing the tee. It exits when
// in is closed (after draining the final snapshot), then closes done.
func authoringWriter(path string, in <-chan model.AuthoringInfo, done chan<- struct{}) {
	defer close(done)
	for info := range in {
		b, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			continue
		}
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, path)
		}
	}
}

// assistantText extracts the displayable authoring text from a stream `message`
// payload. Only role=="assistant" content contributes (the role=="user" message
// is pi-mcp's own forcing prompt; "toolResult" is the finished result). It concats
// each content block's .text (type text) and .thinking (type thinking).
func assistantText(raw json.RawMessage) string {
	var m struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Role != "assistant" {
		return ""
	}
	var sb strings.Builder
	for _, c := range m.Content {
		if c.Text != "" {
			sb.WriteString(c.Text)
		}
		if c.Thinking != "" {
			sb.WriteString(c.Thinking)
		}
	}
	return sb.String()
}

// appendPreview appends s to buf and tail-truncates to MaxAuthoringPreviewBytes
// on a UTF-8 rune boundary (keep the most recent content).
func appendPreview(buf []byte, s string) []byte {
	buf = append(buf, s...)
	if len(buf) <= config.MaxAuthoringPreviewBytes {
		return buf
	}
	cut := len(buf) - config.MaxAuthoringPreviewBytes
	for cut < len(buf) && (buf[cut]&0xC0) == 0x80 { // skip mid-rune continuation bytes
		cut++
	}
	return buf[cut:]
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/app/ -run ObserveAuthoring`
Expected: PASS (all four observer tests, including TeeNoDeadlock).

- [ ] **Step 5: Run the whole app package (no regression)**

Run: `go test -race ./internal/app/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/launcher.go internal/app/observe_authoring_test.go
git commit -m "feat(app): observeAuthoring — live blind-window plan capture (deadlock-safe)"
```

---

## Task 5: `pi_status` surfaces authoring

**Files:**
- Modify: `internal/mcpserver/ports.go`
- Modify: `internal/app/runstore_adapter.go`
- Modify: `internal/mcpserver/handler_status.go`
- Modify: `internal/mcpserver/fakes_test.go` (the fake RunStore)
- Modify/Create test: `internal/mcpserver/handler_status_test.go`

- [ ] **Step 1: Extend the RunStore port**

In `internal/mcpserver/ports.go`, add to the `RunStore` interface:
```go
	// ReadAuthoring returns the live blind-window authoring snapshot for jobID
	// (the orchestrator's in-flight plan), or (nil,false) when absent/corrupt.
	ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool)
```

- [ ] **Step 2: Implement it in the adapter**

In `internal/app/runstore_adapter.go`, add:
```go
func (runStoreAdapter) ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool) {
	return runstore.ReadAuthoring(runsDir, jobID)
}
```

- [ ] **Step 3: Update the fake RunStore in tests**

In `internal/mcpserver/fakes_test.go`, find the fake that implements `RunStore` (it has `Load`/`ListItems`). Add a settable field + method:
```go
	authoring map[string]*model.AuthoringInfo // key: jobID
```
and:
```go
func (f *fakeRunStore) ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool) {
	a, ok := f.authoring[jobID]
	return a, ok
}
```
(Match the actual fake's name/shape in that file — it may be a struct literal with function fields; if so add a `ReadAuthoringFn func(string,string)(*model.AuthoringInfo,bool)` field and a method that calls it, defaulting to `(nil,false)` when nil.)

- [ ] **Step 4: Write the failing test**

Add to `internal/mcpserver/handler_status_test.go`:
```go
func TestBuildStatus_BlindWindowAttachesAuthoring(t *testing.T) {
	// a running job whose run file does not exist yet (blind window), with an
	// authoring snapshot available.
	rec := model.JobRecord{JobID: "job-A", RunsDir: "/runs", Status: model.JobRunning, Mode: model.ModeRead}
	jobs := &fakeJobs{rec: rec, ok: true}            // Lookup returns rec
	store := &fakeRunStore{                            // Load -> ErrRunNotFound; authoring present
		loadErr:   mcpErrRunNotFound(),               // wraps mcpserver.ErrRunNotFound
		authoring: map[string]*model.AuthoringInfo{"job-A": {JobID: "job-A", Model: "openai-codex/gpt-5.5", Preview: "phase('Recon')", Done: false}},
	}
	s := newServerForTest(jobs, store)               // existing test constructor
	out := s.buildStatus(s.mustResolve("job-A"))      // existing helpers; or call handleStatus
	if !out.BlindWindow {
		t.Fatal("expected blind_window=true")
	}
	if out.Authoring == nil || out.Authoring.Preview != "phase('Recon')" || out.Authoring.Model != "openai-codex/gpt-5.5" {
		t.Errorf("Authoring = %+v", out.Authoring)
	}
}
```
ADAPT this to the file's existing test helpers (constructor, how `resolved` is built, how `ErrRunNotFound` is produced). The behavioral assertions (blind_window true + Authoring populated from the store) are what matters.

- [ ] **Step 5: Run to verify it fails**

Run: `go test ./internal/mcpserver/ -run BlindWindowAttachesAuthoring`
Expected: FAIL (out.Authoring is nil — handler doesn't read it yet).

- [ ] **Step 6: Read authoring in the blind branch**

In `internal/mcpserver/handler_status.go`, in `buildStatus`, the blind-window branch (where it sets `out.Status = "running"; out.BlindWindow = true`), add the authoring read just before `out.Progress = progressBlock(...)`:
```go
				out.Status = "running"
				out.BlindWindow = true
				if a, ok := s.store.ReadAuthoring(tgt.runsDir, tgt.jobID); ok {
					out.Authoring = a
				}
				if tgt.mode == model.ModeWrite {
					out.Write = s.writeBlock(tgt, nil)
				}
				out.Progress = progressBlock(tgt, now, wtFiles, wtLast, wtOK)
				return out
```

- [ ] **Step 7: Run the tests**

Run: `go test -race ./internal/mcpserver/`
Expected: PASS (new test + all existing).

- [ ] **Step 8: Commit**

```bash
git add internal/mcpserver/ports.go internal/app/runstore_adapter.go internal/mcpserver/handler_status.go internal/mcpserver/fakes_test.go internal/mcpserver/handler_status_test.go
git commit -m "feat(mcpserver): pi_status surfaces blind-window authoring snapshot"
```

---

## Task 6: Dashboard detail surfaces authoring

**Files:**
- Modify: `internal/dashboard/state.go`
- Modify: `internal/dashboard/state_test.go`

- [ ] **Step 1: Add the field to `JobDetail`**

In `internal/dashboard/state.go`, add to the `JobDetail` struct:
```go
	Authoring    *model.AuthoringInfo       `json:"authoring,omitempty"` // blind-window live plan
```

- [ ] **Step 2: Write the failing test**

Add to `internal/dashboard/state_test.go`:
```go
func TestBuildDetail_BlindWindowAttachesAuthoring(t *testing.T) {
	dir := t.TempDir()
	// a running job, runId empty => blind window (readRun returns fs.ErrNotExist)
	rec := model.JobRecord{JobID: "job-A", RunsDir: dir, Mode: model.ModeRead, Status: model.JobRunning, StartedAt: nowFresh.Add(-5 * time.Second)}
	if err := os.WriteFile(runstore.AuthoringPath(dir, "job-A"),
		[]byte(`{"jobId":"job-A","model":"openai-codex/gpt-5.5","preview":"phase('Recon')","done":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, ok := BuildDetail(rec, nowFresh)
	if !ok {
		t.Fatal("BuildDetail ok=false")
	}
	if !d.BlindWindow {
		t.Fatalf("expected blindWindow; got status=%q", d.Status)
	}
	if d.Authoring == nil || d.Authoring.Preview != "phase('Recon')" {
		t.Errorf("Authoring = %+v", d.Authoring)
	}
}
```
Add imports to `state_test.go` if missing: `"os"`, `"pi-mcp/internal/runstore"`.

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run BlindWindowAttachesAuthoring`
Expected: FAIL (d.Authoring nil).

- [ ] **Step 4: Read authoring in the blind path of `BuildDetail`**

In `internal/dashboard/state.go`, in `BuildDetail`, change the blind-return branch:
```go
	run, err := readRun(rec.RunsDir, rec.RunID)
	if err != nil || run == nil {
		if d.BlindWindow {
			if a, ok := runstore.ReadAuthoring(rec.RunsDir, rec.JobID); ok {
				d.Authoring = a
			}
		}
		return d, true // blind / no run file: summary (+authoring) only
	}
```
(`d.BlindWindow` is already set by `summarize` for a running job with no run file. `runstore` is already imported in state.go.)

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/dashboard/`
Expected: PASS (new test + all existing).

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/state.go internal/dashboard/state_test.go
git commit -m "feat(dashboard): attach live authoring snapshot to blind-window detail"
```

---

## Task 7: Frontend — blind-window render (timer + model + preview)

**Files:**
- Modify: `internal/dashboard/web/app.js`
- Modify: `internal/dashboard/web/app.css`

- [ ] **Step 1: Replace the blind-window branch in `renderDetail`**

In `internal/dashboard/web/app.js`, find the blind branch in `renderDetail` (currently):
```js
  if (d.blindWindow) {
    h += `<div class="notice"><div class="big">✍</div><b>Orchestrator is authoring the workflow…</b><br>No run file yet (the ~20s blind window).</div>`;
    $("#panel").innerHTML = h; return;
  }
```
Replace it with:
```js
  if (d.blindWindow) {
    const a = d.authoring || {};
    const model = a.model || "orchestrator";
    const plan = a.preview
      ? `<pre class="authpre">${esc(a.preview)}</pre>`
      : `<div class="authwait"><span class="spin"></span> waiting for the plan… (the author streams it in one shot near the end)</div>`;
    h += `<div class="authoring">
      <div class="authhead">✍ writing the workflow plan…
        <span class="authmeta">· <span data-elapsed="${d.startedAt}">${elapsed(d.startedAt)}</span> · ${esc(model)}</span></div>
      ${plan}</div>`;
    $("#panel").innerHTML = h; return;
  }
```
(`esc`, `elapsed`, and the 1s `[data-elapsed]` ticker already exist in app.js.)

- [ ] **Step 2: Add styles**

Append to `internal/dashboard/web/app.css`:
```css
/* blind-window authoring */
.authoring{background:#fff;border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);padding:18px 20px;max-width:880px}
.authhead{font-size:15px;font-weight:650}
.authmeta{font-family:var(--mono);font-size:12px;color:var(--dim);font-weight:500}
.authwait{display:flex;align-items:center;gap:9px;color:var(--dim);font-size:13px;margin-top:12px}
.authpre{margin:12px 0 0;background:#0b1020;color:#cdd3e1;border-radius:10px;padding:14px;font-family:var(--mono);font-size:11.5px;line-height:1.5;max-height:380px;overflow:auto;white-space:pre-wrap;word-break:break-word}
.spin{width:13px;height:13px;border-radius:50%;border:2px solid var(--line);border-top-color:var(--run);display:inline-block;animation:spin .8s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
```

- [ ] **Step 3: Verify embed tests still pass**

Run: `go test -race ./internal/dashboard/`
Expected: PASS (assets still embed; `TestServer_Index` still finds "control plane").

- [ ] **Step 4: Visual check (fabricated authoring state)**

```bash
go build -o /tmp/pi-dashboard ./cmd/pi-dashboard
# fabricate a running, no-run-file job + its authoring file under a temp state dir
ST=$(mktemp -d); mkdir -p "$ST/pi-mcp" "$ST/runs"
cat > "$ST/pi-mcp/registry.json" <<JSON
{"jobs":[{"jobId":"job-demo","runId":"","mode":"read","cwd":"/tmp/proj","runsDir":"$ST/runs","pid":1,"status":"running","startedAt":"2026-06-08T12:00:00Z"}]}
JSON
cat > "$ST/runs/job-demo.authoring" <<JSON
{"jobId":"job-demo","model":"openai-codex/gpt-5.5","chars":120,"preview":"phase('Recon');\nparallel([ agent('git status'), agent('plan extract') ]);","done":false}
JSON
XDG_STATE_HOME="$ST" /tmp/pi-dashboard --addr 127.0.0.1:7799 --state-dir "$ST" &
PID=$!
# (use the Playwright harness/shot script to screenshot http://127.0.0.1:7799 and click job-demo)
```
Then drive Playwright (the `/tmp/shot/one.js` harness from the redesign) against `http://127.0.0.1:7799`, click `job-demo`, screenshot, and confirm it shows `✍ writing the workflow plan… · {timer} · gpt-5.5` + the plan `<pre>`. Kill the server after.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/web/app.js internal/dashboard/web/app.css
git commit -m "feat(dashboard): blind-window UI — timer + model + live plan preview"
```

---

## Task 8: Final gate

- [ ] **Step 1: Full race suite**

Run: `go test -race ./...`
Expected: all packages PASS (incl. `internal/app`, `internal/runstore`, `internal/mcpserver`, `internal/dashboard`, existing suite + e2e).

- [ ] **Step 2: Vet + fmt + build**

Run: `go vet ./... && gofmt -l internal/ cmd/ && go build -o /tmp/pi-mcp ./cmd/pi-mcp && go build -o /tmp/pi-dashboard ./cmd/pi-dashboard && echo OK`
Expected: `gofmt -l` prints nothing; ends with `OK`.

- [ ] **Step 3: Rebuild + restart the live dashboard service**

```bash
go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard
systemctl --user restart pi-dashboard && systemctl --user is-active pi-dashboard
```
Expected: `active`.

- [ ] **Step 4: Commit any final fmt fixes (if gofmt listed files)**

```bash
gofmt -w internal/ cmd/
git add -A && git commit -m "style: gofmt" || echo "nothing to format"
```

---

## Self-Review (completed by plan author)

**Spec coverage:**
- `.authoring` file `<RunsDir>/<jobID>.authoring`, JSON, `.authoring` ext → Task 2 (`AuthoringPath`) + ListRuns-ignores test. ✔
- `AuthoringInfo` shape (jobId/model/chars/preview/done/updatedAt) → Task 1. ✔ (uses strings/scalars only — safe as an MCP output, no `any` field.)
- observeAuthoring: push sessionId, accumulate assistant text/thinking + workflow script, drain to EOF, never early-return, decoupled writer, MkdirAll once, defer-delete → Task 4 + the TeeNoDeadlock test. ✔
- Skip `role:user` forcing prompt → `assistantText` (role=="assistant" only) + the `SECRETPROMPT` exclusion test. ✔
- Immediate timer+model (client-side), plan revealed late → Task 7 (`data-elapsed` + `a.preview` when present, spinner otherwise). ✔
- pi_status reads authoring in blind branch → Task 5. ✔
- dashboard detail reads authoring in blind path → Task 6. ✔
- Shared reader (no drift) → `runstore.ReadAuthoring` used by both Task 5 (via port/adapter) and Task 6. ✔
- Spec.JobID plumbing → Task 3. ✔
- Deadlock review-fixes (no early return; write back-pressure decoupled) → Task 4 design + TeeNoDeadlock test. ✔

**Placeholder scan:** none. The Task 4 test has one illustrative stray line (`var a *config.Placeholder`) explicitly called out to delete in the NOTE — the real helper is `waitForAuthoring`. Task 5's test is explicitly marked "ADAPT to existing test helpers" because the fake/constructor names live in files not quoted here; the behavioral assertions are concrete.

**Type consistency:** `AuthoringInfo` (model) used identically in runstore.ReadAuthoring, observeAuthoring writer, StatusOutput.Authoring, JobDetail.Authoring, and both readers. `AuthoringPath(runsDir, jobID)` signature consistent across writer + readers. `Spec.JobID` set in registry.start, read in observeAuthoring. `config.MaxAuthoringPreviewBytes` / `config.OrchestratorModel` referenced consistently.
