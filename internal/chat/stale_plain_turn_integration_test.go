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

func (c *overlappingPlainCompleter) ChatStream(ctx context.Context, _ provider.Request, _ io.Writer) (string, error) {
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

func (c *overlappingPlainCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "reply", FinishReason: "stop"}, nil
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
