package parser

import (
	"encoding/json"
	"strings"
)

const jsonFenceOpen = "```json"
const fence = "```"

// extractWorkflowResult turns the workflow tool_execution_end content[0].text into
// a json.RawMessage. Preference order:
//  1. The first fenced ```json ... ``` block, if its inner bytes are valid JSON -> verbatim.
//  2. The text with the leading "✓ Workflow ... finished" header line stripped,
//     wrapped as a JSON string.
//  3. The whole text wrapped as a JSON string.
//
// The return value is always valid JSON.
func extractWorkflowResult(text string) json.RawMessage {
	if block, ok := fencedJSONBlock(text); ok {
		trimmed := strings.TrimSpace(block)
		if json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed)
		}
	}
	body := stripHeaderLine(text)
	b, _ := json.Marshal(body) // string marshal never errors
	return json.RawMessage(b)
}

// fencedJSONBlock returns the inner content of the first ```json ... ``` block.
func fencedJSONBlock(text string) (string, bool) {
	start := strings.Index(text, jsonFenceOpen)
	if start < 0 {
		return "", false
	}
	// Move past "```json" and an optional single newline.
	inner := text[start+len(jsonFenceOpen):]
	inner = strings.TrimPrefix(inner, "\n")
	end := strings.Index(inner, fence)
	if end < 0 {
		return "", false
	}
	return inner[:end], true
}

// stripHeaderLine removes a leading "✓ Workflow ... finished ..." header line
// (everything up to and including the first newline) when present. If the text has
// no header marker, it is returned unchanged. If there is a header but no trailing
// newline, the whole text is the header and an empty body is returned.
func stripHeaderLine(text string) string {
	if !strings.HasPrefix(text, "✓ Workflow") {
		return text
	}
	if nl := strings.IndexByte(text, '\n'); nl >= 0 {
		return text[nl+1:]
	}
	return ""
}
