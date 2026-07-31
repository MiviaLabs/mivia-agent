package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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
	skillRegistry, err := skills.LoadMarkdown(root, completer, "model")
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
	skillRegistry, err := skills.LoadMarkdown(root, completer, "model")
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
