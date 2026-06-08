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
	// Mirror the real launcher's pre-canceled-Start semantics (invariant e): if
	// ctx is already canceled, Launch fails immediately like cmd.Start().
	if err := ctx.Err(); err != nil {
		return 0, nil, nil, err
	}
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

// cleanExitLauncher is a Launcher whose wait() blocks ONLY on its release
// channel and then returns nil — it never selects on ctx. This models a process
// that exits cleanly even after a cancel/kill request, so finish() is driven
// with JobCompleted while Cancel has already set JobAborted. Used to exercise
// the finish() compare-and-set guard (a clean exit must not overwrite aborted).
type cleanExitLauncher struct {
	mu      sync.Mutex
	release chan struct{}
}

func newCleanExitLauncher() *cleanExitLauncher {
	return &cleanExitLauncher{release: make(chan struct{})}
}

func (f *cleanExitLauncher) Launch(ctx context.Context, spec Spec) (int, <-chan string, func() error, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, nil, err
	}
	sessionCh := make(chan string)
	close(sessionCh) // no session event; correlate exits immediately
	rel := f.release
	wait := func() error {
		<-rel // blocks ONLY on release; ignores ctx so it returns nil after a kill
		return nil
	}
	return 4242, sessionCh, wait, nil
}

func (f *cleanExitLauncher) releaseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.release)
}

// blockingWaitLauncher is a Launcher whose wait() blocks until release; the
// session channel is left open (no event) so correlate() parks on ctx. Used by
// the white-box admission test (j.cancel must be non-nil while running).
type blockingWaitLauncher struct {
	release chan struct{}
}

func newBlockingWaitLauncher() *blockingWaitLauncher {
	return &blockingWaitLauncher{release: make(chan struct{})}
}

func (f *blockingWaitLauncher) Launch(ctx context.Context, spec Spec) (int, <-chan string, func() error, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, nil, err
	}
	sessionCh := make(chan string) // never emits; correlate() parks on ctx.Done()
	rel := f.release
	wait := func() error {
		select {
		case <-rel:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	return 4242, sessionCh, wait, nil
}

func (f *blockingWaitLauncher) releaseAll() { close(f.release) }

// seqLauncher returns a programmed sequence of wait() errors across successive
// Launch calls; wait() returns immediately (no release gate) and no session id is
// emitted (so correlate resolves nothing -> RunID stays "" -> the authoring-retry
// path is taken). It honors ctx at spawn. For retry tests.
type seqLauncher struct {
	mu       sync.Mutex
	waitErrs []error
	launches int
}

func (s *seqLauncher) Launch(ctx context.Context, _ Spec) (int, <-chan string, func() error, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, nil, err
	}
	s.mu.Lock()
	i := s.launches
	s.launches++
	var werr error
	if i < len(s.waitErrs) {
		werr = s.waitErrs[i]
	}
	s.mu.Unlock()
	ch := make(chan string, 1)
	close(ch) // no session id -> correlate returns immediately, RunID ""
	return 4242, ch, func() error { return werr }, nil
}

func (s *seqLauncher) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launches
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
