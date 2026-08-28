package clichat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestProcessLineChatToolsOnWrapsClassicAgentUI: with tools on, the classic
// REPL must attach the agent event handler to the session and stream the turn
// through the classic agent UI wrapper - the surface that renders tool
// activity between the user line and the final answer.
func TestProcessLineChatToolsOnWrapsClassicAgentUI(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{ProviderName: "p", Model: "m"}, nullCompleter{})
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	buf := new(bytes.Buffer)
	term := &Terminal{out: buf}
	renderer := NewChatRenderer(term, sess.CurrentModel())
	input := NewInputBuffer("> ")
	res := &config.Resolved{ProviderName: "p", Model: "m", ShowIterationNotices: true}

	if err := processLineChat("hello", sess, res, true, term, renderer, input, "m"); err != nil {
		t.Fatalf("processLineChat with tools on: %v", err)
	}
	out := stripAnsiOut(buf.String())
	if !strings.Contains(out, "hello") {
		t.Fatalf("output = %q, want the echoed user line", out)
	}
	if sess.OnAgentEvent == nil {
		t.Fatal("tools-on turn left sess.OnAgentEvent unset; tool activity would be invisible")
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want the turn to have drained the queue", notes)
	}
}
