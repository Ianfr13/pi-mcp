package runstore

import (
	"path/filepath"
	"testing"

	"pi-mcp/internal/model"
)

var modelRunNoAgents = model.Run{RunID: "empty", Status: "running"}

func TestModelHistogram_Canonical(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	got := ModelHistogram(r)
	want := map[string]int{
		"deepseek/deepseek-v4-flash": 3, // the spec's {flash:3}
		"openai-codex/gpt-5.5":       1, // the spec's {gpt-5.5:1}
	}
	if len(got) != len(want) {
		t.Fatalf("histogram = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("histogram[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestModelHistogram_SkipsEmptyModel(t *testing.T) {
	// Partial run: running agent (callIndex 1) still has a model; agents with
	// empty model strings must not create a "" bucket.
	r, err := ReadRun(filepath.Join("testdata", "sample-run-partial.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	got := ModelHistogram(r)
	if _, ok := got[""]; ok {
		t.Errorf("histogram has empty-string key: %v", got)
	}
	if got["deepseek/deepseek-v4-flash"] != 3 {
		t.Errorf("histogram[flash] = %d, want 3", got["deepseek/deepseek-v4-flash"])
	}
}

func TestModelHistogram_Empty(t *testing.T) {
	got := ModelHistogram(&modelRunNoAgents)
	if len(got) != 0 {
		t.Errorf("histogram = %v, want empty map", got)
	}
}
