package main

import (
	"strings"
	"testing"
)

func TestRegistryPathFor_AlwaysDB(t *testing.T) {
	got := registryPathFor("/custom/state")
	want := "/custom/state/pi-mcp/registry.db"
	if got != want {
		t.Errorf("registryPathFor=%q want %q", got, want)
	}
	if strings.HasSuffix(got, ".json") {
		t.Errorf("registry path must be the canonical .db, not legacy .json: %q", got)
	}
}
