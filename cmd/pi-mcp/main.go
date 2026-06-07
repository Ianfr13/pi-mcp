// Command pi-mcp is the MCP server entrypoint. It delegates all wiring to
// internal/app so the startup sequence is unit-testable; see app.Run.
package main

import (
	"context"
	"log"

	"pi-mcp/internal/app"
)

func main() {
	if err := app.Run(context.Background(), app.Deps{}); err != nil {
		log.Fatal(err)
	}
}
