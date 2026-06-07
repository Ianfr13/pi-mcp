package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"pi-mcp/internal/model"
)

func (s *Server) handleWorkflow(ctx context.Context, _ *mcp.CallToolRequest, in model.WorkflowInput) (*mcp.CallToolResult, model.WorkflowOutput, error) {
	var zero model.WorkflowOutput
	if strings.TrimSpace(in.Task) == "" {
		return nil, zero, fmt.Errorf("task is required")
	}
	mode, err := validateMode(in.Mode)
	if err != nil {
		return nil, zero, err
	}
	cwd, err := validateCWD(in.CWD)
	if err != nil {
		return nil, zero, err
	}

	rec, err := s.jobs.Submit(ctx, JobSpec{
		Task:    in.Task,
		Mode:    mode,
		CWD:     cwd,
		Context: in.Context,
	})
	if err != nil {
		return nil, zero, err
	}

	out := model.WorkflowOutput{
		JobID:     rec.JobID,
		Status:    string(rec.Status),
		Mode:      string(rec.Mode),
		CWD:       rec.CWD,
		StartedAt: rec.StartedAt.UTC().Format(time.RFC3339),
	}
	if rec.Mode == model.ModeWrite {
		out.WorktreePath = rec.WorktreePath
	}
	return nil, out, nil
}
