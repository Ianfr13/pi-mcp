package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"pi-mcp/internal/config"
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/model"
	"pi-mcp/internal/parser"
	"pi-mcp/internal/runner"
	"pi-mcp/internal/runstore"
)

// realLauncher is the production jobs.Launcher: it renders the forcing prompt,
// spawns pi, drains stdout through the parser, and surfaces the run outcome.
type realLauncher struct{}

// Launch starts a pi subprocess for spec and returns promptly once the PID is
// known. The session id from the first `session` event is pushed onto sessionCh
// during the blind window (before wait() is called). wait() blocks for the
// process to exit and the stream to be parsed, surfacing NO_WORKFLOW_RUN or a
// workflow isError as a non-nil error.
func (realLauncher) Launch(ctx context.Context, spec jobs.Spec) (int, <-chan string, func() error, error) {
	prompt, err := runner.RenderPrompt(string(spec.Mode), spec.Task, spec.Context)
	if err != nil {
		return 0, nil, nil, err
	}

	proc, err := runner.Spawn(ctx, runner.SpawnConfig{
		Prompt:         prompt,
		NoContextFiles: false,
		CWD:            spec.CWD,
		Stderr:         os.Stderr,
	})
	if err != nil {
		return 0, nil, nil, err
	}

	sessionCh := make(chan string, 1)

	// Tee proc.Stdout: ParseStream reads the canonical stream while observeAuthoring
	// scans the teed copy for the first `session` event (pushed onto sessionCh) and
	// the orchestrator's authoring (persisted to the <jobID>.authoring file).
	pr, pw := io.Pipe()
	tee := io.TeeReader(proc.Stdout, pw)

	go observeAuthoring(pr, sessionCh, spec)

	type outcome struct {
		res parser.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, perr := parser.ParseStream(ctx, tee)
		// Closing the pipe writer unblocks the observe goroutine at EOF.
		_ = pw.Close()
		done <- outcome{res: res, err: perr}
	}()

	wait := func() error {
		o := <-done
		waitErr := proc.Wait()
		return composeWaitError(o.res, o.err, waitErr)
	}

	return proc.PID(), sessionCh, wait, nil
}

// authoringLine is the minimal stream shape observeAuthoring decodes per line.
type authoringLine struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	ToolName string          `json:"toolName"`
	Message  json.RawMessage `json:"message"`
	Args     json.RawMessage `json:"args"`
}

// observeAuthoring drains r (a teed copy of pi stdout) to EOF — ALWAYS, never
// early-returns. It shares an io.Pipe with parser.ParseStream; an early return
// would block the pipe writer and deadlock wait(). It pushes the first sessionId,
// accumulates the orchestrator's authoring (assistant text/thinking + the workflow
// script) and hands latest-wins snapshots to a decoupled writer goroutine that
// persists <RunsDir>/<jobID>.authoring (so disk I/O never back-pressures the
// parser). The file is deleted exactly once when this function returns (clean EOF
// or the child dying and EOFing the stream).
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
			close(snapCh) // writer drains its last snapshot, then exits
			<-writerDone  // ...so the delete below can't be clobbered by a late write
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
			UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
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
					snapshot(true) // authoring done; KEEP reading to EOF
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
// payload. Only role=="assistant" content contributes (role=="user" is pi-mcp's
// own forcing prompt; "toolResult" is the finished result). It concats each
// content block's .text (type text) and .thinking (type thinking).
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

// appendPreview appends s to buf and tail-truncates to MaxAuthoringPreviewBytes on
// a UTF-8 rune boundary (keep the most recent content).
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

// composeWaitError turns the parser result, parser error, and process exit error
// into a single wait() error. A workflow that ran cleanly (no parse error, no
// NO_WORKFLOW_RUN, not isError) yields nil regardless of a non-zero exit code,
// because the parser/runstore make the authoritative success determination.
func composeWaitError(res parser.Result, parseErr, waitErr error) error {
	if parseErr != nil {
		return fmt.Errorf("pi stream parse: %w", parseErr)
	}
	if code := res.Err(); code != "" {
		// NO_WORKFLOW_RUN: pi answered directly without running a workflow.
		return fmt.Errorf("%s", code)
	}
	if res.IsError {
		msg := res.RawText
		if msg == "" {
			msg = "workflow reported an error"
		}
		return fmt.Errorf("%s", msg)
	}
	// Workflow ran and reported success; the exit code is advisory only.
	_ = waitErr
	return nil
}
