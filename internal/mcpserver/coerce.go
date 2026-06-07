package mcpserver

import (
	"encoding/json"
)

// coerceResult applies the §5.4 best-effort output-contract coercion.
// Returns the coerced JSON and a warn flag (true when a result was expected but missing).
// It NEVER discards original data.
func coerceResult(raw json.RawMessage, mcpStatus string) (json.RawMessage, bool) {
	if len(raw) == 0 {
		if mcpStatus == "completed" {
			// (d) absent in completed -> {summary:""} + warn
			return json.RawMessage(`{"summary":""}`), true
		}
		// mid-run: nothing to coerce yet
		return nil, false
	}

	// Try as object first.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if _, hasSummary := obj["summary"]; hasSummary {
			// (a) already in shape -> passthrough
			return raw, false
		}
		// (c) different keys -> add summary, preserve originals.
		summary := synthSummary(obj)
		obj["summary"] = mustJSON(summary)
		return mustJSON(obj), false
	}

	// Try as a JSON string scalar.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return mustJSON(map[string]string{"summary": s}), false
	}

	// Any other scalar (number/bool/array): stringify into summary, preserve raw alongside.
	return mustJSON(map[string]json.RawMessage{
		"summary": mustJSON(string(raw)),
		"value":   raw,
	}), false
}

// synthSummary picks a human summary string from common keys, else a compact synopsis.
func synthSummary(obj map[string]json.RawMessage) string {
	for _, k := range []string{"overall", "summary_text", "conclusion", "result"} {
		if v, ok := obj[k]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
		}
	}
	// fallback: list the keys present so nothing is silently lost.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	b, _ := json.Marshal(keys)
	return "result with keys " + string(b)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

// rawToAny decodes a stored json.RawMessage into an `any` (object/array/scalar)
// for placement in an OUTPUT struct. The OUTPUT fields are `any` (not
// json.RawMessage) so go-sdk reflects an unconstrained schema and validates the
// real value; raw bytes would reflect to "null|array" and be rejected. Empty or
// undecodable raw yields nil (the omitempty field is dropped).
func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}
