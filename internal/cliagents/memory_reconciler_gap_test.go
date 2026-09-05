package cliagents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/fsnotify/fsnotify"
)

// TestStartMemoryIndexReconcilerAcceptsRawMarkdownStore covers
// markdownStoreOf's raw *markdownStore branch. The lifecycle test in
// memory_reconciler_test.go only ever exercises the ownedMarkdownStore
// branch (the production wrapper from OpenMemoryStore); OpenMarkdownStore
// itself returns the raw type, which is a supported input in its own right.
func TestStartMemoryIndexReconcilerAcceptsRawMarkdownStore(t *testing.T) {
	s, _, _ := newFreshnessStore(t)
	stop, ok := StartMemoryIndexReconciler(s, 50*time.Millisecond)
	if !ok {
		t.Fatal("raw markdown store must accept a reconciler")
	}
	t.Cleanup(stop)
}

// TestMemoryReconcilerLoopExitsWhenEventsChannelCloses and
// TestMemoryReconcilerLoopExitsWhenErrsChannelCloses cover the loop's two
// "channel closed" exits, distinct from the ctx.Done() exit every other test
// takes: fsnotify closes both channels when the watcher itself closes, and
// the loop must return (not spin) when that happens.
func TestMemoryReconcilerLoopExitsWhenEventsChannelCloses(t *testing.T) {
	s, _, _ := newFreshnessStore(t)
	fakeEvents := make(chan fsnotify.Event)
	logs := &logCapture{}
	r := newMemoryReconciler(s, time.Hour, logs.logf)
	r.events = fakeEvents
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)

	close(fakeEvents)
	waitFor(t, 2*time.Second, func() bool { return loopExited(r) })
}

func TestMemoryReconcilerLoopExitsWhenErrsChannelCloses(t *testing.T) {
	s, _, _ := newFreshnessStore(t)
	fakeErrs := make(chan error)
	logs := &logCapture{}
	r := startReconciler(t, s, time.Hour, logs, fakeErrs)

	close(fakeErrs)
	waitFor(t, 2*time.Second, func() bool { return loopExited(r) })
}

func loopExited(r *memoryReconciler) bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// TestMemoryReconcilerHandleEventIgnoresUnwatchedPath covers scopeForPath's
// negative case: an event for a path under neither a watched directory nor a
// watched directory itself must not schedule a sync.
func TestMemoryReconcilerHandleEventIgnoresUnwatchedPath(t *testing.T) {
	s, spy, source := newFreshnessStore(t)
	mkProjectDir(t, source)
	fakeEvents := make(chan fsnotify.Event, 1)
	r := newMemoryReconciler(s, time.Hour, (&logCapture{}).logf)
	r.events = fakeEvents
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	attachReconciler(s, r.fallback)

	before := spy.scanCount(memory.ScopeProject)
	fakeEvents <- fsnotify.Event{Name: filepath.Join(t.TempDir(), "unrelated.md"), Op: fsnotify.Write}
	time.Sleep(2 * memoryDebounceDelay)
	if got := spy.scanCount(memory.ScopeProject); got != before {
		t.Fatalf("scans %d -> %d, want no sync for a path outside every watched dir", before, got)
	}
}

// TestMemoryReconcilerHandleEventMatchesWatchedDirItself covers
// scopeForPath's fallback lookup: fsnotify can report the watched directory
// itself as the event name (e.g. a rename of the directory), which
// filepath.Dir does not resolve to an entry in dirs, so the fallback direct
// lookup is the only path that matches it to a scope.
func TestMemoryReconcilerHandleEventMatchesWatchedDirItself(t *testing.T) {
	s, spy, source := newFreshnessStore(t)
	mkProjectDir(t, source)
	fakeEvents := make(chan fsnotify.Event, 1)
	r := newMemoryReconciler(s, time.Hour, (&logCapture{}).logf)
	r.events = fakeEvents
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	attachReconciler(s, r.fallback)

	before := spy.scanCount(memory.ScopeProject)
	fakeEvents <- fsnotify.Event{Name: source.ProjectDir(), Op: fsnotify.Write}
	waitFor(t, 5*time.Second, func() bool { return spy.scanCount(memory.ScopeProject) > before })
}

// TestMemoryReconcilerSyncScopeSkipsAfterCancel covers syncScope's guard
// against work straggling past Stop: once the reconciler's context is
// canceled, a sync request must be a no-op rather than touching the store.
func TestMemoryReconcilerSyncScopeSkipsAfterCancel(t *testing.T) {
	s, spy, _ := newFreshnessStore(t)
	r := newMemoryReconciler(s, time.Hour, (&logCapture{}).logf)
	r.cancel()

	before := spy.scanCount(memory.ScopeProject)
	r.syncScope(memory.ScopeProject)
	if got := spy.scanCount(memory.ScopeProject); got != before {
		t.Fatalf("scans %d -> %d, want syncScope to skip after cancel", before, got)
	}
}

// TestMemoryReconcilerDirForUnconfiguredScopes covers dirFor's two "no
// directory" returns directly: the org branch when org is not configured
// (unreachable through configuredScopes, which only ever offers dirFor an
// org scope once both OrgID and OrgDir are set) and the default branch for a
// scope value the reconciler does not know.
func TestMemoryReconcilerDirForUnconfiguredScopes(t *testing.T) {
	s, _, _ := newFreshnessStore(t)
	r := newMemoryReconciler(s, time.Hour, (&logCapture{}).logf)

	if got := r.dirFor(memory.ScopeOrg); got != "" {
		t.Fatalf("dirFor(org) = %q, want empty when org is not configured", got)
	}
	if got := r.dirFor(memory.Scope("bogus")); got != "" {
		t.Fatalf("dirFor(bogus) = %q, want empty for an unrecognized scope", got)
	}
}

// emptyProjectDirSource wraps a real Markdown source but reports no project
// directory, so reattachWatches sees a configured scope with nowhere to
// watch - a state a live store cannot reach today (ProjectDir always names a
// real directory), but the guard exists in the reconciler independent of
// that, so this constructs the state directly through the interface seam.
type emptyProjectDirSource struct {
	inner memory.MarkdownSource
}

func (e *emptyProjectDirSource) Scan(ctx context.Context, scope memory.Scope) ([]memory.MarkdownDocument, error) {
	return e.inner.Scan(ctx, scope)
}

func (e *emptyProjectDirSource) Save(ctx context.Context, entry memory.Entry) (memory.MarkdownDocument, error) {
	return e.inner.Save(ctx, entry)
}

func (e *emptyProjectDirSource) Delete(ctx context.Context, path string) error {
	return e.inner.Delete(ctx, path)
}

func (e *emptyProjectDirSource) ProjectDir() string { return "" }
func (e *emptyProjectDirSource) OrgDir() string     { return "" }

// TestMemoryReconcilerReattachWatchesSkipsEmptyProjectDir covers
// reattachWatches' "if dir == ” continue" guard.
func TestMemoryReconcilerReattachWatchesSkipsEmptyProjectDir(t *testing.T) {
	root := t.TempDir()
	source, err := memory.NewMarkdownSource(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	store, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{
		Source: &emptyProjectDirSource{inner: source}, Index: index, ProjectID: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := store.(*markdownStore)
	r := newMemoryReconciler(s, time.Hour, (&logCapture{}).logf)

	r.reattachWatches()
	if len(r.dirs) != 0 {
		t.Fatalf("dirs=%v, want nothing watched when the project dir is empty", r.dirs)
	}
}

func mkProjectDir(t *testing.T, source memory.MarkdownSource) {
	t.Helper()
	if err := os.MkdirAll(source.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
}
