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

func TestResolveAddr_Explicit(t *testing.T) {
	got, err := resolveAddr("1.2.3.4:9999", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "1.2.3.4:9999" {
		t.Errorf("addr=%q want 1.2.3.4:9999", got)
	}
}

func TestResolveAddr_DetectsTailscale(t *testing.T) {
	detect := func() (string, error) { return "100.64.0.5", nil }
	got, err := resolveAddr("", detect)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "100.64.0.5:7777" {
		t.Errorf("addr=%q want 100.64.0.5:7777", got)
	}
}
