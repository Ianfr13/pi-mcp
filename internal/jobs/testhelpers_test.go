package jobs

import (
	"context"
	"sync"
)

// errFake is a sentinel error for wait()-failure tests.
var errFake = errFakeT("boom")

type errFakeT string

func (e errFakeT) Error() string { return string(e) }

// fakeLauncher is a programmable Launcher for tests. Each Launch call:
//   - returns pid (default 4242),
//   - emits sessionID on sessionCh (unless emitSession=false, in which case the
//     channel is closed without a value),
//   - blocks wait() until the test signals via release().
type fakeLauncher struct {
	mu          sync.Mutex
	pid         int
	sessionID   string
	emitSession bool
	waitErr     error
	launched    int
	releaseChs  []chan struct{}
}

func newFakeLauncher(sessionID string) *fakeLauncher {
	return &fakeLauncher{pid: 4242, sessionID: sessionID, emitSession: true}
}

func (f *fakeLauncher) Launch(ctx context.Context, spec Spec) (int, <-chan string, func() error, error) {
	f.mu.Lock()
	f.launched++
	rel := make(chan struct{})
	f.releaseChs = append(f.releaseChs, rel)
	pid := f.pid
	sid := f.sessionID
	emit := f.emitSession
	waitErr := f.waitErr
	f.mu.Unlock()

	sessionCh := make(chan string, 1)
	if emit {
		sessionCh <- sid
	}
	close(sessionCh)

	wait := func() error {
		select {
		case <-rel:
		case <-ctx.Done():
			return ctx.Err()
		}
		return waitErr
	}
	return pid, sessionCh, wait, nil
}

// release lets the i-th launched job's wait() return.
func (f *fakeLauncher) release(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.releaseChs[i])
}

func (f *fakeLauncher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.launched
}

// fakeCorrelator maps sessionID -> runID from a fixed table.
type fakeCorrelator struct {
	table map[string]string // sessionID -> runID
}

func (c *fakeCorrelator) RunIDForSession(runsDir, sessionID string) (string, bool) {
	r, ok := c.table[sessionID]
	return r, ok
}

// fakePruner records prune calls.
type fakePruner struct {
	mu    sync.Mutex
	calls []struct{ Worktree, Branch string }
	err   error
}

func (p *fakePruner) Prune(worktreePath, branch string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, struct{ Worktree, Branch string }{worktreePath, branch})
	return p.err
}

func (p *fakePruner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}
