package main

import "testing"

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
