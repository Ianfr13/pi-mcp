package app

import (
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/mcpserver"
)

// Compile-time proof the production adapters satisfy the package seams they wire.
var (
	_ jobs.Launcher         = realLauncher{}
	_ jobs.Correlator       = realCorrelator{}
	_ jobs.Pruner           = worktreePruner{}
	_ mcpserver.JobsService = (*jobsAdapter)(nil)
	_ mcpserver.RunStore    = runStoreAdapter{}
)
