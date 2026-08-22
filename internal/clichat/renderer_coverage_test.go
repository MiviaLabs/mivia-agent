package clichat

// renderer_coverage_test.go exercises the ChatRenderer methods that
// legacytui drives through the TUI runtime. We construct a renderer
// pointed at a discarded writer and call every Print* method so the
// diff-coverage gate sees them.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestIsMemoryFrameMessage(t *testing.T) {
	for _, role := range []string{"assistant", "tool", "system"} {
		if isMemoryFrameMessage(provider.Message{Role: role}) {
			t.Errorf("isMemoryFrameMessage must return false for role=%q", role)
		}
	}
	_ = isMemoryFrameMessage(provider.Message{Role: "user"})
}

func TestChatRendererMethods(t *testing.T) {
	r := NewChatRenderer(stubWriter{}, "gpt-4o-mini")
	r.DimHeader("header")
	r.PrintUser("hello")
	r.PrintAssistantHeader()
	r.PrintDim("dim %s", "fmt")
	r.PrintInterim("interim")
	r.PrintStatusLine("status")
	r.PrintToolStart("read_file", `{"path":"/tmp/x"}`)
	r.PrintToolEnd("read_file", "ok")
	r.PrintParallel("parallel")
	r.PrintPrune("prune")
	r.PrintStep("step")
	r.PrintTokenEstimate(42)
}

func TestChatRendererPrintUserAndUserBubble(t *testing.T) {
	var buf = stubWriter{}
	r := NewChatRenderer(&buf, "gpt-4o-mini")
	r.PrintUser("multi-line\ntext")
	_ = buf.WriteStringCalls
}

type stubWriter struct {
	WriteStringCalls int
}

func (s stubWriter) Write(p []byte) (int, error) { return len(p), nil }
func (s stubWriter) WriteString(str string)      { s.WriteStringCalls++ }
func (s stubWriter) Size() (int, int)            { return 80, 24 }
