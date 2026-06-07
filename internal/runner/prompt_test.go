package runner

import (
	"strings"
	"testing"

	"pi-mcp/internal/config"
)

func TestRenderPrompt(t *testing.T) {
	tests := []struct {
		name         string
		mode         string
		task         string
		context      string
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:    "read with context",
			mode:    "read",
			task:    "Audit auth flow",
			context: "focus on session.go",
			wantContains: []string{
				"You MUST make exactly ONE call",
				"background:false",
				config.ReadContractSkeleton,
				"TASK:\nAudit auth flow",
				"CONTEXT:\nfocus on session.go",
			},
			wantAbsent: []string{
				"{{CONTRACT}}", "{{TASK}}", "{{CONTEXT}}",
				config.WriteContractSkeleton,
			},
		},
		{
			name:    "write without context omits bracket block",
			mode:    "write",
			task:    "Add retry to client",
			context: "",
			wantContains: []string{
				config.WriteContractSkeleton,
				"TASK:\nAdd retry to client",
			},
			wantAbsent: []string{
				"{{CONTRACT}}", "{{TASK}}", "{{CONTEXT}}",
				"CONTEXT:", "[CONTEXT", config.ReadContractSkeleton,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderPrompt(tt.mode, tt.task, tt.context)
			if err != nil {
				t.Fatalf("RenderPrompt error: %v", err)
			}
			for _, sub := range tt.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("prompt missing %q\n--- prompt ---\n%s", sub, got)
				}
			}
			for _, sub := range tt.wantAbsent {
				if strings.Contains(got, sub) {
					t.Errorf("prompt should NOT contain %q\n--- prompt ---\n%s", sub, got)
				}
			}
		})
	}
}

func TestRenderPromptBadMode(t *testing.T) {
	if _, err := RenderPrompt("delete", "t", ""); err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}
