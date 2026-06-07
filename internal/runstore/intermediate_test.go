package runstore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

func TestIntermediates_JoinByCallIndexNotPosition(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	got := Intermediates(r, config.MaxInlineResultBytes)
	if len(got) != 4 {
		t.Fatalf("len(intermediate) = %d, want 4", len(got))
	}
	// Journal[0] has Index 0 -> must join to agent callIndex 0 ("claim A").
	// Journal[1] has Index 2 -> must join to agent callIndex 2 ("claim C"),
	// NOT to agents[1] ("claim B") and NOT via id (id=callIndex+1).
	if got[0].Label != "claim A" {
		t.Errorf("intermediate[0].Label = %q, want claim A", got[0].Label)
	}
	if got[1].Label != "claim C" {
		t.Errorf("intermediate[1].Label = %q, want claim C (journal index 2, NOT array pos 1)", got[1].Label)
	}
	if got[1].Model != "deepseek/deepseek-v4-flash" {
		t.Errorf("intermediate[1].Model = %q, want deepseek/deepseek-v4-flash", got[1].Model)
	}
	if got[1].Phase != "Scan" {
		t.Errorf("intermediate[1].Phase = %q, want Scan", got[1].Phase)
	}
	// Result must be the COMPLETE journal[].result, not the agent resultPreview.
	// Result is now `any` (decoded JSON); re-marshal to assert on the shape.
	var parsed struct {
		Claim   string `json:"claim"`
		Verdict string `json:"verdict"`
	}
	resBytes, err := json.Marshal(got[1].Result)
	if err != nil {
		t.Fatalf("intermediate[1].Result not marshalable: %v", err)
	}
	if err := json.Unmarshal(resBytes, &parsed); err != nil {
		t.Fatalf("intermediate[1].Result not valid JSON: %v", err)
	}
	if parsed.Verdict != "TRUE" || !strings.Contains(parsed.Claim, "7 is a prime") {
		t.Errorf("intermediate[1].Result = %s, want the claim-C journal result", resBytes)
	}
	if got[1].Truncated {
		t.Errorf("intermediate[1].Truncated = true, want false (under limit)")
	}
}

func TestIntermediates_Partial_Subset(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run-partial.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	got := Intermediates(r, config.MaxInlineResultBytes)
	// Only journal entries 0 and 2 exist; agent callIndex 1 is still running.
	if len(got) != 2 {
		t.Fatalf("len(intermediate) = %d, want 2", len(got))
	}
	if got[0].Label != "claim A" {
		t.Errorf("intermediate[0].Label = %q, want claim A", got[0].Label)
	}
	if got[1].Label != "claim C" {
		t.Errorf("intermediate[1].Label = %q, want claim C", got[1].Label)
	}
}

func TestIntermediates_TruncatesOverLimit(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	// Force truncation with a tiny limit.
	got := Intermediates(r, 16)
	if len(got) == 0 {
		t.Fatal("no intermediates")
	}
	for i := range got {
		if !got[i].Truncated {
			t.Errorf("intermediate[%d].Truncated = false, want true (limit 16)", i)
		}
		if got[i].Preview == "" {
			t.Errorf("intermediate[%d].Preview empty, want a preview when truncated", i)
		}
		if got[i].Result != nil {
			t.Errorf("intermediate[%d].Result not dropped when truncated: %v", i, got[i].Result)
		}
	}
}

func TestIntermediates_UnmatchedJournalSkipped(t *testing.T) {
	r := &model.Run{
		Journal: []model.JournalEntry{
			{Index: 99, Result: json.RawMessage(`{"x":1}`)}, // no agent with callIndex 99
		},
		Agents: []model.Agent{
			{CallIndex: 0, ID: 1, Label: "a", Model: "m", Phase: "P"},
		},
	}
	got := Intermediates(r, config.MaxInlineResultBytes)
	if len(got) != 0 {
		t.Errorf("len(intermediate) = %d, want 0 (journal index 99 has no matching agent)", len(got))
	}
}
