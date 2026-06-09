package runstore

import "pi-mcp/internal/model"

// Metadata builds the StatusMetadata block for pi_status: the by_model histogram,
// agent count, and (only when present in the run file) token usage and duration.
// TokenUsage and Cost are written by pi only at the end of a run, so they remain
// nil mid-run — Metadata never synthesizes or recomputes them (Cost is verbatim).
func Metadata(r *model.Run) model.StatusMetadata {
	return model.StatusMetadata{
		ByModel:    ModelHistogram(r),
		AgentCount: len(r.Agents),
		TokenUsage: r.TokenUsage, // nil until the run ends
		DurationMs: r.DurationMs, // nil until the run ends
	}
}
