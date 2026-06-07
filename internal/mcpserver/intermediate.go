package mcpserver

import (
	"encoding/json"

	"pi-mcp/internal/model"
)

// buildIntermediate joins journal[].Index == agents[].CallIndex (NEVER array position,
// NEVER agents[].ID). Journal order (completion order) is preserved. Entries whose Result
// exceeds maxBytes are returned truncated (Preview + Truncated=true, full Result omitted).
func buildIntermediate(run *model.Run, maxBytes int) []model.IntermediateResult {
	byCall := make(map[int]model.Agent, len(run.Agents))
	for _, a := range run.Agents {
		byCall[a.CallIndex] = a
	}
	out := make([]model.IntermediateResult, 0, len(run.Journal))
	for _, j := range run.Journal {
		a, ok := byCall[j.Index] // join key
		if !ok {
			continue // orphan journal entry (no matching agent) -> skip
		}
		ir := model.IntermediateResult{
			Label: a.Label,
			Model: a.Model,
			Phase: a.Phase,
		}
		if len(j.Result) > maxBytes {
			ir.Truncated = true
			ir.Preview = preview(j.Result, maxBytes)
			// Result omitted when truncated.
		} else {
			// Unmarshal the stored json.RawMessage into `any` so the OUTPUT struct
			// carries a real object/array/scalar that passes go-sdk output-schema
			// validation (json.RawMessage reflects to "null|array" and is rejected).
			ir.Result = rawToAny(j.Result)
		}
		out = append(out, ir)
	}
	return out
}

func preview(raw json.RawMessage, maxBytes int) string {
	s := string(raw)
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	return s[:maxBytes]
}

// buildMetadata computes the model histogram + agentCount and passes tokenUsage/durationMs
// through VERBATIM (nil mid-run so cost is suppressed until the run finishes).
func buildMetadata(run *model.Run) *model.StatusMetadata {
	byModel := make(map[string]int)
	for _, a := range run.Agents {
		if a.Model != "" {
			byModel[a.Model]++
		}
	}
	return &model.StatusMetadata{
		ByModel:    byModel,
		AgentCount: len(run.Agents),
		TokenUsage: run.TokenUsage, // verbatim; nil mid-run
		DurationMs: run.DurationMs, // nil mid-run
	}
}
