package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"pi-mcp/internal/jobs"
	"pi-mcp/internal/parser"
	"pi-mcp/internal/runner"
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

	// Tee proc.Stdout: ParseStream reads the canonical stream while a peek
	// goroutine scans the teed copy for the first `session` event and pushes its
	// id onto sessionCh during the blind window (before wait() is invoked).
	pr, pw := io.Pipe()
	tee := io.TeeReader(proc.Stdout, pw)

	go peekSessionID(pr, sessionCh)

	type outcome struct {
		res parser.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, perr := parser.ParseStream(ctx, tee)
		// Closing the pipe writer unblocks the peek goroutine at EOF.
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

// peekSessionID scans r for the first `session` event and pushes its id onto ch,
// then drains the rest so the TeeReader writer never blocks. It is safe to call
// even when no session event ever arrives.
func peekSessionID(r io.Reader, ch chan<- string) {
	// A bufio.Reader with ReadBytes('\n') handles arbitrarily long lines, matching
	// parser.ParseStream's reader exactly. Both readers MUST consume every line to
	// EOF at the same threshold or the TeeReader writer deadlocks on a line one
	// reader accepts and the other rejects.
	br := bufio.NewReader(r)
	pushed := false
	for {
		line, err := br.ReadBytes('\n')
		if !pushed {
			trimmed := bytes.TrimRight(line, "\n")
			trimmed = bytes.TrimRight(trimmed, "\r")
			if len(trimmed) > 0 {
				var ev struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				}
				if json.Unmarshal(trimmed, &ev) == nil &&
					ev.Type == "session" && ev.ID != "" {
					ch <- ev.ID
					pushed = true
				}
			}
		}
		if err != nil {
			// io.EOF (clean drain) or a read error: stop. The parser goroutine
			// owns the authoritative result; we have already drained to EOF.
			return
		}
	}
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
