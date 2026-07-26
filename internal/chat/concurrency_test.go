package chat

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ──────────────────────────────────────────────────────────────────────
// Concurrency tests — these exercise the Session.mu mutex and verify
// that parallel Save/Load/Delete/List operations don't race or corrupt.
// ──────────────────────────────────────────────────────────────────────

// TestConcurrentSaveDifferentSessions verifies N goroutines saving to
// different session names simultaneously. All saves should succeed.
func TestConcurrentSaveDifferentSessions(t *testing.T) {
	s := newTestBenchSession(t, "concurrent-diff")
	const (
		nSessions = 20
		msgsEach  = 10
	)

	// Build a message payload.
	s.Messages = nil
	for i := 0; i < msgsEach; i++ {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "ping"})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "pong"})
	}

	var wg sync.WaitGroup
	errCh := make(chan error, nSessions)

	for i := 0; i < nSessions; i++ {
		wg.Add(1)
		name := sprintf("concurrent-session-%d", i)
		go func(n string) {
			defer wg.Done()
			if err := s.Save(n); err != nil {
				errCh <- err
			}
		}(name)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent save: %v", err)
	}

	// Verify all sessions are listed.
	infos, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != nSessions {
		t.Fatalf("expected %d sessions, got %d", nSessions, len(infos))
	}

	// Cleanup all sessions.
	for _, info := range infos {
		if err := s.DeleteSession(info.Name); err != nil {
			t.Errorf("cleanup delete %q: %v", info.Name, err)
		}
	}
}

// TestConcurrentSaveSameSession verifies that many goroutines saving
// to the same session name are serialized by the mutex and don't race.
func TestConcurrentSaveSameSession(t *testing.T) {
	s := newTestBenchSession(t, "concurrent-same")
	const (
		nWorkers    = 10
		msgsPerTurn = 5
	)

	s.Messages = nil
	for i := 0; i < msgsPerTurn; i++ {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "hi"})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "hey"})
	}

	var wg sync.WaitGroup
	for i := 0; i < nWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each worker saves and then loads back, verifying integrity.
			if err := s.Save("shared-session"); err != nil {
				t.Errorf("save: %v", err)
				return
			}
			// Verify the session is loadable.
			s2 := newTestBenchSession(t, "verify")
			s2.SessionDir = s.SessionDir
			if err := s2.Load("shared-session"); err != nil {
				t.Errorf("load after save: %v", err)
			}
		}()
	}
	wg.Wait()

	// Final load: should have the expected message count.
	if err := s.Load("shared-session"); err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != msgsPerTurn*2 {
		t.Fatalf("expected %d messages, got %d", msgsPerTurn*2, len(s.Messages))
	}

	// Load via a different session to double-check persistence.
	s3 := newTestBenchSession(t, "verify2")
	s3.SessionDir = s.SessionDir
	if err := s3.Load("shared-session"); err != nil {
		t.Fatal(err)
	}
	if len(s3.Messages) != msgsPerTurn*2 {
		t.Fatalf("expected %d messages after re-load, got %d", msgsPerTurn*2, len(s3.Messages))
	}
}

// TestConcurrentSaveAndList verifies that ListSessions does not race with Save.
func TestConcurrentSaveAndList(t *testing.T) {
	s := newTestBenchSession(t, "concurrent-list")
	const nSaves = 30

	s.Messages = nil
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "hi"})
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ho"})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < nSaves; i++ {
			name := sprintf("race-session-%d", i)
			_ = s.Save(name)
		}
	}()

	// While saves happen, hammer ListSessions concurrently.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, err := s.ListSessions()
				if err != nil && err.Error() != "session directory not set" {
					t.Errorf("list: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	// Cleanup.
	infos, _ := s.ListSessions()
	for _, info := range infos {
		_ = s.DeleteSession(info.Name)
	}
}

// TestConcurrentSaveAndDeleteSameSession verifies that concurrent
// Save + Delete on the same session name is safe (one will win).
func TestConcurrentSaveAndDeleteSameSession(t *testing.T) {
	s := newTestBenchSession(t, "concurrent-save-delete")
	const nOps = 20

	s.Messages = nil
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "x"})
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "y"})

	var wg sync.WaitGroup
	for i := 0; i < nOps; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				_ = s.Save("tug-of-war")
			} else {
				_ = s.DeleteSession("tug-of-war")
			}
		}(i)
	}
	wg.Wait()

	// Final state is either deleted or present — both are valid.
	// Just verify no panics and that the remaining state (if any) is loadable.
	infos, _ := s.ListSessions()
	for _, info := range infos {
		if info.Name == "tug-of-war" {
			s2 := newTestBenchSession(t, "verify-tug")
			s2.SessionDir = s.SessionDir
			if err := s2.Load("tug-of-war"); err != nil {
				t.Errorf("load surviving session: %v", err)
			}
			_ = s.DeleteSession("tug-of-war")
		}
	}
}

