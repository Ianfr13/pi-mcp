package mcpserver

import (
	"testing"
	"time"

	"pi-mcp/internal/model"
)

func TestList_HappyAndDefaultLimit(t *testing.T) {
	completed := mustTime("2026-06-07T16:51:55Z")
	cost := 0.1463847
	dur := int64(22391)
	store := newFakeStore()
	store.list = []model.ListItem{
		{RunID: "r1", WorkflowName: "judge_claims", Status: "completed", AgentCount: 4,
			ByModel:     map[string]int{"deepseek/deepseek-v4-flash": 3, "openai-codex/gpt-5.5": 1},
			Cost:        &cost,
			DurationMs:  &dur,
			CompletedAt: &completed},
	}
	srv := New(newFakeJobs(), store)
	dir := t.TempDir()

	_, out, err := srv.handleList(ctxBG(), nil, model.ListInput{CWD: dir, Limit: 0})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(out.Runs) != 1 || out.Runs[0].RunID != "r1" {
		t.Fatalf("bad list output: %+v", out.Runs)
	}
	if out.Runs[0].ByModel["deepseek/deepseek-v4-flash"] != 3 {
		t.Fatalf("histogram lost")
	}
}

func TestList_CWDValidated(t *testing.T) {
	srv := New(newFakeJobs(), newFakeStore())
	if _, _, err := srv.handleList(ctxBG(), nil, model.ListInput{CWD: ""}); err == nil {
		t.Fatalf("empty cwd must error")
	}
	if _, _, err := srv.handleList(ctxBG(), nil, model.ListInput{CWD: "rel/path"}); err == nil {
		t.Fatalf("relative cwd must error")
	}
}

func TestList_StorePassesResolvedLimit(t *testing.T) {
	store := &capturingStore{fakeStore: newFakeStore()}
	srv := New(newFakeJobs(), store)
	dir := t.TempDir()
	_, _, _ = srv.handleList(ctxBG(), nil, model.ListInput{CWD: dir, Limit: 0})
	if store.gotLimit != defaultListLimit {
		t.Fatalf("limit<=0 should default to %d, got %d", defaultListLimit, store.gotLimit)
	}
	_, _, _ = srv.handleList(ctxBG(), nil, model.ListInput{CWD: dir, Limit: 5})
	if store.gotLimit != 5 {
		t.Fatalf("explicit limit not passed, got %d", store.gotLimit)
	}
	_ = time.Now
}

type capturingStore struct {
	*fakeStore
	gotCWD   string
	gotLimit int
}

func (c *capturingStore) ListItems(cwd string, limit int) ([]model.ListItem, error) {
	c.gotCWD, c.gotLimit = cwd, limit
	return c.fakeStore.ListItems(cwd, limit)
}
