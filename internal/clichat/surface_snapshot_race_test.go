package clichat

import (
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// publishSurfaceLoop stands in for a turn boundary admitting a deferred tool:
// TryPublishAgentSurface writes Session.Tools under the session lock. Every
// reader of that field must go through AgentSurfaceSnapshot, which is the
// documented contract for the field and the point of these tests.
func publishSurfaceLoop(t *testing.T, sess *chat.Session, iterations int) func() {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			sess.TryPublishAgentSurface(chat.AgentSurfacePublication{
				Prompt:   "root",
				MaxSteps: 4,
				Registry: tierRegistry("read_file", "grep"),
			})
		}
	}()
	return func() { <-done }
}

// TestClassicToolsSlashSnapshotsTheSurface is the same read on the classic
// REPL path, which reads Session.Tools three times unlocked.
func TestClassicToolsSlashSnapshotsTheSurface(t *testing.T) {
	res := switchableDeferredRes()
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = tierRegistry("read_file")
	term := &Terminal{out: &syncBuffer{}}
	wait := publishSurfaceLoop(t, sess, 300)
	for i := 0; i < 300; i++ {
		if _, _, err := handleSlashInfo("/tools", []string{"/tools"}, sess, res, true, term); err != nil {
			t.Fatal(err)
		}
	}
	wait()
}

// TestModelBindingSnapshotsTheSurface: /model builds its candidate binding
// BEFORE SwitchBinding refuses on activeTurns, so the build runs mid-turn and
// its RemainderSpoolFromRegistry(sess.Tools) read races the publication.
func TestModelBindingSnapshotsTheSurface(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	res := switchableDeferredRes()
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = tierRegistry("read_file")
	sess.UseTools = true
	wait := publishSurfaceLoop(t, sess, 60)
	for i := 0; i < 60; i++ {
		binding, err := buildModelBinding(sess, res, dir, res.ProviderName, res.Model, nil)
		if err != nil {
			t.Fatalf("buildModelBinding: %v", err)
		}
		if binding.Dispatcher != nil {
			binding.Dispatcher.Close()
		}
	}
	wait()
}

// syncBuffer is a race-free sink for the classic terminal writer.
type syncBuffer struct {
	mu sync.Mutex
	n  int
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n += len(p)
	return len(p), nil
}
