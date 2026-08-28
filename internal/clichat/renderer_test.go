package clichat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// mockTerminal creates a minimal terminal-like writer for testing.
type mockTerminal struct {
	*bytes.Buffer
}

func newMockTerminal() *mockTerminal {
	return &mockTerminal{Buffer: new(bytes.Buffer)}
}

func (m *mockTerminal) WriteString(s string) {
	m.Buffer.WriteString(s)
}

func (m *mockTerminal) Size() (width, height int) {
	return 80, 24
}

func TestNewChatRenderer(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "test-model")
	if r == nil {
		t.Fatal("expected non-nil renderer")
	}
	if r.model != "test-model" {
		t.Fatalf("expected model 'test-model', got %q", r.model)
	}
	if r.out != mt {
		t.Fatal("expected writer to match")
	}
}

func TestDimHeader(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.DimHeader("test")
	output := mt.String()
	if !strings.Contains(stripANSI(output), "── test ──") {
		t.Fatalf("expected header with '── test ──', got %q", output)
	}
	if !strings.Contains(output, "─") {
		t.Fatalf("expected fill characters in header, got %q", output)
	}
}

func TestPrintUser(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintUser("hello world")
	output := stripANSI(mt.String())
	if !strings.Contains(output, "── you ──") {
		t.Fatalf("expected '── you ──' header, got %q", output)
	}
	if !strings.Contains(output, "hello world") {
		t.Fatalf("expected 'hello world' in output, got %q", output)
	}
}

func TestPrintAssistantHeader(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "deepseek-v4-flash")
	r.PrintAssistantHeader()
	output := stripANSI(mt.String())
	if !strings.Contains(output, "── deepseek-v4-flash ──") {
		t.Fatalf("expected model name in header, got %q", output)
	}
}

func TestPrintToolStartEnd(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintToolStart("read_file", `{"path":"main.go"}`)
	r.PrintToolEnd("read_file", "package main")
	output := stripANSI(mt.String())
	if !strings.Contains(output, "read_file") || !strings.Contains(output, "◐") {
		t.Fatalf("expected tool start, got %q", output)
	}
	if !strings.Contains(output, "✓") || !strings.Contains(output, "package main") {
		t.Fatalf("expected tool end, got %q", output)
	}
}

func TestPrintStep(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintStep("1/∞")
	output := stripANSI(mt.String())
	if !strings.Contains(output, "1/∞") {
		t.Fatalf("expected step info, got %q", output)
	}
}

func TestPrintPrune(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintPrune("pruned 1000 tokens")
	output := stripANSI(mt.String())
	if !strings.Contains(output, "pruned 1000 tokens") {
		t.Fatalf("expected prune info, got %q", output)
	}
}

func TestPrintParallel(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintParallel("3 tools: read_file, read_file, grep")
	output := stripANSI(mt.String())
	if !strings.Contains(output, "3 tools") {
		t.Fatalf("expected parallel info, got %q", output)
	}
}

func TestPrintError(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintError("something went wrong")
	output := mt.String()
	if !strings.Contains(output, "error") || !strings.Contains(output, "something went wrong") {
		t.Fatalf("expected error message, got %q", output)
	}
}

func TestPrintTokenEstimate(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintTokenEstimate(1500)
	output := stripANSI(mt.String())
	if !strings.Contains(output, "1500") {
		t.Fatalf("expected token count 1500, got %q", output)
	}
}

func TestPrintInfo(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintInfo("session saved")
	output := mt.String()
	if !strings.Contains(output, "session saved") {
		t.Fatalf("expected info message, got %q", output)
	}
}

func TestMakeAgentUIWithRenderer(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	_, handler := newClassicAgentHandler(r, true)
	handler(agent.Event{Kind: agent.EventToolStart, Name: "grep", Detail: `{"pattern":"test"}`})
	handler(agent.Event{Kind: agent.EventToolEnd, Name: "grep", Detail: "found 2 matches"})
	handler(agent.Event{Kind: agent.EventStep, Detail: "2/∞"})
	handler(agent.Event{Kind: agent.EventPrune, Detail: "pruned 500 tokens"})
	handler(agent.Event{Kind: agent.EventToolParallel, Detail: "2 tools: read, grep"})
	handler(agent.Event{Kind: agent.EventCacheUsage, Detail: "prompt cache: 80/100 tokens cached (80%)"})
	output := stripANSI(mt.String())
	if !strings.Contains(output, "grep") || !strings.Contains(output, "◐") {
		t.Fatalf("expected tool start, got %q", output)
	}
	if !strings.Contains(output, "✓") || !strings.Contains(output, "found 2 matches") {
		t.Fatalf("expected tool end, got %q", output)
	}
	if !strings.Contains(output, "2/∞") {
		t.Fatalf("expected step, got %q", output)
	}
	if !strings.Contains(output, "pruned 500") {
		t.Fatalf("expected prune, got %q", output)
	}
	if !strings.Contains(output, "2 tools") {
		t.Fatalf("expected parallel, got %q", output)
	}
	if !strings.Contains(output, "prompt cache: 80/100 tokens cached (80%)") {
		t.Fatalf("expected cache usage status line, got %q", output)
	}
}

func TestPrintDim(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	r.PrintDim("hello %s", "world")
	output := mt.String()
	if !strings.Contains(output, AnsiDim) {
		t.Fatalf("expected dim style, got %q", output)
	}
	stripped := stripANSI(output)
	if !strings.Contains(stripped, "hello world") {
		t.Fatalf("expected 'hello world', got %q", stripped)
	}
}

func TestRenderHistory(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "test-model")
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "you are a helper"},
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi there"},
		{Role: provider.RoleUser, Content: "what is 2+2?"},
		{Role: provider.RoleAssistant, Content: "4"},
	}
	r.RenderHistory(msgs)
	output := stripANSI(mt.String())
	if !strings.Contains(output, "hello") {
		t.Fatalf("expected 'hello' in history, got %q", output)
	}
	if !strings.Contains(output, "hi there") {
		t.Fatalf("expected 'hi there' in history, got %q", output)
	}
	if !strings.Contains(output, "what is 2+2?") {
		t.Fatalf("expected 'what is 2+2?' in history, got %q", output)
	}
	if !strings.Contains(output, "4") {
		t.Fatalf("expected '4' in history, got %q", output)
	}
	if strings.Contains(output, "you are a helper") {
		t.Fatalf("expected system prompt omitted, got %q", output)
	}
}

func TestRenderHistoryWithMarkdown(t *testing.T) {
	mt := newMockTerminal()
	r := NewChatRenderer(mt, "m")
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "show code"},
		{Role: provider.RoleAssistant, Content: "use `fmt.Println` for output"},
	}
	r.RenderHistory(msgs)
	output := mt.String()
	if !strings.Contains(output, AnsiYellow) {
		t.Fatalf("expected code highlighting in markdown-rendered history, got %q", output)
	}
	stripped := stripANSI(output)
	if !strings.Contains(stripped, "fmt.Println") {
		t.Fatalf("expected code content in output, got %q", stripped)
	}
}
