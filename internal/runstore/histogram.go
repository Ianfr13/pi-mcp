package runstore

import "pi-mcp/internal/model"

// ModelHistogram counts agents per model id, keyed by the verbatim agents[].model
// string (e.g. "deepseek/deepseek-v4-flash"). Agents with an empty model string
// are skipped so no "" bucket appears. Returns a non-nil (possibly empty) map.
func ModelHistogram(r *model.Run) map[string]int {
	h := make(map[string]int, len(r.Agents))
	for i := range r.Agents {
		m := r.Agents[i].Model
		if m == "" {
			continue
		}
		h[m]++
	}
	return h
}
