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
			want:           []string{"pi", "-p", "--mode", "json", "--no-session", "THE PROMPT\nwith newline"},
		},
		{
			name:           "context-files flag when true",
			noContextFiles: true,
			want:           []string{"pi", "-p", "--mode", "json", "--no-session", "--no-context-files", "THE PROMPT\nwith newline"},
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
