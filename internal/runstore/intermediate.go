package runstore

import (
	"pi-mcp/internal/model"
)

// Intermediates builds the live intermediate[] list for pi_status by joining
// each journal entry to its agent via journal[].Index == agents[].CallIndex
// (NEVER by array position — journal is in completion order; NEVER by
// agents[].ID, which equals CallIndex+1). The order of the returned slice
// follows journal order (completion order). Journal entries with no matching
// agent are skipped.
//
// Each entry carries the COMPLETE journal[].result. When that result exceeds
// maxBytes, the raw Result is dropped and a truncated UTF-8-safe Preview is set
// with Truncated=true.
func Intermediates(r *model.Run, maxBytes int) []model.IntermediateResult {
	byCallIndex := make(map[int]*model.Agent, len(r.Agents))
	for i := range r.Agents {
		byCallIndex[r.Agents[i].CallIndex] = &r.Agents[i]
	}

	out := make([]model.IntermediateResult, 0, len(r.Journal))
	for i := range r.Journal {
		j := &r.Journal[i]
		ag, ok := byCallIndex[j.Index] // JOIN: index == callIndex
		if !ok {
			continue
		}
		ir := model.IntermediateResult{
			Label: ag.Label,
			Model: ag.Model,
			Phase: ag.Phase,
		}
		if maxBytes > 0 && len(j.Result) > maxBytes {
			ir.Truncated = true
			ir.Preview = truncatePreview(string(j.Result), maxBytes)
			// Result left empty when truncated.
		} else {
			ir.Result = j.Result
		}
		out = append(out, ir)
	}
	return out
}

// truncatePreview returns s clipped to at most n bytes without splitting a
// multi-byte UTF-8 rune.
func truncatePreview(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // mid-rune continuation byte
		cut--
	}
	return s[:cut]
}
