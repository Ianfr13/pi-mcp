// Package parser parses the pi -p --mode json stdout stream (line-delimited
// JSON). It scans line-by-line, switches on the event .type, ignores unknown
// types and malformed lines, captures the first session event id, and detects
// the workflow tool_execution_end event to extract the final workflow result.
//
// The parser deliberately does NOT import internal/model: it owns a minimal
// private line-decode envelope (spec §6/§7). It returns its own small Result
// struct. The single cross-package dependency is internal/config for the
// NO_WORKFLOW_RUN error-code string.
package parser

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"pi-mcp/internal/config"
)

const maxLineBytes = 1 << 20 // 1MB scanner buffer (longest fixture line ~13.5KB)

// Result is the outcome of parsing a full pi -p --mode json stream.
type Result struct {
	// SessionID is the id from the first `session` event (== Run.SessionID).
	SessionID string
	// WorkflowFound is true iff a tool_execution_end(toolName="workflow") was seen.
	WorkflowFound bool
	// IsError mirrors that event's isError flag.
	IsError bool
	// RawText is content[0].text of the workflow tool_execution_end (empty if none).
	RawText string
	// Result is the extracted workflow result: the fenced ```json``` block parsed
	// from RawText, or the raw text JSON-string-wrapped when no/!invalid fenced
	// block. It is nil when no workflow event was seen.
	Result json.RawMessage
}

// streamEvent is the minimal envelope decoded per JSONL line. Extra fields on the
// wire are ignored by encoding/json. Unknown .type values match no case below.
type streamEvent struct {
	Type     string      `json:"type"`
	ID       string      `json:"id"`       // session.id (top-level)
	ToolName string      `json:"toolName"` // tool_execution_* events
	IsError  bool        `json:"isError"`  // tool_execution_end
	Result   *toolResult `json:"result"`   // tool_execution_end
}

type toolResult struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseStream reads JSONL from r, scanning line-by-line. Unknown event types and
// malformed individual lines are ignored (resilient stream parsing). It returns a
// non-nil error ONLY on a fatal read error from r. Use Result.WorkflowFound (or
// Result.Err) to detect NO_WORKFLOW_RUN.
//
// ParseStream tolerates an io.Reader that is being drained concurrently with
// proc.Wait(): it simply reads to EOF.
func ParseStream(ctx context.Context, r io.Reader) (Result, error) {
	var res Result
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			// Malformed line: ignore (resilient stream parsing), do not abort.
			continue
		}
		switch ev.Type {
		case "session":
			if res.SessionID == "" {
				res.SessionID = ev.ID
			}
		case "tool_execution_end":
			if ev.ToolName == "workflow" {
				res.WorkflowFound = true
				res.IsError = ev.IsError
				if ev.Result != nil && len(ev.Result.Content) > 0 {
					res.RawText = ev.Result.Content[0].Text
				}
				res.Result = extractWorkflowResult(res.RawText)
			}
		default:
			// Unknown / uninteresting type: ignore.
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	return res, nil
}

// Err returns config.ErrNoWorkflowRun ("NO_WORKFLOW_RUN") when the stream
// contained no tool_execution_end(toolName="workflow"); otherwise "" (no
// parser-level error). Note: IsError (a workflow that ran but failed) is surfaced
// by the caller, not here.
func (r Result) Err() string {
	if !r.WorkflowFound {
		return config.ErrNoWorkflowRun
	}
	return ""
}
