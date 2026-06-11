package runstore

import (
	"encoding/json"
	"testing"

	"pi-mcp/internal/model"
)

// eventsRun: journal in completion order 0,2,1; agent callIndex 1 errored.
func eventsRun() *model.Run {
	errMsg := "boom"
	return &model.Run{
		Agents: []model.Agent{
			{ID: 1, CallIndex: 0, Label: "scan", Model: "haiku", Phase: "Scan", Status: "done"},
			{ID: 2, CallIndex: 1, Label: "fix", Model: "sonnet", Phase: "Fix", Status: "error", Error: &errMsg},
			{ID: 3, CallIndex: 2, Label: "verify", Model: "opus", Phase: "Verify", Status: "done"},
		},
		Journal: []model.JournalEntry{
			{Index: 0, Result: json.RawMessage(`{"ok":1}`)},
			{Index: 2, Result: json.RawMessage(`{"ok":2}`)},
			{Index: 1, Result: json.RawMessage(`{"ok":3}`)},
		},
	}
}

func TestEventsSince_DeltaAndJoin(t *testing.T) {
	r := eventsRun()

	all := EventsSince(r, 0, false, 1024)
	if len(all) != 3 {
		t.Fatalf("from 0: want 3 events, got %d", len(all))
	}
	// journal completion order: scan, verify, fix
	if all[0].Label != "scan" || all[1].Label != "verify" || all[2].Label != "fix" {
		t.Fatalf("join/order wrong: %+v", all)
	}
	if all[2].Status != "error" || all[2].Error != "boom" {
		t.Fatalf("errored agent must surface status=error + message: %+v", all[2])
	}
	if all[0].Status != "ok" || all[0].Result != nil {
		t.Fatalf("default rows carry no result body: %+v", all[0])
	}

	tail := EventsSince(r, 2, false, 1024)
	if len(tail) != 1 || tail[0].Label != "fix" {
		t.Fatalf("from 2: want only the last journal entry, got %+v", tail)
	}

	if got := EventsSince(r, 3, false, 1024); len(got) != 0 {
		t.Fatalf("from == len(journal): want empty, got %+v", got)
	}
	if got := EventsSince(r, 99, false, 1024); len(got) != 0 {
		t.Fatalf("from beyond journal: want empty (clamped), got %+v", got)
	}
}

func TestEventsSince_IncludeResultsAndTruncation(t *testing.T) {
	r := eventsRun()

	with := EventsSince(r, 0, true, 1024)
	if with[0].Result == nil || with[0].Truncated {
		t.Fatalf("include_results must attach the full result: %+v", with[0])
	}

	// 4-byte cap: every result truncates to a preview.
	small := EventsSince(r, 0, true, 4)
	if small[0].Result != nil || !small[0].Truncated || small[0].Preview == "" {
		t.Fatalf("oversized result must become preview+truncated: %+v", small[0])
	}
}

func TestEventsSince_SkipsJournalWithoutAgent(t *testing.T) {
	r := eventsRun()
	r.Journal = append(r.Journal, model.JournalEntry{Index: 99, Result: json.RawMessage(`1`)})
	got := EventsSince(r, 3, false, 1024)
	if len(got) != 0 {
		t.Fatalf("journal entry with no agent is skipped: %+v", got)
	}
}
