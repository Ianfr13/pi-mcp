package mcpserver

import (
	"encoding/json"

	"pi-mcp/internal/model"
)

// buildRun mirrors the out-of-order sample-run.json shape (journal completion order 0,2,1,3).
// Shared run fixture for the handler/status tests.
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
