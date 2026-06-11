package mcpserver

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/config"
	"pi-mcp/internal/watch"
)

// Server holds the dependencies the handlers need and tunables for long-poll.
type Server struct {
	jobs  JobsService
	store RunStore

	now          func() time.Time // injectable clock (tests)
	waitCap      time.Duration    // long-poll cap
	pollInterval time.Duration    // long-poll tick
	delta        *deltaTracker    // per-job delivery + early-warning state (delta protocol)
	// sleep is the injectable seam for the transient parse-error grace (tests
	// use a no-op; production uses time.Sleep). NOT used for the long-poll.
	sleep func(time.Duration)

	// pidAlive reports whether the job's process is alive (same session). Injected so
	// tests can simulate dead processes; defaults to "assume alive" when nil.
	pidAlive func(pid int) bool

	// subscribe is the fsnotify seam (watch.Subscribe in production; nil in
	// most tests -> pure ticker). Events are hints: the wait re-reads and
	// re-evaluates its predicate on every wake regardless of source.
	subscribe func(dir string) (<-chan struct{}, func(), error)
}

// New builds a Server with production defaults.
func New(js JobsService, store RunStore) *Server {
	return &Server{
		jobs:         js,
		store:        store,
		now:          time.Now,
		waitCap:      config.WaitCap(),
		pollInterval: defaultPollInterval,
		delta:        newDeltaTracker(),
		sleep:        time.Sleep,
		pidAlive:     func(int) bool { return true },
		subscribe:    watch.Subscribe,
	}
}

// Tool descriptions (kept terse; the rich contract lives in the spec/forcing prompt).
const (
	descWorkflow = "Delegate a TASK to the pi dynamic-workflow engine. pi decomposes the task and fans out a heterogeneous fleet of models. Returns immediately with a jobId; poll pi_status. mode=read runs in-place in cwd; mode=write runs in an isolated git worktree and returns branch+diff. Both mode and cwd are REQUIRED."
	descStatus   = "Get job progress as a compact DELTA: each call returns only the agents that finished since your previous call for this job (events[]), plus status/phase/agentsDone/agentsTotal/heartbeat. The full synthesized result arrives ONCE at status=completed. wait=true long-polls until something changes (cap 5min; env PI_MCP_WAIT_CAP). from_start=true re-delivers all events; include_results=true attaches the new events' full results (16KB cap). Query by jobId, or runId+cwd. status=stalled means no activity past the stale threshold (non-terminal; consider pi_cancel)."
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
