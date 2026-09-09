package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// asyncFakeRunner adds ports.AsyncCompactionRunner to fakeRunner: /compact
// takes the async path only when s.runner implements that optional
// extension, which fakeRunner alone does not.
type asyncFakeRunner struct {
	*fakeRunner
	handle ports.CompactionHandle
	err    error
}

func (a *asyncFakeRunner) StartCompaction(context.Context, string) (ports.CompactionHandle, error) {
	return a.handle, a.err
}

// TestSlashCompactStartsAsyncCompactionWhenSupported covers commands.go's
// AsyncCompactionRunner branch: when the wired runner supports it, /compact
// must start the async flow instead of falling through to runner.Run.
func TestSlashCompactStartsAsyncCompactionWhenSupported(t *testing.T) {
	runner := &asyncFakeRunner{
		fakeRunner: &fakeRunner{},
		handle:     compactionTestHandle{events: make(chan ports.CompactionEvent)},
	}
	s := newScreen(t, &compactionTestConversation{}, nil, nil)
	s.SetCommandRunner(runner)

	s, _ = sendLine(t, s, "/compact")
	if s.compaction == nil {
		t.Fatal("/compact with an async-capable runner did not start async compaction")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner.Run was called %v, want the async path to skip it entirely", runner.calls)
	}
}

// TestSlashCompactReportsAsyncStartFailure covers the async path's own
// error branch: StartCompaction failing must surface as a command error,
// not fall through to runner.Run either.
func TestSlashCompactReportsAsyncStartFailure(t *testing.T) {
	runner := &asyncFakeRunner{fakeRunner: &fakeRunner{}, err: errors.New("boom")}
	s := newScreen(t, &compactionTestConversation{}, nil, nil)
	s.SetCommandRunner(runner)

	s, _ = sendLine(t, s, "/compact")
	if s.compaction != nil {
		t.Fatal("a failed StartCompaction must not leave a compaction handle armed")
	}
	if !strings.Contains(lastErrorDetail(t, s), "compact failed: boom") {
		t.Fatalf("error detail = %q, want it to explain the start failure", lastErrorDetail(t, s))
	}
}
