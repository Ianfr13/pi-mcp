package runner

import (
	"reflect"
	"testing"
)

func TestBuildArgv(t *testing.T) {
	prompt := "THE PROMPT\nwith newline"
	tests := []struct {
		name           string
		noContextFiles bool
		want           []string
	}{
		{
			name:           "no context-files flag when false",
			noContextFiles: false,
			want:           []string{"pi", "-p", "--mode", "json", "--no-session", "--model", "openai-codex/gpt-5.5", "--thinking", "high", "THE PROMPT\nwith newline"},
		},
		{
			name:           "context-files flag when true",
			noContextFiles: true,
			want:           []string{"pi", "-p", "--mode", "json", "--no-session", "--model", "openai-codex/gpt-5.5", "--thinking", "high", "--no-context-files", "THE PROMPT\nwith newline"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildArgv(prompt, tt.noContextFiles)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BuildArgv mismatch\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

// The prompt must be exactly ONE argv element (no shell, no splitting on spaces/newlines).
func TestBuildArgvPromptIsSingleElement(t *testing.T) {
	prompt := "multi word\nmulti line prompt"
	got := BuildArgv(prompt, false)
	last := got[len(got)-1]
	if last != prompt {
		t.Fatalf("prompt not preserved as single argv element\n got: %q\nwant: %q", last, prompt)
	}
}

// pi-mcp pins a strong orchestrator (script-author) model so the workflow script
// is valid JS; the flag must be present and precede the positional prompt.
func TestBuildArgvPinsOrchestratorModel(t *testing.T) {
	got := BuildArgv("p", false)
	var mi, ti, pi = -1, -1, -1
	for i, a := range got {
		switch a {
		case "--model":
			mi = i
		case "--thinking":
			ti = i
		case "p":
			pi = i
		}
	}
	if mi < 0 || got[mi+1] != "openai-codex/gpt-5.5" {
		t.Fatalf("expected --model openai-codex/gpt-5.5, got %#v", got)
	}
	if ti < 0 || got[ti+1] != "high" {
		t.Fatalf("expected --thinking high, got %#v", got)
	}
	if !(mi < pi && ti < pi) {
		t.Fatalf("model/thinking flags must precede the prompt, got %#v", got)
	}
}
