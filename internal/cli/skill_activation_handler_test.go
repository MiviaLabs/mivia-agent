package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

type resourceSkillCompleter struct {
	requests []provider.Request
}

func (*resourceSkillCompleter) Name() string { return "resource-skill-test" }
func (c *resourceSkillCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
func (c *resourceSkillCompleter) ChatStream(ctx context.Context, req provider.Request, _ io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *resourceSkillCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		var call provider.ToolCall
		call.ID = "resource-call"
		call.Type = "function"
		call.Function.Name = tools.SkillResourceToolName
		call.Function.Arguments = `{"id":"template"}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "review complete", FinishReason: "stop"}, nil
}

func TestResourceSkillInvocationGetsOnlyItsScopedReader(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"SKILL.md":       "---\nname: review\n---\nLoad the declared template before reporting.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Required report template\"\n",
		"template.md":    "PRIVATE RESOURCE TEXT",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	completer := &resourceSkillCompleter{}
	skillRegistry, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := newSessionDispatcherMinimal(registry, completer, "model", config.DefaultSubagentConfig, 0, skillRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := registry.Get(tools.SkillResourceToolName); exists {
		t.Fatal("root registry received a scoped resource tool")
	}
	result := dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "review-run", Kind: runtime.Subagent, Name: "review", Input: json.RawMessage(`"inspect"`),
	})
	if result.Err != nil || !strings.Contains(string(result.Output), "review complete") {
		t.Fatalf("result=%s err=%v", result.Output, result.Err)
	}
	if len(completer.requests) != 2 {
		t.Fatalf("provider requests=%d", len(completer.requests))
	}
	if !strings.Contains(messagesContent(completer.requests[0].Messages), "<skill-resources>") || !strings.Contains(messagesContent(completer.requests[0].Messages), "template") {
		t.Fatalf("initial request lacked catalogue: %#v", completer.requests[0].Messages)
	}
	if strings.Contains(messagesContent(completer.requests[0].Messages), "PRIVATE RESOURCE TEXT") {
		t.Fatal("initial request eagerly included resource body")
	}
	if !strings.Contains(messagesContent(completer.requests[1].Messages), "PRIVATE RESOURCE TEXT") {
		t.Fatalf("second request lacked resource body: %#v", completer.requests[1].Messages)
	}
}

func TestDirectSkillTurnKeepsResourceReaderLocal(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nLoad the declared template before reporting.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Required report template\"\n",
		"template.md":    "PRIVATE DIRECT RESOURCE TEXT",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	completer := &resourceSkillCompleter{}
	skillRegistry, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := skillRegistry.Get("review")
	if !ok {
		t.Fatal("skill missing")
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rootTools := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	session := chat.NewSession(&config.Resolved{Model: "model"}, completer)
	session.Tools = rootTools
	session.UseTools = true
	m := &tuiModel{session: session, toolsOn: true}
	prompt, turn, err := m.prepareSkillTurn(skillSlashSpec{definition: definition, args: "check", display: "⚙ /review check"})
	if err != nil {
		t.Fatal(err)
	}
	if turn == nil || turn.Tools == rootTools || turn.Dispatcher == nil {
		t.Fatalf("turn did not receive an isolated tool surface: %#v", turn)
	}
	if _, exists := rootTools.Get(tools.SkillResourceToolName); exists {
		t.Fatal("root tools received the direct-turn reader")
	}
	if _, exists := turn.Tools.Get(tools.SkillResourceToolName); !exists {
		t.Fatal("direct turn lacks resource reader")
	}
	if !strings.Contains(prompt, "<skill-resources>") || strings.Contains(prompt, "PRIVATE DIRECT RESOURCE TEXT") {
		t.Fatalf("prompt=%q", prompt)
	}
	result := turn.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "direct-resource", Kind: runtime.Tool, Name: tools.SkillResourceToolName, Input: json.RawMessage(`{"id":"template"}`),
	})
	if result.Err != nil || !strings.Contains(string(result.Output), "PRIVATE DIRECT RESOURCE TEXT") {
		t.Fatalf("result=%s err=%v", result.Output, result.Err)
	}
	turn.Cleanup()
	if result = turn.Dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "after-cleanup", Kind: runtime.Tool, Name: tools.SkillResourceToolName, Input: json.RawMessage(`{"id":"template"}`),
	}); result.Err == nil {
		t.Fatal("closed direct-turn dispatcher accepted a resource read")
	}
}

func messagesContent(messages []provider.Message) string {
	var content []string
	for _, message := range messages {
		content = append(content, message.Content)
	}
	return strings.Join(content, "\n")
}

// queuedResourceCompleter holds its first turn open so the test can submit a
// second slash command through the live TUI queue before it is dequeued.
type queuedResourceCompleter struct {
	mu           sync.Mutex
	requests     []provider.Request
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (*queuedResourceCompleter) Name() string { return "queued-resource-test" }
func (c *queuedResourceCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	response, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}
func (c *queuedResourceCompleter) ChatStream(ctx context.Context, req provider.Request, _ io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *queuedResourceCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	snapshot := req
	snapshot.Messages = append([]provider.Message(nil), req.Messages...)
	c.requests = append(c.requests, snapshot)
	requestNumber := len(c.requests)
	if requestNumber == 1 {
		close(c.firstStarted)
	}
	c.mu.Unlock()
	switch requestNumber {
	case 1:
		select {
		case <-c.releaseFirst:
			return &provider.Response{Content: "first turn complete", FinishReason: "stop"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case 2:
		var call provider.ToolCall
		call.ID = "queued-resource-call"
		call.Type = "function"
		call.Function.Name = tools.SkillResourceToolName
		call.Function.Arguments = `{"id":"template"}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	default:
		return &provider.Response{Content: "queued review complete", FinishReason: "stop"}, nil
	}
}

