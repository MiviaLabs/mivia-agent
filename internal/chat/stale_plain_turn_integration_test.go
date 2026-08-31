package chat

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type overlappingPlainCompleter struct {
	mu       sync.Mutex
	calls    int
	started  chan int
	allowOne chan struct{}
	allowTwo chan struct{}
}

func (c *overlappingPlainCompleter) Name() string { return "overlap" }

func (c *overlappingPlainCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.ChatStream(ctx, req, io.Discard)
}

// ChatStream delegates to ChatTurn, as a real completer does. Holding the
// blocking handshake only here made this double disagree with every provider,
// so a caller reaching ChatTurn never blocked and the test hung on started.
func (c *overlappingPlainCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	req.Stream = true
	req.StreamWriter = w
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *overlappingPlainCompleter) chatStreamLegacy(ctx context.Context, _ provider.Request, _ io.Writer) (string, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	c.started <- call
	allow := c.allowOne
	if call == 2 {
		allow = c.allowTwo
	}
	select {
	case <-allow:
		return "reply", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *overlappingPlainCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	content, err := c.chatStreamLegacy(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, content)
	}
	return &provider.Response{Content: content, FinishReason: "stop"}, nil
}

func TestIntegrationStalePlainTurnDoesNotAutosave(t *testing.T) {
	comp := &overlappingPlainCompleter{
		started:  make(chan int, 2),
		allowOne: make(chan struct{}),
		allowTwo: make(chan struct{}),
	}
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "model", SystemPrompt: "sys"}, comp)
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewSaveManager(store, "model", "test")
	sess.SetSessionStore(store, manager)
	sess.Messages = append(sess.Messages,
		provider.Message{Role: provider.RoleUser, Content: "existing question"},
		provider.Message{Role: provider.RoleAssistant, Content: "existing answer"},
	)

	firstDone := make(chan error, 1)
	go func() {
		_, err := sess.SendUser(context.Background(), "first", io.Discard)
		firstDone <- err
	}()
	if call := <-comp.started; call != 1 {
		t.Fatalf("first provider call = %d", call)
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := sess.SendUser(context.Background(), "second", io.Discard)
		secondDone <- err
	}()
	if call := <-comp.started; call != 2 {
		t.Fatalf("second provider call = %d", call)
	}

	// Complete the stale first turn while the newer turn is still active.
	close(comp.allowOne)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := manager.Metrics().SaveAfterTurnCount; got != 0 {
		t.Fatalf("stale turn autosaved %d time(s) while newer turn was active", got)
	}

	close(comp.allowTwo)
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if got := manager.Metrics().SaveAfterTurnCount; got != 1 {
		t.Fatalf("completed current turn saves = %d, want 1", got)
	}
}
