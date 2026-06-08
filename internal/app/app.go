// Package app wires config + the job registry + the MCP tool handlers and serves
// them over stdio. It is split out of main so the startup sequence
// (build registry -> reconcile -> register tools -> serve) is unit-testable with
// fakes, without spawning the real pi subprocess.
package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/config"
	"pi-mcp/internal/jobs"
	"pi-mcp/internal/mcpserver"
)

const (
	serverName    = "pi-mcp"
	serverVersion = "v0.1.0"
)

// Deps carries the app's collaborators. The function fields are seams that
// default (via fill) to the real implementations; tests override them.
type Deps struct {
	Logger *log.Logger

	// buildRegistry constructs the job registry (with the production launcher /
	// correlator / pruner wired in). Tests inject a registry over noop deps.
	buildRegistry func(Deps) (*jobs.Registry, error)
	// newServer constructs a fresh go-sdk MCP server. Tests reuse the production
	// constructor; production fills it with the default below.
	newServer func() *mcp.Server
	// serve runs the MCP server (production: over stdio). Tests stub it to assert
	// the startup sequence without binding stdin/stdout.
	serve func(ctx context.Context, m *mcp.Server) error
}

// fill replaces nil seam fields with their production implementations.
func (d Deps) fill() Deps {
	if d.Logger == nil {
		d.Logger = log.New(os.Stderr, "pi-mcp ", log.LstdFlags)
	}
	if d.buildRegistry == nil {
		d.buildRegistry = buildRegistryReal
	}
	if d.newServer == nil {
		d.newServer = newServerReal
	}
	if d.serve == nil {
		d.serve = serveStdio
	}
	return d
}

// Run is the production entrypoint: build the registry, run startup
// reconciliation (recoverable on error), register the four MCP tools, and serve.
func Run(ctx context.Context, in Deps) error {
	d := in.fill()

	reg, err := d.buildRegistry(d)
	if err != nil {
		return fmt.Errorf("build registry: %w", err)
	}
	defer func() { _ = reg.Close() }()

	if n, rerr := reg.Reconcile(ctx); rerr != nil {
		d.Logger.Printf("startup reconcile error: %v (continuing)", rerr)
	} else {
		d.Logger.Printf("startup reconciled %d job record(s)", n)
	}

	js := &jobsAdapter{reg: reg}
	rs := runStoreAdapter{}
	srv := mcpserver.New(js, rs)

	m := d.newServer()
	srv.Register(m)
	d.Logger.Printf("serving %s %s over stdio (tools=%v)", serverName, serverVersion, srv.RegisteredToolNames())

	if err := d.serve(ctx, m); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// buildRegistryReal constructs the production registry with the runner-backed
// launcher, runstore-backed correlator, and worktree-backed pruner.
func buildRegistryReal(Deps) (*jobs.Registry, error) {
	persist, wtRoot, err := statePaths()
	if err != nil {
		return nil, err
	}
	return jobs.NewRegistry(
		jobs.Config{
			Cap:          config.DefaultConcurrencyCap,
			PersistPath:  persist,
			WorktreeRoot: wtRoot,
		},
		realLauncher{},
		realCorrelator{},
		worktreePruner{},
	)
}

// statePaths resolves the registry persist file and the worktree scan root under
// the XDG state dir (mirroring the worktree package's base-dir resolution).
func statePaths() (persist, worktreeRoot string, err error) {
	base := config.StateDir()
	root := filepath.Join(base, config.WorktreeSubdir)
	if mkErr := os.MkdirAll(root, 0o755); mkErr != nil {
		return "", "", fmt.Errorf("create state dir: %w", mkErr)
	}
	return config.RegistryPath(), root, nil
}

func newServerReal() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
}

func serveStdio(ctx context.Context, m *mcp.Server) error {
	return m.Run(ctx, &mcp.StdioTransport{})
}
