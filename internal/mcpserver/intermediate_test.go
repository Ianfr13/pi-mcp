package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"pi-mcp/internal/model"
)

// buildRun mirrors the out-of-order sample-run.json shape (journal completion order 0,2,1,3).
func buildRun() *model.Run {
	return &model.Run{
		RunID:        "mq40rdpt-yij9hj",
		SessionID:    "019ea2fe-db76-7e31-85ad-9718d3fbc23a",
		WorkflowName: "judge_claims",
		Status:       "completed",
		CurrentPhase: strptr("Final"),
		Phases:       []string{"Scan", "Final"},
		Agents: []model.Agent{
			{ID: 1, CallIndex: 0, Label: "claim A", Model: "deepseek/deepseek-v4-flash", Phase: "Scan", Status: "done", ResultPreview: "{\"claim\":\"A...", Tokens: 31514},
			{ID: 2, CallIndex: 1, Label: "claim B", Model: "deepseek/deepseek-v4-flash", Phase: "Scan", Status: "done", ResultPreview: "{\"claim\":\"B...", Tokens: 31561},
			{ID: 3, CallIndex: 2, Label: "claim C", Model: "deepseek/deepseek-v4-flash", Phase: "Scan", Status: "done", ResultPreview: "{\"claim\":\"C...", Tokens: 31568},
			{ID: 4, CallIndex: 3, Label: "final synthesis", Model: "openai-codex/gpt-5.5", Phase: "Final", Status: "done", ResultPreview: "{\"claims\":...", Tokens: 25826},
		},
		Journal: []model.JournalEntry{
			{Index: 0, Result: json.RawMessage(`{"claim":"A","verdict":"TRUE"}`)},
			{Index: 2, Result: json.RawMessage(`{"claim":"C","verdict":"TRUE"}`)},
			{Index: 1, Result: json.RawMessage(`{"claim":"B","verdict":"FALSE"}`)},
			{Index: 3, Result: json.RawMessage(`{"claims":[],"overall":"done"}`)},
		},
		Result:     json.RawMessage(`{"claims":[1,2,3],"overall":"two true one false"}`),
		TokenUsage: &model.TokenUsage{Input: 116075, Output: 938, Total: 120469, Cost: 0.1463847},
		DurationMs: i64ptr(22391),
	}
}

func TestBuildIntermediate_JoinByIndex(t *testing.T) {
	got := buildIntermediate(buildRun(), 1024)
	if len(got) != 4 {
		t.Fatalf("want 4 intermediate, got %d", len(got))
	}
	// journal index 2 must join agent callIndex 2 == "claim C", NOT array position 1.
	second := got[1] // journal completion order -> second completed is index 2
	if second.Label != "claim C" {
		t.Fatalf("join wrong: want 'claim C' got %q", second.Label)
	}
	if second.Model != "deepseek/deepseek-v4-flash" || second.Phase != "Scan" {
		t.Fatalf("join wrong model/phase: %+v", second)
	}
	// Result is now `any` (decoded JSON object), not raw bytes.
	r, ok := second.Result.(map[string]any)
	if !ok || r["claim"] != "C" {
		t.Fatalf("result wrong for index 2: %#v", second.Result)
	}
	if second.Truncated {
		t.Fatalf("short result should not be truncated")
	}
}

func TestBuildIntermediate_Truncation(t *testing.T) {
	run := buildRun()
	big := `{"x":"` + strings.Repeat("z", 200) + `"}`
	run.Journal = []model.JournalEntry{{Index: 0, Result: json.RawMessage(big)}}
	run.Agents = run.Agents[:1]
	got := buildIntermediate(run, 16) // tiny limit forces truncation
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if !got[0].Truncated {
		t.Fatalf("expected Truncated=true")
	}
	if got[0].Preview == "" {
		t.Fatalf("expected non-empty Preview when truncated")
	}
	if got[0].Result != nil {
		t.Fatalf("truncated entry should omit full Result, got %v", got[0].Result)
	}
}

func TestBuildIntermediate_OrphanJournalSkipped(t *testing.T) {
	run := buildRun()
	// journal index 9 has no matching agent; must be skipped, not panic.
	run.Journal = append(run.Journal, model.JournalEntry{Index: 9, Result: json.RawMessage(`{}`)})
	got := buildIntermediate(run, 1024)
	if len(got) != 4 {
		t.Fatalf("orphan journal entry must be skipped, got %d", len(got))
	}
}

func TestBuildMetadata(t *testing.T) {
	md := buildMetadata(buildRun())
	if md.AgentCount != 4 {
		t.Fatalf("agentCount want 4 got %d", md.AgentCount)
	}
	if md.ByModel["deepseek/deepseek-v4-flash"] != 3 || md.ByModel["openai-codex/gpt-5.5"] != 1 {
		t.Fatalf("by_model histogram wrong: %v", md.ByModel)
	}
	if md.TokenUsage == nil || md.TokenUsage.Cost != 0.1463847 {
		t.Fatalf("cost must be verbatim, got %+v", md.TokenUsage)
	}
	if md.DurationMs == nil || *md.DurationMs != 22391 {
		t.Fatalf("durationMs wrong: %+v", md.DurationMs)
	}
}

func TestBuildMetadata_SuppressCostMidRun(t *testing.T) {
	run := buildRun()
	run.TokenUsage = nil // mid-run: cost/tokens absent
	run.DurationMs = nil
	md := buildMetadata(run)
	if md.TokenUsage != nil {
		t.Fatalf("tokenUsage must stay nil mid-run")
	}
	if md.AgentCount != 4 {
		t.Fatalf("agentCount should still count agents: %d", md.AgentCount)
	}
}
