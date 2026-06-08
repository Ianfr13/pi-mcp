package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"pi-mcp/internal/model"
)

// AuthoringPath is the canonical path of the blind-window authoring file for
// a job: <runsDir>/<jobID>.authoring. The .authoring extension is deliberately
// distinct from the .json/.bak/.tmp run-file vocabulary so list.go's
// hasJSONSuffix filter excludes it for free (verified by
// TestListRuns_IgnoresAuthoring).
func AuthoringPath(runsDir, jobID string) string {
	return filepath.Join(runsDir, jobID+".authoring")
}

// ReadAuthoring decodes the authoring file for jobID into a *model.AuthoringInfo.
// It returns (nil, false) — never an error — when the file is missing, the
// jobID is empty, or the JSON is corrupt: the consumer treats authoring as
// simply "not available" rather than a hard failure (the dashboard / pi_status
// already gate on the blind-window state, so a missing/corrupt file is a
// graceful no-op).
func ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool) {
	if jobID == "" {
		return nil, false
	}
	b, err := os.ReadFile(AuthoringPath(runsDir, jobID))
	if err != nil {
		return nil, false
	}
	var info model.AuthoringInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, false
	}
	// Treat a degenerate (zero-time) UpdatedAt as not available so a half-written
	// file that has not yet been refreshed still surfaces as absent.
	if info.UpdatedAt == "" {
		return nil, false
	}
	if _, perr := time.Parse(time.RFC3339Nano, info.UpdatedAt); perr != nil {
		return nil, false
	}
	return &info, true
}
