package model

import (
	"encoding/json"
	"os"
	"testing"
)

func TestUnmarshalSampleRun(t *testing.T) {
	b, err := os.ReadFile("../../docs/research/fixtures/sample-run.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("unmarshal Run: %v", err)
	}
	if r.RunID != "mq40rdpt-yij9hj" {
		t.Errorf("RunID = %q", r.RunID)
	}
	if r.SessionID != "019ea2fe-db76-7e31-85ad-9718d3fbc23a" {
		t.Errorf("SessionID = %q", r.SessionID)
	}
	if r.Status != "completed" {
		t.Errorf("Status = %q", r.Status)
	}
	if r.CurrentPhase == nil || *r.CurrentPhase != "Final" {
		t.Errorf("CurrentPhase = %v", r.CurrentPhase)
	}
	if len(r.Agents) != 4 {
		t.Fatalf("len(Agents) = %d", len(r.Agents))
	}
	if len(r.Journal) != 4 {
		t.Fatalf("len(Journal) = %d", len(r.Journal))
	}
	// journal is in completion order, NOT array position: index 0,2,1,3
	if r.Journal[1].Index != 2 {
		t.Errorf("Journal[1].Index = %d, want 2 (out-of-order)", r.Journal[1].Index)
	}
	// agents[].id == callIndex+1
	a0 := r.Agents[0]
	if a0.CallIndex != 0 || a0.ID != 1 {
		t.Errorf("agent0 callIndex/id = %d/%d", a0.CallIndex, a0.ID)
	}
	if a0.Model != "deepseek/deepseek-v4-flash" {
		t.Errorf("agent0 model = %q", a0.Model)
	}
	// token usage: cost is verbatim float64, total present
	if r.TokenUsage == nil {
		t.Fatal("TokenUsage nil")
	}
	if r.TokenUsage.Total != 120469 {
		t.Errorf("TokenUsage.Total = %d", r.TokenUsage.Total)
	}
	if r.TokenUsage.Cost != 0.1463847 {
		t.Errorf("TokenUsage.Cost = %v (must be verbatim)", r.TokenUsage.Cost)
	}
	// optionals present here
	if r.DurationMs == nil || *r.DurationMs != 22391 {
		t.Errorf("DurationMs = %v", r.DurationMs)
	}
	if r.CompletedAt == nil {
		t.Error("CompletedAt nil but fixture has it")
	}
	// .result is authoritative RawMessage, must contain "overall"
	var res map[string]json.RawMessage
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("Result not an object: %v", err)
	}
	if _, ok := res["overall"]; !ok {
		t.Error("Result missing 'overall'")
	}
	// logs[0] verbatim (used for failure-message sidecar)
	if len(r.Logs) == 0 {
		t.Error("Logs empty")
	}
}

func TestOptionalsAbsentDoNotError(t *testing.T) {
	// A running run omits result/tokenUsage/completedAt/durationMs.
	js := `{"runId":"r1","sessionId":"s1","status":"running","agents":[],"journal":[]}`
	var r Run
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("unmarshal partial: %v", err)
	}
	if r.CompletedAt != nil || r.DurationMs != nil || r.TokenUsage != nil {
		t.Error("absent optionals should be nil pointers")
	}
	if len(r.Result) != 0 {
		t.Errorf("absent Result should be empty RawMessage, got %s", r.Result)
	}
}

// TestStreamEventDecode pins the JSONL event envelope the parser consumes:
// session id, and tool_execution_end(toolName="workflow") with content[].text.
func TestStreamEventDecode(t *testing.T) {
	const sessionLine = `{"type":"session","id":"019ea2fe-db76-7e31-85ad-9718d3fbc23a","version":1,"cwd":"/repo"}`
	var se StreamEvent
	if err := json.Unmarshal([]byte(sessionLine), &se); err != nil {
		t.Fatalf("unmarshal session event: %v", err)
	}
	if se.Type != "session" {
		t.Errorf("Type = %q, want session", se.Type)
	}
	if se.ID != "019ea2fe-db76-7e31-85ad-9718d3fbc23a" {
		t.Errorf("ID = %q", se.ID)
	}

	const toolLine = `{"type":"tool_execution_end","toolName":"workflow","isError":false,"result":{"content":[{"type":"text","text":"hello"}]}}`
	var te StreamEvent
	if err := json.Unmarshal([]byte(toolLine), &te); err != nil {
		t.Fatalf("unmarshal tool_execution_end: %v", err)
	}
	if te.ToolName != "workflow" {
		t.Errorf("ToolName = %q", te.ToolName)
	}
	if te.IsError {
		t.Error("IsError = true, want false")
	}
	if te.Result == nil {
		t.Fatal("Result nil on tool_execution_end")
	}
	if len(te.Result.Content) != 1 {
		t.Fatalf("len(Content) = %d", len(te.Result.Content))
	}
	if te.Result.Content[0].Type != "text" || te.Result.Content[0].Text != "hello" {
		t.Errorf("content[0] = %+v", te.Result.Content[0])
	}
}

// TestJobRecordRoundTrip pins the persisted registry record wire shape (§8).
func TestJobRecordRoundTrip(t *testing.T) {
	rec := JobRecord{
		JobID:        "job-1",
		Mode:         ModeWrite,
		CWD:          "/repo",
		RunsDir:      "/repo/.pi/workflows/runs",
		WorktreePath: "/wt/job-1",
		Branch:       "pi-mcp/job-job-1",
		Status:       JobRunning,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got JobRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Mode != ModeWrite {
		t.Errorf("Mode = %q", got.Mode)
	}
	if got.Status != JobRunning {
		t.Errorf("Status = %q", got.Status)
	}
	if got.Branch != "pi-mcp/job-job-1" {
		t.Errorf("Branch = %q", got.Branch)
	}
	// Status job consts are the MCP-level vocabulary.
	if JobQueued != "queued" || JobCompleted != "completed" || JobFailed != "failed" || JobAborted != "aborted" {
		t.Error("JobStatus consts diverge from wire vocabulary")
	}
}