// TestConcurrentLoad verifies loading the same session from multiple
// goroutines produces identical results.
func TestConcurrentLoad(t *testing.T) {
	s := newTestBenchSession(t, "concurrent-load-source")
	const nMsgs = 50

	s.Messages = nil
	for i := 0; i < nMsgs; i++ {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleUser,
			Content: sprintf("message %d", i),
		})
	}
	if err := s.Save("load-source"); err != nil {
		t.Fatal(err)
	}

	const nReaders = 10
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		msgLen int
	)

	for i := 0; i < nReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s2 := newTestBenchSession(t, "reader")
			s2.SessionDir = s.SessionDir
			if err := s2.Load("load-source"); err != nil {
				t.Errorf("concurrent load: %v", err)
				return
			}
			mu.Lock()
			if msgLen == 0 {
				msgLen = len(s2.Messages)
			} else if len(s2.Messages) != msgLen {
				t.Errorf("inconsistent message count: got %d, want %d", len(s2.Messages), msgLen)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if msgLen != nMsgs {
		t.Fatalf("expected %d messages, got %d", nMsgs, msgLen)
	}
}

// TestConcurrentSavePreservesMessages verifies that concurrent saves
// to different session names all succeed and messages are persisted.
func TestConcurrentSavePreservesMessages(t *testing.T) {
	s := newTestBenchSession(t, "concurrent-multi")
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "original"},
		{Role: provider.RoleAssistant, Content: "ok"},
	}

	var wg sync.WaitGroup
	const n = 10
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Each goroutine saves to its own uniquely-named session.
			s2 := newTestBenchSession(t, fmt.Sprintf("worker-%d", id))
			s2.SessionDir = s.SessionDir
			s2.Messages = append([]provider.Message{}, s.Messages...)
			s2.Messages = append(s2.Messages,
				provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("user-%d", id)},
				provider.Message{Role: provider.RoleAssistant, Content: "ok"},
			)
			name := fmt.Sprintf("concurrent-session-%d", id)
			if err := s2.Save(name); err != nil {
				t.Errorf("save %q: %v", name, err)
			}
		}(i)
	}
	wg.Wait()

	infos, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != n {
		t.Fatalf("expected %d sessions, got %d", n, len(infos))
	}

	// Cleanup.
	for _, info := range infos {
		_ = s.DeleteSession(info.Name)
	}
}

// ──────────────────────────────────────────────────────────────────────
// Benchmarks — performance of save/load at various scales.
// Run with: go test -bench=Benchmark -benchmem ./internal/chat/
// ──────────────────────────────────────────────────────────────────────

// BenchmarkSave10 saves a session with 10 messages.
func BenchmarkSave10(b *testing.B) { benchmarkSave(b, 10) }

// BenchmarkSave100 saves a session with 100 messages.
func BenchmarkSave100(b *testing.B) { benchmarkSave(b, 100) }

// BenchmarkSave1000 saves a session with 1000 messages (forces chunking).
func BenchmarkSave1000(b *testing.B) { benchmarkSave(b, 1000) }

// BenchmarkLoad10 loads a session with 10 messages.
func BenchmarkLoad10(b *testing.B) { benchmarkLoad(b, 10) }

// BenchmarkLoad100 loads a session with 100 messages.
func BenchmarkLoad100(b *testing.B) { benchmarkLoad(b, 100) }

// BenchmarkLoad1000 loads a session with 1000 messages.
func BenchmarkLoad1000(b *testing.B) { benchmarkLoad(b, 1000) }

