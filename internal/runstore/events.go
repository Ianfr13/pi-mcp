package runstore

import "pi-mcp/internal/model"

// EventsSince builds the pi_status delta: one StatusEvent per journal entry at
// POSITION >= from (journal is completion order — positions, not indexes),
// joined to its agent via journal[].Index == agents[].CallIndex (same join
// rule as Intermediates: NEVER by array position, NEVER by agent id). Entries
// with no matching agent are skipped but still consume a position, so the
// caller's "delivered = len(journal)" bookkeeping stays consistent.
//
// includeResults attaches the COMPLETE journal result, truncated to maxBytes
// (preview+truncated beyond that). Default rows carry identity+status only —
// that is the context-frugality contract.
func EventsSince(r *model.Run, from int, includeResults bool, maxBytes int) []model.StatusEvent {
	if from < 0 {
		from = 0
	}
	if from > len(r.Journal) {
		from = len(r.Journal)
	}
	byCallIndex := make(map[int]*model.Agent, len(r.Agents))
	for i := range r.Agents {
		byCallIndex[r.Agents[i].CallIndex] = &r.Agents[i]
	}
	out := make([]model.StatusEvent, 0, len(r.Journal)-from)
	for i := from; i < len(r.Journal); i++ {
		j := &r.Journal[i]
		ag, ok := byCallIndex[j.Index]
		if !ok {
			continue
		}
		ev := model.StatusEvent{Label: ag.Label, Model: ag.Model, Phase: ag.Phase, Status: "ok"}
		if ag.Status == "error" {
			ev.Status = "error"
			if ag.Error != nil {
				ev.Error = *ag.Error
			}
		}
		if includeResults {
			if maxBytes > 0 && len(j.Result) > maxBytes {
				ev.Truncated = true
				ev.Preview = truncatePreview(string(j.Result), maxBytes)
			} else {
				ev.Result = RawToAny(j.Result)
			}
		}
		out = append(out, ev)
	}
	return out
}
