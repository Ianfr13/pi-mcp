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

func TestRenderPrompt_IncludesAgentTimeout(t *testing.T) {
	out, err := RenderPrompt("read", "do a thing", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agentTimeoutMs") {
		t.Errorf("prompt missing agentTimeoutMs directive:\n%s", out)
	}
	if !strings.Contains(out, "1200000") {
		t.Errorf("prompt missing the 20-min timeout value:\n%s", out)
	}
	if !strings.Contains(out, "tokenBudget") || !strings.Contains(out, "2000000000") {
		t.Errorf("prompt missing the explicit large tokenBudget directive:\n%s", out)
	}
}
