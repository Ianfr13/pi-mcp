package runner

import (
	"fmt"
	"strings"

	"pi-mcp/internal/config"
)

// RenderPrompt builds the single positional prompt (§4.2) from the forcing
// template, the §5.4 contract skeleton for the mode, the task, and optional
// context. TASK/CONTEXT are inserted as delimited data, never as executable
// script (the engine vm is not a security sandbox — §14).
//
// When context is empty the entire "[CONTEXT:\n...]" bracket block is removed,
// matching the optional-bracket form in the §4.2 template.
func RenderPrompt(mode, task, context string) (string, error) {
	var contract string
	switch mode {
	case "read":
		contract = config.ReadContractSkeleton
	case "write":
		contract = config.WriteContractSkeleton
	default:
		return "", fmt.Errorf("runner: invalid mode %q (want \"read\" or \"write\")", mode)
	}

	out := config.ForcingPromptTemplate
	out = strings.Replace(out, "{{CONTRACT}}", contract, 1)
	out = strings.Replace(out, "{{TASK}}", task, 1)

	if strings.TrimSpace(context) == "" {
		// Remove the optional bracketed CONTEXT block, including the blank
		// line that precedes it, leaving a clean prompt tail.
		out = stripContextBlock(out)
	} else {
		out = strings.Replace(out, "{{CONTEXT}}", context, 1)
	}
	return out, nil
}

// stripContextBlock removes the "[CONTEXT:\n{{CONTEXT}}]" block (and the blank
// line separating it from TASK) from the rendered template when no context is
// supplied. It is resilient to the {{CONTEXT}} placeholder being present.
func stripContextBlock(s string) string {
	idx := strings.Index(s, "[CONTEXT:")
	if idx < 0 {
		return s
	}
	head := s[:idx]
	// Trim trailing whitespace/newlines left by removing the block.
	return strings.TrimRight(head, "\n ")
}
