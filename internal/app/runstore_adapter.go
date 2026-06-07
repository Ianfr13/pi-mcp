package app

import (
	"errors"
	"fmt"

	"pi-mcp/internal/mcpserver"
	"pi-mcp/internal/model"
	"pi-mcp/internal/runstore"
)

// runStoreAdapter implements mcpserver.RunStore over the runstore package, mapping
// runstore.ErrRunNotFound onto mcpserver.ErrRunNotFound (the handlers' sentinel).
type runStoreAdapter struct{}

func (runStoreAdapter) Load(runsDir, runID string) (*model.Run, error) {
	r, err := runstore.Load(runsDir, runID)
	if errors.Is(err, runstore.ErrRunNotFound) {
		return nil, fmt.Errorf("%w", mcpserver.ErrRunNotFound)
	}
	return r, err
}

func (runStoreAdapter) ListItems(cwd string, limit int) ([]model.ListItem, error) {
	return runstore.ListItems(cwd, limit)
}
