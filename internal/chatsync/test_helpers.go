package chatsync

// The three functions below exist ONLY to let external test packages
// (uiadapter's SessionPool.ApplySyncOpts fan-out test) build a
// *SyncSession fixture and inspect its projector without a real
// OpenSession round trip. They carry a `ForTest` name suffix so a
// reader (or `go vet`'s unusedresult-style tooling) can tell at a
// glance that production code must never call them, matching the
// stdlib's own httptest.NewRequest / iotest.TimeoutReader convention
// for test-only exported API. cmd/mivia's import graph never touches
// these three names, so they carry no runtime behavior (no init, no
// goroutines, no global state) even though they compile into the
// package.

// NewSyncSessionForTest constructs a SyncSession whose projector is
// already attached, without going through OpenSession (which requires
// a real remote server).
func NewSyncSessionForTest(opts ProjectorOptions) *SyncSession {
	return &SyncSession{
		opts:      SessionOptions{ProjectorOptions: opts},
		projector: NewProjector("sess-test", 0, opts),
	}
}

// ProjectorOptsForTest returns the projector's ProjectorOptions by
// value, so an external test can assert on it without reaching into
// SyncSession's unexported fields.
func (s *SyncSession) ProjectorOptsForTest() ProjectorOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projector == nil {
		return s.opts.ProjectorOptions
	}
	return s.projector.opts
}

// StoppedForTest latches remoteEnded=true so the next SetSyncOpts call
// hits the stopped-session no-op branch. Real stop paths (Stop,
// classifyFlushError -> recoverRemoteSession) own the same atomic in
// production.
func (s *SyncSession) StoppedForTest() {
	s.remoteEnded.Store(true)
}
