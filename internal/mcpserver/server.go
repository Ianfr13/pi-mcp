package mcpserver

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server holds the dependencies the handlers need and tunables for long-poll.
type Server struct {
	jobs  JobsService
	store RunStore

	now          func() time.Time // injectable clock (tests)
	waitCap      time.Duration    // long-poll cap
	pollInterval time.Duration    // long-poll tick

	// pidAlive reports whether the job's process is alive (same session). Injected so
	// tests can simulate dead processes; defaults to "assume alive" when nil.
	pidAlive func(pid int) bool
}

// New builds a Server with production defaults.
func New(js JobsService, store RunStore) *Server {
	return &Server{
		jobs:         js,
		store:        store,
		now:          time.Now,
		waitCap:      defaultWaitCap,
		pollInterval: defaultPollInterval,
		pidAlive:     func(int) bool { return true },
	}
}

// Tool descriptions (kept terse; the rich contract lives in the spec/forcing prompt).
const (
	descWorkflow = "Delegate a TASK to the pi dynamic-workflow engine. pi decomposes the task and fans out a heterogeneous fleet of models. Returns immediately with a jobId; poll pi_status. mode=read runs in-place in cwd; mode=write runs in an isolated git worktree and returns branch+diff. Both mode and cwd are REQUIRED."
	descStatus   = "Get live intermediate results and the final synthesized result for a job (by jobId) or run (by runId+cwd). Optional wait=true long-polls until progress or completion."
	descList     = "List recent workflow runs under <cwd>/.pi/workflows/runs (newest first)."
	descCancel   = "Cancel a running job: kill the pi process, mark aborted, and (write mode) prune the worktree/branch."
)

// Register wires all four tools onto the given mcp.Server using the typed generic AddTool.
func (s *Server) Register(m *mcp.Server) {
	mcp.AddTool(m, &mcp.Tool{Name: "pi_workflow", Description: descWorkflow}, s.handleWorkflow)
	mcp.AddTool(m, &mcp.Tool{Name: "pi_status", Description: descStatus}, s.handleStatus)
	mcp.AddTool(m, &mcp.Tool{Name: "pi_list", Description: descList}, s.handleList)
	mcp.AddTool(m, &mcp.Tool{Name: "pi_cancel", Description: descCancel}, s.handleCancel)
}

// RegisteredToolNames reports the tool names Register installs (for main/logging/tests).
func (s *Server) RegisteredToolNames() []string {
	return []string{"pi_workflow", "pi_status", "pi_list", "pi_cancel"}
}
