package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type toolExecResult struct {
	index           int // original position in ToolCalls slice
	toolCall        provider.ToolCall
	result          string
	truncated       bool // whether result was truncated for history
	err             error
	ephemeralMarker string
	// hookRuns are the lifecycle hooks that fired for this call, for display.
	hookRuns []runtime.HookRun
}

type toolTask struct {
	call       provider.ToolCall
	raw        json.RawMessage
	capability tools.Capability
	timeout    time.Duration
	callCtx    context.Context
	cancel     context.CancelFunc
}

type toolScheduler struct {
	limit chan struct{}
	mu    sync.Mutex
	locks map[string]*keyLock
}

// keyLock wraps a per-key mutex channel with a reference count so the
// scheduler can clean up entries that are no longer in use, preventing
// unbounded map growth over long sessions.
type keyLock struct {
	ch   chan struct{}
	refs int32
}

func newToolScheduler(limit int) *toolScheduler {
	if limit <= 0 {
		limit = 4
	}
	return &toolScheduler{limit: make(chan struct{}, limit), locks: make(map[string]*keyLock)}
}

func (s *toolScheduler) acquire(ctx context.Context, key string) (func(), error) {
	select {
	case s.limit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if key == "" {
		return func() { <-s.limit }, nil
	}
	s.mu.Lock()
	kl := s.locks[key]
	if kl == nil {
		kl = &keyLock{ch: make(chan struct{}, 1)}
		s.locks[key] = kl
	}
	kl.refs++
	s.mu.Unlock()
	select {
	case kl.ch <- struct{}{}:
		return func() {
			<-kl.ch
			s.mu.Lock()
			kl.refs--
			// Only clean up when this goroutine was the last reference
			// AND no one is waiting on the channel.
			if kl.refs <= 0 && len(kl.ch) == 0 {
				delete(s.locks, key)
			}
			s.mu.Unlock()
			<-s.limit
		}, nil
	case <-ctx.Done():
		s.mu.Lock()
		kl.refs--
		// A canceled waiter can be the last reference, so it owes the same
		// cleanup as the release path. Keys are per file path, so skipping it
		// grows the map without bound over a long session.
		if kl.refs <= 0 && len(kl.ch) == 0 {
			delete(s.locks, key)
		}
		s.mu.Unlock()
		<-s.limit
		return nil, ctx.Err()
	}
}
