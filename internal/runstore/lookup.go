package runstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"pi-mcp/internal/config"
	"pi-mcp/internal/model"
)

// ErrRunNotFound is returned (wrapped) by Load when the run file does not exist
// (blind window / not-yet-created). mcpserver maps its own sentinel onto errors.Is(this).
var ErrRunNotFound = errors.New("runstore: run file not found")

// RunIDForSession scans runsDir for a run file whose sessionId == sessionID and
// returns its runId. ok=false if none matches yet (blind window). Reuses ReadRun
// (so .bak fallback + decode rules are shared). Never errors: a missing/unreadable
// dir yields ("", false).
func RunIDForSession(runsDir, sessionID string) (runID string, ok bool) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !hasJSONSuffix(n) {
			continue
		}
		r, derr := ReadRun(filepath.Join(runsDir, n))
		if derr != nil {
			continue
		}
		if r.SessionID == sessionID && sessionID != "" {
			return r.RunID, true
		}
	}
	return "", false
}

// Load reads <runsDir>/<runID>.json into a model.Run. A missing file is reported
// as a wrapped ErrRunNotFound (so callers do errors.Is(err, runstore.ErrRunNotFound)).
func Load(runsDir, runID string) (*model.Run, error) {
	path := filepath.Join(runsDir, runID+".json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load %s: %w", runID, ErrRunNotFound)
	}
	return ReadRun(path)
}

// ListItems is the cwd-relative convenience wrapper over ListRuns: it resolves the
// runs dir as <cwd>/config.RunsDirRel and returns the rows. (mcpserver.RunStore seam.)
func ListItems(cwd string, limit int) ([]model.ListItem, error) {
	out, err := ListRuns(filepath.Join(cwd, config.RunsDirRel), limit)
	if err != nil {
		return nil, err
	}
	return out.Runs, nil
}
