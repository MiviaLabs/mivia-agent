package chat

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type fakeCompleter struct {
	err error
	out string
}

func (f *fakeCompleter) Name() string { return "fake" }

func (f *fakeCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return f.ChatStream(ctx, req, io.Discard)
}

func (f *fakeCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if w != nil {
		_, _ = io.WriteString(w, f.out)
	}
	return f.out, nil
}

func (f *fakeCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &provider.Response{Content: f.out, FinishReason: "stop"}, nil
}

func TestClearAndUserTurns(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{out: "ok"})
	if len(s.Messages) != 1 {
		t.Fatalf("want system message")
	}
	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.UserTurns() != 1 {
		t.Fatalf("turns=%d", s.UserTurns())
	}
	s.Clear()
	if s.UserTurns() != 0 || len(s.Messages) != 1 {
		t.Fatalf("clear failed: turns=%d msgs=%d", s.UserTurns(), len(s.Messages))
	}
}

func TestFailedSendDropsUserTurn(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{err: context.Canceled})
	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if s.UserTurns() != 0 {
		t.Fatalf("user turn should be dropped, turns=%d", s.UserTurns())
	}
}

// gatedCompleter blocks the first ChatStream until release is closed (or ctx done).
// Subsequent calls return immediately with outFast.
type gatedCompleter struct {
	firstEntered chan struct{}
	releaseFirst chan struct{}
	outSlow      string
	outFast      string
	calls        int
	mu           sync.Mutex
}

func (g *gatedCompleter) Name() string { return "gated" }

func (g *gatedCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return g.ChatStream(ctx, req, io.Discard)
}

func (g *gatedCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()
	if n == 1 {
		close(g.firstEntered)
		select {
		case <-g.releaseFirst:
			if w != nil {
				_, _ = io.WriteString(w, g.outSlow)
			}
			return g.outSlow, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if w != nil {
		_, _ = io.WriteString(w, g.outFast)
	}
	return g.outFast, nil
}

func (g *gatedCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	out, err := g.ChatStream(ctx, req, io.Discard)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: out, FinishReason: "stop"}, nil
}

// TestSendUserStaleTurnDoesNotOverwrite ensures a slow first SendUser cannot
// overwrite Messages after a newer turn has completed (force-send race).
func TestSendUserStaleTurnDoesNotOverwrite(t *testing.T) {
	g := &gatedCompleter{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		outSlow:      "REPLY_FIRST",
		outFast:      "REPLY_SECOND",
	}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, g)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	done1 := make(chan error, 1)
	go func() {
		_, err := s.SendUser(ctx1, "first", io.Discard)
		done1 <- err
	}()

	select {
	case <-g.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first SendUser did not enter ChatStream")
	}

	// Newer turn while first is still in-flight (simulates force-send overlap).
	cancel1()
	_, err := s.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatalf("second SendUser: %v", err)
	}

	// Unblock first if it is still waiting (cancelled path may already have returned).
	close(g.releaseFirst)
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("first SendUser did not finish")
	}

	// Latest turn wins: history must reflect "second", not a late write of "first".
	s.mu.Lock()
	defer s.mu.Unlock()
	var users []string
	var lastAssistant string
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			users = append(users, m.Content)
		}
		if m.Role == provider.RoleAssistant {
			lastAssistant = m.Content
		}
	}
	if len(users) != 1 || users[0] != "second" {
		t.Fatalf("user messages = %v, want [second]", users)
	}
	if lastAssistant != "REPLY_SECOND" {
		t.Fatalf("assistant = %q, want REPLY_SECOND", lastAssistant)
	}
}

// TestSendUserSlowFirstCannotOverwriteFasterSecond covers the non-cancel race:
// first turn completes after second and must not clobber Messages.
func TestSendUserSlowFirstCannotOverwriteFasterSecond(t *testing.T) {
	g := &gatedCompleter{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		outSlow:      "REPLY_FIRST",
		outFast:      "REPLY_SECOND",
	}
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, g)

	done1 := make(chan error, 1)
	go func() {
		_, err := s.SendUser(context.Background(), "first", io.Discard)
		done1 <- err
	}()

	select {
	case <-g.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first SendUser did not enter ChatStream")
	}

	_, err := s.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatalf("second SendUser: %v", err)
	}

	close(g.releaseFirst)
	if err := <-done1; err != nil {
		t.Fatalf("first SendUser: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var users []string
	var lastAssistant string
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser {
			users = append(users, m.Content)
		}
		if m.Role == provider.RoleAssistant {
			lastAssistant = m.Content
		}
	}
	if len(users) != 1 || users[0] != "second" {
		t.Fatalf("user messages = %v, want [second] (stale first must not write)", users)
	}
	if lastAssistant != "REPLY_SECOND" {
		t.Fatalf("assistant = %q, want REPLY_SECOND", lastAssistant)
	}
}
