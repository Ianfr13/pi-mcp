package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/jobs"
)

func newTestRegistry(t *testing.T) *jobs.Registry {
	t.Helper()
	reg, err := jobs.NewRegistry(
		jobs.Config{Cap: 4, PersistPath: t.TempDir() + "/registry.db"},
		noopLauncher{}, noopCorrelator{}, worktreePruner{},
	)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func TestRun_ReconcileThenServe(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	reg := newTestRegistry(t)

	served := false
	d := Deps{
		Logger:        logger,
		buildRegistry: func(Deps) (*jobs.Registry, error) { return reg, nil },
		newServer:     func() *mcp.Server { return mcp.NewServer(&mcp.Implementation{Name: "pi-mcp", Version: "test"}, nil) },
		serve: func(ctx context.Context, m *mcp.Server) error {
			served = true
			return nil
		},
	}
	if err := Run(context.Background(), d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !served {
		t.Fatal("serve was not called")
	}
	if !strings.Contains(buf.String(), "reconciled") {
		t.Fatalf("missing reconcile log: %q", buf.String())
	}
}

func TestRun_ServeErrorReturned(t *testing.T) {
	reg := newTestRegistry(t)
	sentinel := errors.New("serve boom")
	d := Deps{
		Logger:        log.New(&bytes.Buffer{}, "", 0),
		buildRegistry: func(Deps) (*jobs.Registry, error) { return reg, nil },
		newServer:     func() *mcp.Server { return mcp.NewServer(&mcp.Implementation{Name: "pi-mcp", Version: "test"}, nil) },
		serve:         func(ctx context.Context, m *mcp.Server) error { return sentinel },
	}
	err := Run(context.Background(), d)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run err = %v, want it to wrap %v", err, sentinel)
	}
}

func TestRun_RegistersFourToolsBeforeServing(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	reg := newTestRegistry(t)

	// serve runs only AFTER the four tools are registered onto the mcp server.
	// We assert the registration happened (logged tool names) and that serve is
	// reached without starting stdio.
	served := false
	d := Deps{
		Logger:        logger,
		buildRegistry: func(Deps) (*jobs.Registry, error) { return reg, nil },
		newServer:     func() *mcp.Server { return mcp.NewServer(&mcp.Implementation{Name: "pi-mcp", Version: "test"}, nil) },
		serve: func(ctx context.Context, m *mcp.Server) error {
			served = true
			return nil
		},
	}
	if err := Run(context.Background(), d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !served {
		t.Fatal("serve was not reached (tools must register, then serve)")
	}
	out := buf.String()
	for _, name := range []string{"pi_workflow", "pi_status", "pi_list", "pi_cancel"} {
		if !strings.Contains(out, name) {
			t.Fatalf("expected tool %q to be logged as registered; log=%q", name, out)
		}
	}
}
