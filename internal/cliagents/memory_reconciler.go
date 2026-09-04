package cliagents

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/fsnotify/fsnotify"
)

// memoryDebounceDelay coalesces a burst of watcher events for one scope into
// a single sync: the per-scope timer resets on every event and fires only
// after this quiet period.
const memoryDebounceDelay = 250 * time.Millisecond

// defaultMemoryIndexRefreshInterval is the fallback tick and freshness TTL
// when the caller passes no interval. The config resolves the production
// value; this only bounds a misconfigured zero.
const defaultMemoryIndexRefreshInterval = 30 * time.Second

// memoryReconciler keeps a Markdown store's derived index fresh: watcher
// events debounce into store syncs, a fallback tick re-adds lost watches and
// re-syncs every configured scope, and watcher errors degrade scopes so the
// read path rescans until a tick re-confirms the watch. Every sync goes
// through the store's syncScope, never the index directly, so background
// work serializes with Save and Delete on the store mutex.
type memoryReconciler struct {
	store    *markdownStore
	fallback time.Duration
	logf     func(format string, args ...any)

	// watcher is created by start. events and errs default to the watcher's
	// channels; tests inject fakes before start to drive the pump without a
	// real filesystem error.
	watcher *fsnotify.Watcher
	events  <-chan fsnotify.Event
	errs    <-chan error

	// dirs maps each watched directory to its scope. Loop goroutine only.
	dirs map[string]memory.Scope

	// timers holds one pending debounce timer per scope. Loop goroutine only;
	// timer callbacks hand scopes back through syncCh.
	timers map[memory.Scope]*time.Timer
	syncCh chan memory.Scope

	// watchErrStreak counts consecutive watcher errors without an
	// intervening fully-watched tick, so one streak logs one line. Loop
	// goroutine only.
	watchErrStreak int

	// syncFailing records scopes with a failing sync streak, so one streak
	// logs one line instead of one line per retry. stateMu guards it because
	// sync goroutines touch it.
	stateMu     sync.Mutex
	syncFailing map[memory.Scope]bool

	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	syncs    sync.WaitGroup
	stopOnce sync.Once
}

// StartMemoryIndexReconciler attaches the filesystem reconciler to a
// Markdown-backed memory store and returns its stop func. ok is false for
// any other backend or a store that already carries a reconciler. The caller
// must invoke stop before the store's index closes; stop is idempotent. The
// attached check and the set share one lock step, so concurrent starts
// resolve to at most one reconciler winning the flag.
func StartMemoryIndexReconciler(store memory.Store, fallback time.Duration) (stop func(), ok bool) {
	inner, ok := markdownStoreOf(store)
	if !ok {
		return nil, false
	}
	r := newMemoryReconciler(inner, fallback, nil)
	if err := r.start(); err != nil {
		r.logf("memory reconciler: %v", err)
		r.Stop()
		return nil, false
	}
	inner.mu.Lock()
	if inner.reconcilerAttached {
		inner.mu.Unlock()
		r.Stop()
		return nil, false
	}
	inner.reconcilerAttached = true
	inner.fallback = r.fallback
	inner.mu.Unlock()
	stopFn := func() {
		inner.mu.Lock()
		inner.reconcilerAttached = false
		inner.mu.Unlock()
		r.Stop()
	}
	// The owned wrapper also carries the stop func: its Close runs it
	// defensively, so store teardown cannot race a still-running reconciler
	// even when the caller drops the returned stop.
	if owned, isOwned := store.(*ownedMarkdownStore); isOwned {
		owned.stopMu.Lock()
		owned.stop = stopFn
		owned.stopMu.Unlock()
	}
	return stopFn, true
}

// markdownStoreOf unwraps the Markdown implementation behind the store
// interface: the owned wrapper from the support path, or the raw store from
// OpenMarkdownStore.
func markdownStoreOf(store memory.Store) (*markdownStore, bool) {
	switch s := store.(type) {
	case *ownedMarkdownStore:
		inner, ok := s.Store.(*markdownStore)
		return inner, ok
	case *markdownStore:
		return s, true
	default:
		return nil, false
	}
}

func newMemoryReconciler(store *markdownStore, fallback time.Duration, logf func(format string, args ...any)) *memoryReconciler {
	if fallback <= 0 {
		fallback = defaultMemoryIndexRefreshInterval
	}
	if logf == nil {
		logf = log.Printf
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &memoryReconciler{
		store:       store,
		fallback:    fallback,
		logf:        logf,
		dirs:        make(map[string]memory.Scope),
		timers:      make(map[memory.Scope]*time.Timer),
		syncCh:      make(chan memory.Scope),
		syncFailing: make(map[memory.Scope]bool),
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}
}

// start creates the watcher and launches the reconcile loop. A missing
// memory directory is not an error; the tick retries the Add until the
// directory exists.
func (r *memoryReconciler) start() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		close(r.done)
		return fmt.Errorf("memory reconciler watcher: %w", err)
	}
	r.watcher = watcher
	if r.events == nil {
		r.events = watcher.Events
	}
	if r.errs == nil {
		r.errs = watcher.Errors
	}
	r.reattachWatches()
	go r.loop()
	return nil
}