func (c *queuedResourceCompleter) Requests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

func TestIntegrationQueuedResourceSkillSlashRunsAfterActiveTurn(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	if err := os.Mkdir(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\nuser-invocable: true\n---\nLoad the declared template before reporting.",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Required report template\"\n",
		"template.md":    "PRIVATE QUEUED RESOURCE TEXT",
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	completer := &queuedResourceCompleter{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	skillRegistry, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rootTools := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	session := chat.NewSession(&config.Resolved{Model: "model"}, completer)
	session.UseTools = true
	session.Tools = rootTools
	session.SetBindingSkillRegistry(skillRegistry)

	sp := startScrollProgram(t, func(m *tuiModel) {
		m.session = session
		m.toolsOn = true
		m.waiting = false
	})
	sp.send(keyRunes("first task"))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-completer.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not reach the provider")
	}

	sp.send(keyRunes("/review check"))
	sp.send(tea.KeyMsg{Type: tea.KeyEsc})
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(2*time.Second, func(m *tuiModel) bool {
		return m.waiting && len(m.pendingQueue) == 1 && len(m.pendingSkillTurns) == 1 && m.pendingSkillTurns[0] != nil
	}) {
		t.Fatal("resource skill slash was not queued through the live TUI")
	}
	if got := len(completer.Requests()); got != 1 {
		t.Fatalf("queued resource skill started before dequeue: provider requests=%d", got)
	}
	if _, exists := rootTools.Get(tools.SkillResourceToolName); exists {
		t.Fatal("queued skill leaked its reader into root tools")
	}

	close(completer.releaseFirst)
	if !sp.waitUntil(3*time.Second, func(m *tuiModel) bool {
		return !m.waiting && len(m.pendingQueue) == 0 && len(m.pendingSkillTurns) == 0 && len(completer.Requests()) == 3
	}) {
		t.Fatal("queued resource skill did not dequeue and complete")
	}
	requests := completer.Requests()
	if !strings.Contains(messagesContent(requests[1].Messages), "<skill-resources>") || strings.Contains(messagesContent(requests[1].Messages), "PRIVATE QUEUED RESOURCE TEXT") {
		t.Fatalf("queued skill initial request did not expose only its catalogue: %#v", requests[1].Messages)
	}
	if !strings.Contains(messagesContent(requests[2].Messages), "PRIVATE QUEUED RESOURCE TEXT") {
		t.Fatalf("queued skill follow-up request lacked the resource body: %#v", requests[2].Messages)
	}
	if strings.Contains(messagesContent(session.MessagesCopy()), "PRIVATE QUEUED RESOURCE TEXT") {
		t.Fatalf("persisted session leaked queued resource: %#v", session.MessagesCopy())
	}
	if !strings.Contains(messagesContent(session.MessagesCopy()), "skill resource loaded: template") {
		t.Fatalf("persisted session lacks the queued resource marker: %#v", session.MessagesCopy())
	}
}

func TestInjectSkillResourceToolConflict(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(tools.NewSkillResourceTool(nil, "test-key", 1024))
	// Conflict: tool already exists.
	_, err := injectSkillResourceTool(reg, nil)
	if err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestInjectSkillResourceToolSuccess(t *testing.T) {
	reg := tools.NewRegistry()
	clone, err := injectSkillResourceTool(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if clone == reg {
		t.Fatal("expected clone, not same registry")
	}
	if _, exists := clone.Get(tools.SkillResourceToolName); !exists {
		t.Fatal("tool not registered in clone")
	}
	// Original unchanged.
	if _, exists := reg.Get(tools.SkillResourceToolName); exists {
		t.Fatal("original registry was mutated")
	}
}
