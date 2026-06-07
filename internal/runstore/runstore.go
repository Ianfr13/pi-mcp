// Package runstore reads pi workflow run files from <cwd>/.pi/workflows/runs/*.json,
// decodes them into model.Run, and derives the views the MCP tools need: the
// authoritative .result (when completed), the by_model histogram, the live
// intermediate[] list (joining journal[].Index == agents[].CallIndex), and the
// pi_list listing (sorted by updatedAt desc). It performs no status mapping and
// no liveness checks; those belong to internal/jobs.
package runstore

import (
	"encoding/json"
	"fmt"
	"os"

	"pi-mcp/internal/model"
)

// ReadRun decodes a single run file at path into a *model.Run. If the primary
// file is missing or contains invalid JSON, it transparently falls back to a
// sibling "<path>.bak" when one exists (pi writes .bak snapshots). It never
// reads *.tmp. The returned Run has optional fields left nil/empty when omitted.
func ReadRun(path string) (*model.Run, error) {
	r, err := decodeRunFile(path)
	if err == nil {
		return r, nil
	}
	// Fallback: sibling .bak (corrupt or missing primary).
	bak := path + ".bak"
	if r2, err2 := decodeRunFile(bak); err2 == nil {
		return r2, nil
	}
	return nil, fmt.Errorf("runstore: read %s: %w", path, err)
}

func decodeRunFile(path string) (*model.Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r model.Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &r, nil
}
