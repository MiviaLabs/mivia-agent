package cli

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

type forceSendIntegrationCompleter struct {
	mu           sync.Mutex
	requests     []provider.Request
	firstStarted chan struct{}
}

func (c *forceSendIntegrationCompleter) Name() string { return "force-send-integration" }

func (c *forceSendIntegrationCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}

func (c *forceSendIntegrationCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}

func (c *forceSendIntegrationCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	snapshot := req
	snapshot.Messages = append([]provider.Message(nil), req.Messages...)
	c.requests = append(c.requests, snapshot)
	n := len(c.requests)
	c.mu.Unlock()
	if n == 1 {
		if req.StreamWriter != nil {
			_, _ = io.WriteString(req.StreamWriter, "partial first answer")
		}
		close(c.firstStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &provider.Response{Content: "second answer", FinishReason: "stop"}, nil
}

func (c *forceSendIntegrationCompleter) Requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

func TestIntegrationForceSendCanceledTurnRemainsInContextHistory(t *testing.T) {
	root := t.TempDir()
	completer := &forceSendIntegrationCompleter{firstStarted: make(chan struct{})}
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	session.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	store, err := setupSessionContext(session, root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sp := startScrollProgram(t, func(m *tuiModel) {
		m.session = session
		m.toolsOn = true
		m.waiting = false
	})
	sp.send(keyRunes("first question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-completer.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not reach the provider")
	}

	sp.send(keyRunes("second question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool {
		return m.waiting && len(m.pendingQueue) == 1
	}) {
		t.Fatal("second question was not queued")
	}
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(3*time.Second, func(m *tuiModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0 && len(completer.Requests()) == 2
	}) {
		t.Fatal("force-send did not cancel the first turn and complete the queued turn")
	}

	requests := completer.Requests()
	if got := messagesContent(requests[1].Messages); !strings.Contains(got, "first question") || !strings.Contains(got, "partial first answer") {
		t.Fatalf("second request lost canceled-turn history: %q; session=%q", got, messagesContent(session.MessagesCopy()))
	}
	if got := messagesContent(session.MessagesCopy()); !strings.Contains(got, "first question") || !strings.Contains(got, "partial first answer") {
		t.Fatalf("session history lost canceled-turn content: %q", got)
	}
}
