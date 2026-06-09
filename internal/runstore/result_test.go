package runstore

import (
	"path/filepath"
	"testing"
)

func TestMetadata_Canonical(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	md := Metadata(r)
	if md.AgentCount != 4 {
		t.Errorf("AgentCount = %d, want 4", md.AgentCount)
	}
	if md.ByModel["deepseek/deepseek-v4-flash"] != 3 || md.ByModel["openai-codex/gpt-5.5"] != 1 {
		t.Errorf("ByModel = %v, want {flash:3, gpt-5.5:1}", md.ByModel)
	}
	if md.TokenUsage == nil || md.TokenUsage.Total != 120469 {
		t.Errorf("TokenUsage.Total = %v, want 120469", md.TokenUsage)
	}
	if md.TokenUsage == nil || md.TokenUsage.Cost != 0.1463847 {
		t.Errorf("TokenUsage.Cost = %v, want 0.1463847 (verbatim)", md.TokenUsage)
	}
	if md.DurationMs == nil || *md.DurationMs != 22391 {
		t.Errorf("DurationMs = %v, want 22391", md.DurationMs)
	}
}

func TestMetadata_PartialSuppressesCostAndDuration(t *testing.T) {
	r, err := ReadRun(filepath.Join("testdata", "sample-run-partial.json"))
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	md := Metadata(r)
	if md.AgentCount != 3 {
		t.Errorf("AgentCount = %d, want 3", md.AgentCount)
	}
	if md.TokenUsage != nil {
		t.Errorf("TokenUsage = %v, want nil during run (cost suppressed until end)", md.TokenUsage)
	}
	if md.DurationMs != nil {
		t.Errorf("DurationMs = %v, want nil during run", md.DurationMs)
	}
	if md.ByModel["deepseek/deepseek-v4-flash"] != 3 {
		t.Errorf("ByModel[flash] = %d, want 3", md.ByModel["deepseek/deepseek-v4-flash"])
	}
}