// Stop cancels the loop, closes the watcher, and waits for the loop and all
// in-flight syncs to settle, so nothing touches the store after Stop
// returns. It never takes the store mutex: an in-flight sync finishes on its
// own, bounded by one scan plus the busy-retry budget, and events straggling
// past the cancel find a dead context. Idempotent.
func (r *memoryReconciler) Stop() {
	r.stopOnce.Do(func() {
		r.cancel()
		if r.watcher != nil {
			_ = r.watcher.Close()
		}
		<-r.done
		r.syncs.Wait()
	})
}

func (r *memoryReconciler) loop() {
	defer close(r.done)
	defer r.stopTimers()
	ticker := time.NewTicker(r.fallback)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case ev, ok := <-r.events:
			if !ok {
				return
			}
			r.handleEvent(ev)
		case err, ok := <-r.errs:
			if !ok {
				return
			}
			r.handleWatcherError(err)
		case scope := <-r.syncCh:
			r.startSync(scope)
		case <-ticker.C:
			r.tick()
		}
	}
}

func (r *memoryReconciler) handleEvent(ev fsnotify.Event) {
	scope, ok := r.scopeForPath(ev.Name)
	if !ok {
		return
	}
	if t := r.timers[scope]; t != nil {
		t.Stop()
	}
	r.timers[scope] = time.AfterFunc(memoryDebounceDelay, func() {
		select {
		case r.syncCh <- scope:
		case <-r.ctx.Done():
		}
	})
}

// scopeForPath maps a changed path to the scope watching its directory; the
// changed path may be the watched directory itself.
func (r *memoryReconciler) scopeForPath(path string) (memory.Scope, bool) {
	if scope, ok := r.dirs[filepath.Dir(path)]; ok {
		return scope, true
	}
	scope, ok := r.dirs[path]
	return scope, ok
}

// startSync spawns one scope sync. Loop goroutine only, so no Add can race
// Stop's Wait.
func (r *memoryReconciler) startSync(scope memory.Scope) {
	r.syncs.Add(1)
	go func() {
		defer r.syncs.Done()
		r.syncScope(scope)
	}()
}

// syncScope runs one store sync and maintains the failure streak. A canceled
// context skips the work, so syncs straggling past Stop touch nothing.
func (r *memoryReconciler) syncScope(scope memory.Scope) {
	if r.ctx.Err() != nil {
		return
	}
	if err := r.store.syncScope(r.ctx, scope); err != nil {
		r.noteSyncFailure(scope, err)
		return
	}
	r.clearSyncFailure(scope)
}

func (r *memoryReconciler) noteSyncFailure(scope memory.Scope, err error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.syncFailing[scope] {
		return
	}
	r.syncFailing[scope] = true
	r.logf("memory reconciler: %s scope sync failed; the previous index stays until a sync succeeds: %v", scope, err)
}

func (r *memoryReconciler) clearSyncFailure(scope memory.Scope) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	delete(r.syncFailing, scope)
}

// handleWatcherError degrades every configured scope: fsnotify errors carry
// no path, so no scope's freshness is trustworthy until a tick re-confirms
// the watch.
func (r *memoryReconciler) handleWatcherError(err error) {
	if r.watchErrStreak == 0 {
		r.logf("memory reconciler: watcher error; scopes degrade to fallback rescans until a tick re-adds them: %v", err)
	}
	r.watchErrStreak++
	for _, scope := range r.configuredScopes() {
		r.store.setDegraded(scope, true)
	}
}

// tick re-adds lost or missing watches, then re-syncs every configured
// scope. The timer runs regardless of watcher health: watcher events are the
// optimization, the tick is the correctness bound.
func (r *memoryReconciler) tick() {
	r.reattachWatches()
	for _, scope := range r.configuredScopes() {
		r.startSync(scope)
	}
}

// reattachWatches re-Adds every configured scope directory. fsnotify
// auto-removes a watch when the watched path is deleted or renamed, so the
// tick is also the recovery path. A missing directory is retried silently
// each tick; a successful Add - a documented no-op for an already-watched
// path - clears the scope's degradation.
func (r *memoryReconciler) reattachWatches() {
	allWatched := true
	for _, scope := range r.configuredScopes() {
		dir := r.dirFor(scope)
		if dir == "" {
			continue
		}
		if err := r.watcher.Add(dir); err != nil {
			allWatched = false
			delete(r.dirs, dir)
			continue
		}
		r.dirs[dir] = scope
		r.store.setDegraded(scope, false)
	}
	if allWatched {
		r.watchErrStreak = 0
	}
}

// configuredScopes lists the scopes the reconciler owns: project always, org
// only when both an org identity and an org directory exist, so an
// unconfigured org scope never syncs, watches, or validates.
func (r *memoryReconciler) configuredScopes() []memory.Scope {
	scopes := []memory.Scope{memory.ScopeProject}
	if r.store.cfg.OrgID != "" && r.store.cfg.Source.OrgDir() != "" {
		scopes = append(scopes, memory.ScopeOrg)
	}
	return scopes
}

func (r *memoryReconciler) dirFor(scope memory.Scope) string {
	switch scope {
	case memory.ScopeProject:
		return r.store.cfg.Source.ProjectDir()
	case memory.ScopeOrg:
		if r.store.cfg.OrgID != "" && r.store.cfg.Source.OrgDir() != "" {
			return r.store.cfg.Source.OrgDir()
		}
		return ""
	default:
		return ""
	}
}

func (r *memoryReconciler) stopTimers() {
	for scope, t := range r.timers {
		t.Stop()
		delete(r.timers, scope)
	}
}
