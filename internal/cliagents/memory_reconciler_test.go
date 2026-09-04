package cliagents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// logCapture records reconciler logf lines for log-once-per-streak
// assertions.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *logCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.lines)
}

func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = nil
}

// waitFor polls cond until it holds or the deadline passes. Positive
// conditions are polled, never fixed-slept.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// startReconciler builds and starts a reconciler for s. errs replaces the
// watcher's Errors channel when non-nil. Cleanup ordering is LIFO: the stop
// registered here runs before the store's index cleanup from
// newFreshnessStore, so the watcher closes before the temp directories are
// removed.
func startReconciler(t *testing.T, s *markdownStore, fallback time.Duration, logs *logCapture, errs <-chan error) *memoryReconciler {
	t.Helper()
	r := newMemoryReconciler(s, fallback, logs.logf)
	if errs != nil {
		r.errs = errs
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	return r
}

// attachReconciler mirrors what StartMemoryIndexReconciler records on the
// store, without stamping freshness, so tests observe honest stamping by the
// reconciler itself.
func attachReconciler(s *markdownStore, fallback time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconcilerAttached = true
	s.fallback = fallback
}

func scopeFresh(s *markdownStore, scope memory.Scope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scopeIsFresh(scope)
}

func scopeDegraded(s *markdownStore, scope memory.Scope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded[scope]
}

func countIndexEntries(t *testing.T, s *markdownStore, scope memory.Scope) int {
	t.Helper()
	n, err := s.cfg.Index.CountMemoryIndex(context.Background(), string(scope), s.cfg.ProjectID, orgID(scope, s.cfg.OrgID))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMemoryReconcilerDebouncesAtomicSaves proves one atomic temp+rename
// save yields exactly one debounced sync and a burst of ten rapid saves
// coalesces into one more. The fallback is long enough that no tick can land
// inside the assertion window, so every scan counted here is event-driven.
func TestMemoryReconcilerDebouncesAtomicSaves(t *testing.T) {
	ctx := context.Background()
	s, spy, source := newFreshnessStore(t)
	if err := os.MkdirAll(source.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	logs := &logCapture{}
	r := startReconciler(t, s, 10*time.Second, logs, nil)
	attachReconciler(s, r.fallback)

	if _, err := source.Save(ctx, memory.Entry{Title: "Coalesce one", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "First atomic save.", Why: "Watcher debounce."}); err != nil {
		t.Fatal(err)
	}
	before := spy.scanCount(memory.ScopeProject)
	waitFor(t, 5*time.Second, func() bool { return spy.scanCount(memory.ScopeProject) > before })
	if got := spy.scanCount(memory.ScopeProject); got != before+1 {
		t.Fatalf("scans after one atomic save=%d, want exactly %d", got, before+1)
	}
	// Negative window: a bounded quiet period must add no second sync. A
	// fixed wait is the only way to observe an absence.
	quiet := spy.scanCount(memory.ScopeProject)
	time.Sleep(2 * memoryDebounceDelay)
	if got := spy.scanCount(memory.ScopeProject); got != quiet {
		t.Fatalf("scans drifted %d -> %d inside the quiet window, want none", quiet, got)
	}

	for i := 0; i < 10; i++ {
		if _, err := source.Save(ctx, memory.Entry{Title: fmt.Sprintf("Coalesce burst %d", i), Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: fmt.Sprintf("Rewrite pass %d.", i), Why: "Burst coalescing."}); err != nil {
			t.Fatal(err)
		}
	}
	before = spy.scanCount(memory.ScopeProject)
	waitFor(t, 5*time.Second, func() bool { return spy.scanCount(memory.ScopeProject) > before })
	if got := spy.scanCount(memory.ScopeProject); got != before+1 {
		t.Fatalf("scans after ten rapid saves=%d, want exactly %d", got, before+1)
	}
}

// TestMemoryReconcilerIndexesDirCreatedMidRun starts with a nonexistent
// memory directory - missing watches are retried each tick, never fatal - and
// proves a directory plus file created mid-run are indexed by the tick.
func TestMemoryReconcilerIndexesDirCreatedMidRun(t *testing.T) {
	ctx := context.Background()
	s, spy, source := newFreshnessStore(t)
	logs := &logCapture{}
	r := startReconciler(t, s, 50*time.Millisecond, logs, nil)
	attachReconciler(s, r.fallback)

	if count := countIndexEntries(t, s, memory.ScopeProject); count != 0 {
		t.Fatalf("index count at start=%d, want an empty scope with no error", count)
	}
	if _, err := source.Save(ctx, memory.Entry{Title: "Late arrival", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "midrun directory marker", Why: "Created after start."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return countIndexEntries(t, s, memory.ScopeProject) == 1 })
	if got := spy.scanCount(memory.ScopeProject); got < 2 {
		t.Fatalf("project scans=%d, want the open refresh plus a tick sync", got)
	}
	waitFor(t, 5*time.Second, func() bool { return scopeFresh(s, memory.ScopeProject) })
	hits, err := s.Search(ctx, memory.Query{Text: "midrun directory marker", Scope: memory.ScopeProject})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%d err=%v, want the reconciler's entry served without a rescan", len(hits), err)
	}
}

// TestMemoryReconcilerPoisonedInputLogsOnceAndKeepsServing covers the
// bounded-staleness contract: a malformed and a symlinked .md fail every
// sync, log exactly one line per failure streak, retry only at tick cadence,
// leave the previous index contents in place, and the first read after the
// failed sync surfaces the scan error.
func TestMemoryReconcilerPoisonedInputLogsOnceAndKeepsServing(t *testing.T) {
	ctx := context.Background()
	s, spy, source := newFreshnessStore(t)
	if err := os.MkdirAll(source.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	logs := &logCapture{}
	r := startReconciler(t, s, 50*time.Millisecond, logs, nil)
	attachReconciler(s, r.fallback)

	if _, err := source.Save(ctx, memory.Entry{Title: "Steady fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "survives poison", Why: "The good entry."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return countIndexEntries(t, s, memory.ScopeProject) == 1 })
	logs.reset()

	if err := os.WriteFile(filepath.Join(source.ProjectDir(), "broken.md"), []byte("this file is not a memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.md"), filepath.Join(source.ProjectDir(), "linked.md")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return logs.count() >= 1 })
	if logs.count() != 1 {
		t.Fatalf("failure streak logged %d lines, want exactly one", logs.count())
	}
	// Bounded retries: over a window of roughly ten ticks the attempt count
	// stays far under a ceiling a hot loop would blow through immediately.
	a0 := spy.scanCount(memory.ScopeProject)
	time.Sleep(500 * time.Millisecond)
	a1 := spy.scanCount(memory.ScopeProject)
	if grew := a1 - a0; grew > 20 {
		t.Fatalf("scan attempts grew by %d in 500ms, want tick-cadence retries", grew)
	}
	if logs.count() != 1 {
		t.Fatalf("failure streak logged %d lines after the window, want exactly one", logs.count())
	}
	// Bounded staleness: the first read after the failed sync rescans and
	// surfaces the scan error instead of silently serving stale rows.
	if _, err := s.Search(ctx, memory.Query{Text: "survives poison", Scope: memory.ScopeProject}); err == nil {
		t.Fatal("search after the failed sync returned no error, want the surfaced scan error")
	}
	// The previous index contents survive the streak: once the poison is
	// gone, the pre-poison entry is served again.
	if err := os.Remove(filepath.Join(source.ProjectDir(), "broken.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source.ProjectDir(), "linked.md")); err != nil {
		t.Fatal(err)
	}
	var hits []memory.Result
	waitFor(t, 5*time.Second, func() bool {
		var err error
		hits, err = s.Search(ctx, memory.Query{Text: "survives poison", Scope: memory.ScopeProject})
		return err == nil && len(hits) == 1
	})
	if hits[0].Title != "Steady fact" {
		t.Fatalf("served %+v, want the pre-poison entry", hits[0])
	}
}

// TestMemoryReconcilerWatcherErrorDegradesThenRecovers injects a watcher
// error through the errs seam - the least-flaky injection, because no
// OS-specific watch breaking is involved and the real watcher stays healthy,
// so the tick's re-Add genuinely re-confirms the watch. The scope must
// degrade, the read path must rescan, and the tick must clear the mark.
func TestMemoryReconcilerWatcherErrorDegradesThenRecovers(t *testing.T) {
	ctx := context.Background()
	s, spy, source := newFreshnessStore(t)
	if err := os.MkdirAll(source.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	logs := &logCapture{}
	fakeErrs := make(chan error, 4)
	// The fallback tick is the only thing that clears degradation, so it is
	// kept long enough that no tick can fire between observing the mark and
	// asserting the degraded rescan below.
	r := startReconciler(t, s, 2*time.Second, logs, fakeErrs)
	attachReconciler(s, r.fallback)

	if _, err := source.Save(ctx, memory.Entry{Title: "Watched fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood, Summary: "degradation marker", Why: "Healthy before the error."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool { return scopeFresh(s, memory.ScopeProject) })

	fakeErrs <- errors.New("injected watcher failure")
	waitFor(t, 5*time.Second, func() bool { return scopeDegraded(s, memory.ScopeProject) })
	before := spy.scanCount(memory.ScopeProject)
	if _, err := s.Search(ctx, memory.Query{Text: "degradation marker", Scope: memory.ScopeProject}); err != nil {
		t.Fatal(err)
	}
	if got := spy.scanCount(memory.ScopeProject); got <= before {
		t.Fatalf("project scans %d -> %d while degraded, want a rescan", before, got)
	}
	waitFor(t, 10*time.Second, func() bool { return !scopeDegraded(s, memory.ScopeProject) })
	// Freshness resumes: with the TTL widened for determinism, a read no
	// longer rescans.
	s.mu.Lock()
	s.fallback = 10 * time.Second
	s.mu.Unlock()
	waitFor(t, 5*time.Second, func() bool { return scopeFresh(s, memory.ScopeProject) })
	before = spy.scanCount(memory.ScopeProject)
	if _, err := s.Search(ctx, memory.Query{Text: "degradation marker", Scope: memory.ScopeProject}); err != nil {
		t.Fatal(err)
	}
	if got := spy.scanCount(memory.ScopeProject); got != before {
		t.Fatalf("project scans %d -> %d after recovery, want the freshness skip back", before, got)
	}
}

// TestStartMemoryIndexReconcilerLifecycle proves the exported start contract:
// a Markdown store accepts exactly one reconciler, the stop func is
// idempotent, a stopped store starts again, the owned wrapper's Close runs a
// dropped stop defensively, and the process-local backend is refused.
func TestStartMemoryIndexReconcilerLifecycle(t *testing.T) {
	root := t.TempDir()
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org-memories"), "example.org")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	inner, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: root, OrgID: "example.org"})
	if err != nil {
		t.Fatal(err)
	}
	owned := &ownedMarkdownStore{Store: inner, index: index}

	stop, ok := StartMemoryIndexReconciler(owned, 50*time.Millisecond)
	if !ok {
		t.Fatal("markdown store must accept a reconciler")
	}
	if _, ok := StartMemoryIndexReconciler(owned, 50*time.Millisecond); ok {
		t.Fatal("second start while attached must be refused")
	}
	stop()
	stop()

	restart, ok := StartMemoryIndexReconciler(owned, 50*time.Millisecond)
	if !ok {
		t.Fatal("restart after stop must be accepted")
	}
	// Close runs the recorded stop defensively; the deferred stop must then be
	// a harmless no-op rather than touching a closed index.
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	restart()

	ephemeral, err := memory.Open(memory.Config{Backend: memory.BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer ephemeral.Close()
	if _, ok := StartMemoryIndexReconciler(ephemeral, 50*time.Millisecond); ok {
		t.Fatal("process-local store must refuse a reconciler")
	}
}