// BenchmarkSaveLargeMessages saves a session with 20 large messages (each ~50KB).
func BenchmarkSaveLargeMessages(b *testing.B) {
	bigMsg := provider.Message{
		Role:    provider.RoleUser,
		Content: randString(50 * 1024),
	}
	s := newTestBenchSession(b, "bench-large-save")
	s.Messages = nil
	for i := 0; i < 20; i++ {
		s.Messages = append(s.Messages, bigMsg)
	}
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ok"})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := sprintf("bench-large-%d", i)
		if err := s.Save(name); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadLargeMessages loads a session with 20 large messages (each ~50KB).
func BenchmarkLoadLargeMessages(b *testing.B) {
	// First, create a source session and save it.
	s := newTestBenchSession(b, "bench-large-load-source")
	bigMsg := provider.Message{
		Role:    provider.RoleUser,
		Content: randString(50 * 1024),
	}
	s.Messages = nil
	for i := 0; i < 20; i++ {
		s.Messages = append(s.Messages, bigMsg)
	}
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	if err := s.Save("large-load-source"); err != nil {
		b.Fatal(err)
	}
	sessionDir := s.SessionDir

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s2 := newTestBenchSession(b, "bench-large-load")
		s2.SessionDir = sessionDir
		if err := s2.Load("large-load-source"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConcurrentSave measures throughput of concurrent saves to
// different session names.
func BenchmarkConcurrentSave(b *testing.B) {
	s := newTestBenchSession(b, "bench-concurrent")
	const msgs = 20
	s.Messages = nil
	for i := 0; i < msgs/2; i++ {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "hi"})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ho"})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := rand.Int()
		for pb.Next() {
			name := sprintf("bench-concurrent-%d", id)
			if err := s.Save(name); err != nil {
				b.Error(err)
			}
			id++
		}
	})
}

// ──────────────────────────────────────────────────────────────────────
// Benchmarks for writeJSONL / readJSONL directly (micro benchmarks)
// ──────────────────────────────────────────────────────────────────────

func BenchmarkWriteJSONL10(b *testing.B)   { benchmarkWriteJSONL(b, 10) }
func BenchmarkWriteJSONL100(b *testing.B)  { benchmarkWriteJSONL(b, 100) }
func BenchmarkWriteJSONL1000(b *testing.B) { benchmarkWriteJSONL(b, 1000) }

func BenchmarkReadJSONL10(b *testing.B)   { benchmarkReadJSONL(b, 10) }
func BenchmarkReadJSONL100(b *testing.B)  { benchmarkReadJSONL(b, 100) }
func BenchmarkReadJSONL1000(b *testing.B) { benchmarkReadJSONL(b, 1000) }

// ──────────────────────────────────────────────────────────────────────
// Helpers for benchmarks
// ──────────────────────────────────────────────────────────────────────

func benchmarkSave(b *testing.B, n int) {
	s := newTestBenchSession(b, sprintf("bench-save-%d", n))
	s.Messages = nil
	for i := 0; i < n/2; i++ {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "test message payload for benchmarking purposes " + randString(30)})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: randString(200)})
	}
	// Ensure at least 2 messages if n is small.
	if len(s.Messages) < 2 {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "hi"})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := sprintf("bench-save-%d-%d", n, i)
		if err := s.Save(name); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkLoad(b *testing.B, n int) {
	// Create source session once.
	s := newTestBenchSession(b, sprintf("bench-load-source-%d", n))
	s.Messages = nil
	for i := 0; i < n/2; i++ {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "test message payload for benchmarking " + randString(30)})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: randString(200)})
	}
	if len(s.Messages) < 2 {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "hi"})
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	}
	srcName := sprintf("bench-load-source-%d", n)
	if err := s.Save(srcName); err != nil {
		b.Fatal(err)
	}
	sessionDir := s.SessionDir

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s2 := newTestBenchSession(b, sprintf("bench-load-%d-%d", n, i))
		s2.SessionDir = sessionDir
		if err := s2.Load(srcName); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkWriteJSONL(b *testing.B, n int) {
	msgs := make([]provider.Message, n)
	for i := range msgs {
		msgs[i] = provider.Message{
			Role:    provider.RoleUser,
			Content: randString(100),
		}
	}
	dir := b.TempDir()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(dir, sprintf("bench-write-%d.jsonl", i))
		if err := writeJSONL(path, msgs); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkReadJSONL(b *testing.B, n int) {
	// Write a file once.
	msgs := make([]provider.Message, n)
	for i := range msgs {
		msgs[i] = provider.Message{
			Role:    provider.RoleUser,
			Content: randString(100),
		}
	}
	dir := b.TempDir()
	path := filepath.Join(dir, "bench-read-source.jsonl")
	if err := writeJSONL(path, msgs); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := readJSONL(path)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func newTestBenchSession(tb testing.TB, model string) *Session {
	tb.Helper()
	dir := tb.TempDir()
	sessionDir := filepath.Join(dir, ".mivia", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		tb.Fatal(err)
	}

	res := &config.Resolved{Model: model, SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{out: "ok"})
	s.SessionDir = sessionDir
	return s
}

func sprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

// randString returns a random alphanumeric string of given length.
func randString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 .,!?;:"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
