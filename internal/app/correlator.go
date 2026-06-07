package app

import "pi-mcp/internal/runstore"

// realCorrelator implements jobs.Correlator by scanning the runs directory for a
// run whose sessionId matches and returning its runId.
type realCorrelator struct{}

func (realCorrelator) RunIDForSession(runsDir, sessionID string) (string, bool) {
	return runstore.RunIDForSession(runsDir, sessionID)
}
