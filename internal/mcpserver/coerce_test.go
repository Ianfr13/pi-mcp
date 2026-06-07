package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestCoerceResult(t *testing.T) {
	// (a) object already in read shape -> passthrough (summary preserved)
	a := json.RawMessage(`{"summary":"ok","findings":[],"confidence":"high","open_questions":[]}`)
	gotA, warnA := coerceResult(a, "completed")
	if warnA {
		t.Fatalf("(a) should not warn")
	}
	var ma map[string]any
	_ = json.Unmarshal(gotA, &ma)
	if ma["summary"] != "ok" {
		t.Fatalf("(a) summary lost: %s", gotA)
	}

	// (b) bare string scalar -> {summary:<raw>}
	b := json.RawMessage(`"just a string"`)
	gotB, _ := coerceResult(b, "completed")
	var mb map[string]any
	if err := json.Unmarshal(gotB, &mb); err != nil {
		t.Fatalf("(b) not an object: %s", gotB)
	}
	if mb["summary"] != "just a string" {
		t.Fatalf("(b) want summary=raw, got %s", gotB)
	}

	// (c) object with different keys (fixture {claims,overall}) -> summary added, originals preserved
	c := json.RawMessage(`{"claims":[1,2,3],"overall":"two true one false"}`)
	gotC, _ := coerceResult(c, "completed")
	var mc map[string]any
	_ = json.Unmarshal(gotC, &mc)
	if _, ok := mc["claims"]; !ok {
		t.Fatalf("(c) original key 'claims' dropped: %s", gotC)
	}
	if _, ok := mc["overall"]; !ok {
		t.Fatalf("(c) original key 'overall' dropped: %s", gotC)
	}
	if _, ok := mc["summary"]; !ok {
		t.Fatalf("(c) summary not synthesized: %s", gotC)
	}
	// 'overall' is used as the summary text when present.
	if mc["summary"] != "two true one false" {
		t.Fatalf("(c) summary should reuse overall: %s", gotC)
	}

	// (d) absent result in completed -> {summary:""} + warn
	gotD, warnD := coerceResult(nil, "completed")
	if !warnD {
		t.Fatalf("(d) expected warn")
	}
	var md map[string]any
	_ = json.Unmarshal(gotD, &md)
	if md["summary"] != "" {
		t.Fatalf("(d) want empty summary, got %s", gotD)
	}

	// non-completed + nil -> nil passthrough (no synthesis mid-run)
	if got, _ := coerceResult(nil, "running"); got != nil {
		t.Fatalf("running+nil should stay nil, got %s", got)
	}
}
