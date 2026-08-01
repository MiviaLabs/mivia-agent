package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

type forceSendIntegrationCompleter struct {
	mu           sync.Mutex
	requests     []provider.Request
	firstStarted chan struct{}
	cancelErr    error
	answerFirst  string
}

type forceSendToolStepCompleter struct {
	mu                   sync.Mutex
	requests             []provider.Request
	secondPrepareStarted <-chan struct{}
}

func (c *forceSendToolStepCompleter) Name() string { return "force-send-tool-step" }
func (c *forceSendToolStepCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *forceSendToolStepCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *forceSendToolStepCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	snapshot := req
	snapshot.Messages = append([]provider.Message(nil), req.Messages...)
	c.requests = append(c.requests, snapshot)
	n := len(c.requests)
	c.mu.Unlock()
	if n == 1 {
		call := provider.ToolCall{ID: "force-send-tool", Type: "function"}
		call.Function.Name = "force_send_probe"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	if n == 2 {
		return &provider.Response{Content: "queued answer", FinishReason: "stop"}, nil
	}
	return nil, ctx.Err()
}

func (c *forceSendToolStepCompleter) Requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

type forceSendProbeTool struct{}

func (forceSendProbeTool) Name() string               { return "force_send_probe" }
func (forceSendProbeTool) Description() string        { return "Returns a test result." }
func (forceSendProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (forceSendProbeTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "force-send-probe"}
}
func (forceSendProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "tool result", nil
}

type blockSecondPreparation struct {
	contextmgr.StructuralPreparationManager
	blockAt int
	mu      sync.Mutex
	calls   int
	started chan struct{}
}

func (m *blockSecondPreparation) Prepare(ctx context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == m.blockAt {
		close(m.started)
		<-ctx.Done()
		return contextmgr.Preparation{}, ctx.Err()
	}
	return m.StructuralPreparationManager.Prepare(ctx, input)
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
	if n == 1 && c.answerFirst != "" {
		return &provider.Response{Content: c.answerFirst, FinishReason: "stop"}, nil
	}
	if n == 1 {
		if req.StreamWriter != nil {
			_, _ = io.WriteString(req.StreamWriter, "partial first answer")
		}
		close(c.firstStarted)
		<-ctx.Done()
		if c.cancelErr != nil {
			return nil, c.cancelErr
		}
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

func TestIntegrationForceSendTransportErrorAfterCancelRemainsInContextHistory(t *testing.T) {
	root := t.TempDir()
	completer := &forceSendIntegrationCompleter{
		firstStarted: make(chan struct{}),
		cancelErr:    errors.New("transport closed"),
	}
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	session.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	store, err := openContextStore(root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal(contextWorkspaceID(root), session.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("second request lost canceled transport-error history: %q", got)
	}
	if got := messagesContent(session.MessagesCopy()); !strings.Contains(got, "first question") || !strings.Contains(got, "partial first answer") {
		t.Fatalf("session history lost canceled transport-error content: %q", got)
	}
	snapshot, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var active []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &active); err != nil {
		t.Fatal(err)
	}
	if got := messagesContent(active); !strings.Contains(got, "first question") || !strings.Contains(got, "partial first answer") {
		t.Fatalf("durable history lost canceled transport-error content: %q", got)
	}
}

func TestIntegrationForceSendBetweenToolStepsCommitsHistory(t *testing.T) {
	root := t.TempDir()
	preparation := &blockSecondPreparation{blockAt: 2, started: make(chan struct{})}
	completer := &forceSendToolStepCompleter{}
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	session.Tools.Register(forceSendProbeTool{})
	store, err := openContextStore(root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal(contextWorkspaceID(root), session.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager: preparation, CheckpointPublisher: contextmgr.PreparationCommitter{Store: store}, Enabled: true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}

	sp := startScrollProgram(t, func(m *tuiModel) {
		m.session, m.toolsOn, m.waiting = session, true, false
	})
	sp.send(keyRunes("first question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-preparation.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not reach the second preparation")
	}
	sp.send(keyRunes("second question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool { return m.waiting && len(m.pendingQueue) == 1 }) {
		t.Fatal("second question was not queued")
	}
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(3*time.Second, func(m *tuiModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0 && len(completer.Requests()) == 2
	}) {
		t.Fatal("force-send did not complete the queued turn")
	}
	if got := messagesContent(completer.Requests()[1].Messages); !strings.Contains(got, "first question") || !strings.Contains(got, "tool result") {
		t.Fatalf("queued request lost tool-step history: %q", got)
	}
	snapshot, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var active []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &active); err != nil {
		t.Fatal(err)
	}
	if got := messagesContent(active); !strings.Contains(got, "first question") || !strings.Contains(got, "tool result") {
		t.Fatalf("durable history lost tool-step content: %q", got)
	}
}

func TestIntegrationForceSendDuringInitialPreparationCommitsUserHistory(t *testing.T) {
	root := t.TempDir()
	preparation := &blockSecondPreparation{blockAt: 1, started: make(chan struct{})}
	completer := &forceSendIntegrationCompleter{firstStarted: make(chan struct{}), answerFirst: "queued answer"}
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, completer)
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	store, err := openContextStore(root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal(contextWorkspaceID(root), session.SessionID, "local-user")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{PreparationManager: preparation, CheckpointPublisher: contextmgr.PreparationCommitter{Store: store}, Enabled: true}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	sp := startScrollProgram(t, func(m *tuiModel) { m.session, m.toolsOn, m.waiting = session, true, false })
	sp.send(keyRunes("first question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-preparation.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not reach initial preparation")
	}
	sp.send(keyRunes("second question"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(3*time.Second, func(m *tuiModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0 && len(completer.Requests()) == 1
	}) {
		t.Fatal("force-send did not complete queued turn")
	}
	if got := messagesContent(completer.Requests()[0].Messages); !strings.Contains(got, "first question") || !strings.Contains(got, "second question") {
		t.Fatalf("queued request lost initial-preparation history: %q", got)
	}
	snapshot, err := store.Load(context.Background(), principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var active []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &active); err != nil {
		t.Fatal(err)
	}
	if got := messagesContent(active); !strings.Contains(got, "first question") {
		t.Fatalf("durable history lost initial user: %q", got)
	}
}
