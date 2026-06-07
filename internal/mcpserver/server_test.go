package mcpserver

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisterAddsFourTools(t *testing.T) {
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "pi-mcp", Version: "v0.1.0"}, nil)
	srv := New(newFakeJobs(), newFakeStore())

	// Register must not panic and must wire all four tools onto the mcp server.
	srv.Register(mcpSrv)

	// Smoke: the registration helper returns the names it registered (for the test + main).
	got := srv.RegisteredToolNames()
	want := map[string]bool{"pi_workflow": false, "pi_status": false, "pi_list": false, "pi_cancel": false}
	for _, n := range got {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("tool %q was not registered (got %v)", name, got)
		}
	}
}

// Compile-time proof the fakes satisfy the seams.
var _ JobsService = (*fakeJobs)(nil)
var _ RunStore = (*fakeStore)(nil)
