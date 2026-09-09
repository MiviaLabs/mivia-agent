package chat

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// newTestBenchSession builds a plain in-memory session for race/benchmark
// tests that never touch persistence - relocated from concurrency_test.go
// (deleted with the legacy file-backed session store) minus its now-unused
// SessionDir setup.
func newTestBenchSession(tb testing.TB, model string) *Session {
	tb.Helper()
	res := &config.Resolved{Model: model, SystemPrompt: "sys"}
	return NewSession(res, &fakeCompleter{out: "ok"})
}

// TestSessionMessagesConcurrentReadWrite verifies that concurrent reads of
// s.Messages from the TUI (using MessagesCopy) do not race with writes from
// sendAgent (using s.mu.Lock). This reproduces and verifies the fix for
// Finding #1 from the bug audit.
func TestSessionMessagesConcurrentReadWrite(t *testing.T) {
	s := newTestBenchSession(t, "race-read-write")

	// Seed some messages.
	s.mu.Lock()
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	var iters atomic.Int64
	const target = int64(20000)

	// Writer: simulates sendAgent writing s.Messages = loop.Messages under the lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for iters.Load() < target {
			s.mu.Lock()
			s.Messages = []provider.Message{
				{Role: provider.RoleSystem, Content: "sys"},
				{Role: provider.RoleUser, Content: "hi"},
				{Role: provider.RoleAssistant, Content: "reply"},
				{Role: provider.RoleTool, Content: "tool result", Name: "read_file"},
				{Role: provider.RoleUser, Content: "follow-up"},
				{Role: provider.RoleAssistant, Content: "follow-up reply"},
			}
			s.mu.Unlock()
			iters.Add(1)
			runtime.Gosched()
		}
	}()

	// Reader: uses the safe MessagesCopy() accessor (TUI's fixed pattern).
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iters.Load() < target {
				msgs := s.MessagesCopy()
				if len(msgs) > 0 {
					_ = msgs[int(iters.Load())%len(msgs)]
				}
				_ = s.UserTurns()
				runtime.Gosched()
			}
		}()
	}

	wg.Wait()
}

// TestSessionMessagesConcurrentLoadMore reproduces the exact interleaving from
// the bug audit, now with the fixed accessor pattern.
func TestSessionMessagesConcurrentLoadMore(t *testing.T) {
	s := newTestBenchSession(t, "race-load-more")

	// Large message set to trigger loadMoreMessages-style iteration.
	s.mu.Lock()
	msgs := make([]provider.Message, 200)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = provider.Message{Role: provider.RoleUser, Content: "user msg"}
		} else {
			msgs[i] = provider.Message{Role: provider.RoleAssistant, Content: "assistant msg"}
		}
	}
	s.Messages = msgs
	s.mu.Unlock()

	var wg sync.WaitGroup
	var iters atomic.Int64
	const target = int64(10000)

	// Writer: sendAgent pattern - replace Messages completely.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for iters.Load() < target {
			s.mu.Lock()
			if iters.Load()%2 == 0 {
				s.Messages = s.Messages[:50]
			} else {
				s.Messages = s.Messages[:cap(s.Messages)]
			}
			s.mu.Unlock()
			iters.Add(1)
			runtime.Gosched()
		}
	}()

	// Reader: TUI loadMoreMessages pattern - uses MessagesCopy() (fixed).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for iters.Load() < target {
			msgs := s.MessagesCopy()
			maxIdx := len(msgs) - 1
			if maxIdx < 0 {
				continue
			}
			for j := 50; j >= 0 && j <= maxIdx; j-- {
				_ = msgs[j]
			}
			runtime.Gosched()
		}
	}()

	// Additional reader: appendBlock pattern - uses MessagesCount() (fixed).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for iters.Load() < target {
			n := s.MessagesCount()
			_ = min(n, 100)
			runtime.Gosched()
		}
	}()

	wg.Wait()
}

// TestSessionMessagesRaceDetector uses the Go race detector to verify
// that the fixed accessor methods (MessagesCopy, MessagesCount, UserTurns)
// do not race with concurrent writes under the mutex.
func TestSessionMessagesRaceDetector(t *testing.T) {
	s := newTestBenchSession(t, "race-detector")

	// Seed.
	s.mu.Lock()
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "world"},
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	var done atomic.Bool
	const opsPerGoroutine = 5000

	// Writer goroutine - simulates sendAgent writing under lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < opsPerGoroutine; i++ {
			if done.Load() {
				return
			}
			s.mu.Lock()
			s.Messages = []provider.Message{
				{Role: provider.RoleSystem, Content: "sys"},
				{Role: provider.RoleUser, Content: "new"},
				{Role: provider.RoleAssistant, Content: "response"},
			}
			s.mu.Unlock()
			runtime.Gosched()
		}
	}()

	// Reader goroutine - uses the safe MessagesCopy() accessor.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < opsPerGoroutine; i++ {
			if done.Load() {
				return
			}
			msgs := s.MessagesCopy()
			if len(msgs) > 0 {
				_ = msgs[len(msgs)-1]
			}
			runtime.Gosched()
		}
	}()

	// Third goroutine: uses UserTurns() (read lock).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < opsPerGoroutine; i++ {
			if done.Load() {
				return
			}
			_ = s.UserTurns()
			runtime.Gosched()
		}
	}()

	// Fourth goroutine: uses MessagesCount() (read lock).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < opsPerGoroutine; i++ {
			if done.Load() {
				return
			}
			_ = s.MessagesCount()
			runtime.Gosched()
		}
	}()

	wg.Wait()
}
