package mcpserver

import (
	"errors"
	"testing"

	"pi-mcp/internal/model"
)

func TestCancel_Happy(t *testing.T) {
	j := newFakeJobs()
	j.cancelRec = model.JobRecord{JobID: "job-x", Status: model.JobAborted}
	srv := New(j, newFakeStore())
	_, out, err := srv.handleCancel(ctxBG(), nil, model.CancelInput{JobID: "job-x"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if out.JobID != "job-x" || out.Status != "aborted" {
		t.Fatalf("bad cancel output: %+v", out)
	}
}

func TestCancel_MissingJobID(t *testing.T) {
	srv := New(newFakeJobs(), newFakeStore())
	if _, _, err := srv.handleCancel(ctxBG(), nil, model.CancelInput{JobID: ""}); err == nil {
		t.Fatalf("empty jobId must error")
	}
}

func TestCancel_ErrorPropagates(t *testing.T) {
	j := newFakeJobs()
	j.cancelErr = errors.New("no such job")
	srv := New(j, newFakeStore())
	if _, _, err := srv.handleCancel(ctxBG(), nil, model.CancelInput{JobID: "ghost"}); err == nil {
		t.Fatalf("cancel error must propagate")
	}
}
