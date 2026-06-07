package runstore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pi-mcp/internal/model"
)

// ListRuns scans dir for *.json run files (excluding *.bak and *.tmp), decodes
// each (with .bak fallback on corrupt), and returns ListItems sorted by
// updatedAt descending (ties broken by runId for determinism), capped at limit.
// A missing dir yields an empty list and no error. Files that fail to decode and
// have no usable .bak are skipped (best-effort listing).
func ListRuns(dir string, limit int) (model.ListOutput, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return model.ListOutput{Runs: []model.ListItem{}}, nil
		}
		return model.ListOutput{}, err
	}

	type scored struct {
		item    model.ListItem
		updated time.Time
	}
	var rows []scored
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !hasJSONSuffix(name) {
			continue // excludes non-json plus *.json.bak / *.json.tmp
		}
		r, derr := ReadRun(filepath.Join(dir, name))
		if derr != nil {
			continue // corrupt without recoverable .bak — skip
		}
		rows = append(rows, scored{item: toListItem(r), updated: updatedAt(r)})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].updated.Equal(rows[j].updated) {
			return rows[i].item.RunID > rows[j].item.RunID // deterministic tie-break
		}
		return rows[i].updated.After(rows[j].updated)
	})

	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	out := model.ListOutput{Runs: make([]model.ListItem, 0, len(rows))}
	for i := range rows {
		out.Runs = append(out.Runs, rows[i].item)
	}
	return out, nil
}

// hasJSONSuffix reports whether name is a run-file candidate: a *.json file that
// is neither a *.bak snapshot nor a *.tmp in-progress write.
func hasJSONSuffix(name string) bool {
	return strings.HasSuffix(name, ".json") &&
		!strings.HasSuffix(name, ".bak") &&
		!strings.HasSuffix(name, ".tmp")
}

func updatedAt(r *model.Run) time.Time {
	if r.UpdatedAt != nil {
		return *r.UpdatedAt
	}
	return time.Time{} // missing updatedAt sorts last
}

// toListItem projects a Run into the pi_list row. Cost is the verbatim
// tokenUsage.cost (nil when tokenUsage is omitted, e.g. running/failed runs).
func toListItem(r *model.Run) model.ListItem {
	li := model.ListItem{
		RunID:        r.RunID,
		WorkflowName: r.WorkflowName,
		Status:       r.Status,
		AgentCount:   len(r.Agents),
		ByModel:      ModelHistogram(r),
		DurationMs:   r.DurationMs,
		CompletedAt:  r.CompletedAt,
	}
	if r.TokenUsage != nil {
		c := r.TokenUsage.Cost
		li.Cost = &c
	}
	return li
}
