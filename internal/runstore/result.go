package runstore

import (
	"encoding/json"

	"pi-mcp/internal/model"
)

// StatusCompleted is the run-file status string for a finished run. runstore
// treats .result as authoritative only at this status (per §5.2/§7).
const StatusCompleted = "completed"

// AuthoritativeResult returns the run-file .result and true only when the run
// has completed and carries a non-empty result. Mid-run (running/paused/failed)
// the .result is not authoritative, so ok is false and callers should fall back
// to the live stream parser. The returned RawMessage aliases r.Result.
func AuthoritativeResult(r *model.Run) (json.RawMessage, bool) {
	if r.Status != StatusCompleted {
		return nil, false
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return nil, false
	}
	return r.Result, true
}

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
