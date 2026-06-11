package mcpserver

import (
	"context"
	"fmt"
	"time"

	"pi-mcp/internal/model"
)

// fakeJobs implements JobsService with scripted return values.
type fakeJobs struct {
	submitRec model.JobRecord
	submitErr error
	lookup    map[string]model.JobRecord // jobID -> record
	byRun     map[string]model.JobRecord // runID|cwd -> record
	cancelRec model.JobRecord
	cancelErr error
	writeInfo map[string]model.WriteInfo // jobID -> write info
	activity  map[string]wtActivity      // jobID -> worktree activity

	lastSpec JobSpec // captured for assertions
}

// wtActivity scripts a fakeJobs.WorktreeActivity return.
type wtActivity struct {
	files        int
	lastModified time.Time
	ok           bool
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{
		lookup:    map[string]model.JobRecord{},
		byRun:     map[string]model.JobRecord{},
		writeInfo: map[string]model.WriteInfo{},
		activity:  map[string]wtActivity{},
	}
}

func (f *fakeJobs) Submit(_ context.Context, spec JobSpec) (model.JobRecord, error) {
	f.lastSpec = spec
	return f.submitRec, f.submitErr
}
func (f *fakeJobs) Lookup(jobID string) (model.JobRecord, bool) {
	r, ok := f.lookup[jobID]
	return r, ok
}
func (f *fakeJobs) LookupByRun(runID, cwd string) (model.JobRecord, bool) {
	r, ok := f.byRun[runID+"|"+cwd]
	return r, ok
}
func (f *fakeJobs) Cancel(string) (model.JobRecord, error) { return f.cancelRec, f.cancelErr }
func (f *fakeJobs) WriteInfoFor(jobID string) (model.WriteInfo, bool) {
	wi, ok := f.writeInfo[jobID]
	return wi, ok
}
func (f *fakeJobs) WorktreeActivity(jobID string) (int, time.Time, bool) {
	a, ok := f.activity[jobID]
	if !ok {
		return 0, time.Time{}, false
	}
	return a.files, a.lastModified, a.ok
}

// fakeStore implements RunStore. runs is keyed by runsDir+"/"+runID.
// seq lets a test return a DIFFERENT *model.Run on successive Load calls (long-poll growth).
// seqErrs is optional, parallel to seq: a non-nil entry makes the i-th Load
// return that error (simulates a mid-write decode failure for the transient-grace
// path).
type fakeStore struct {
	runs      map[string]*model.Run
	seq       []*model.Run // if non-nil, returned in order, last value sticks
	seqErrs   []error      // optional, parallel to seq: non-nil entry -> Load returns it
	calls     int
	list      []model.ListItem
	listErr   error
	authoring map[string]*model.AuthoringInfo // keyed by jobID
}

func newFakeStore() *fakeStore { return &fakeStore{runs: map[string]*model.Run{}} }

func (f *fakeStore) Load(runsDir, runID string) (*model.Run, error) {
	if f.seq != nil {
		i := f.calls
		f.calls++
		if i >= len(f.seq) {
			i = len(f.seq) - 1
		}
		if i < len(f.seqErrs) && f.seqErrs[i] != nil {
			return nil, f.seqErrs[i]
		}
		r := f.seq[i]
		if r == nil {
			return nil, fmt.Errorf("load: %w", ErrRunNotFound)
		}
		return r, nil
	}
	r, ok := f.runs[runsDir+"/"+runID]
	if !ok {
		return nil, fmt.Errorf("load: %w", ErrRunNotFound)
	}
	return r, nil
}
func (f *fakeStore) ListItems(string, int) ([]model.ListItem, error) { return f.list, f.listErr }
func (f *fakeStore) ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool) {
	a, ok := f.authoring[jobID]
	return a, ok
}

// strptr/i64ptr helpers for building fixtures in tests.
func strptr(s string) *string { return &s }
func i64ptr(v int64) *int64   { return &v }

func mustTime(s string) time.Time {
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return tt
}
