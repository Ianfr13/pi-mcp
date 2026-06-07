package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/model"
)

const defaultListLimit = 20

func (s *Server) handleList(_ context.Context, _ *mcp.CallToolRequest, in model.ListInput) (*mcp.CallToolResult, model.ListOutput, error) {
	cwd, err := validateCWD(in.CWD)
	if err != nil {
		return nil, model.ListOutput{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	items, err := s.store.ListItems(cwd, limit)
	if err != nil {
		return nil, model.ListOutput{}, err
	}
	if items == nil {
		items = []model.ListItem{}
	}
	return nil, model.ListOutput{Runs: items}, nil
}
