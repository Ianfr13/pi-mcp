// Package deps is a build-graph anchor that pins the project's external module
// dependencies in go.mod/go.sum before the packages that consume them exist.
//
// Foundation Task 1 is the SOLE owner of go.mod/go.sum and must pin
// github.com/modelcontextprotocol/go-sdk@v1.0.0 (used later by internal/mcpserver
// and cmd/pi-mcp) and github.com/google/uuid@v1.6.0 (used later by internal/jobs).
// Because no Foundation-layer source imports them yet, `go mod tidy` would prune
// them. These blank imports keep the require directives present and the build
// reproducible. Downstream packages replace these consumers with real usage; this
// file may be removed once both modules are imported by production code.
package deps

import (
	_ "github.com/google/uuid"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
