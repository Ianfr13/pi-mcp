package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/model"
)

func (s *Server) handleCancel(_ context.Context, _ *mcp.CallToolRequest, in model.CancelInput) (*mcp.CallToolResult, model.CancelOutput, error) {
	if strings.TrimSpace(in.JobID) == "" {
		return nil, model.CancelOutput{}, fmt.Errorf("jobId is required")
	}
	rec, err := s.jobs.Cancel(in.JobID)
	if err != nil {
		return nil, model.CancelOutput{}, err
	}
	return nil, model.CancelOutput{JobID: rec.JobID, Status: string(rec.Status)}, nil
}
