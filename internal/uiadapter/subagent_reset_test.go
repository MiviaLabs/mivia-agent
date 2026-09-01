package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// A subagent's assistant_reset reaches exactly ONE viewer: this dialog. The
// root transcript filters every subagent kind but tool output, so nothing
// downstream can repair what is kept here.
//
// These tests drive SubagentThreads.HandleEvent - the real entry point the
// progress registrar calls - with the event sequence internal/subagents
// actually produces on a schema retry, rather than calling applyEvent with a
// hand-built uievent.

func subagentEventOf(kind agent.EventKind, content, detail string) agent.Event {
	return agent.Event{
		Kind:    kind,
		Content: content,
		Detail:  detail,
		Origin:  agent.EventOrigin{TaskID: "task-1", Agent: "reviewer", Depth: 1},
	}
}

// replyOf returns the assistant text the dialog would show.
func replyOf(t *testing.T, evs ...agent.Event) string {
	t.Helper()
	threads := uiadapter.NewSubagentThreads()
	for _, ev := range evs {
		threads.HandleEvent(ev, uiadapter.TranslateOptions{})
	}
	conv, ok := threads.Thread("task-1")
	if !ok {
		t.Fatal("no thread was registered for the run; the test proves nothing")
	}
	var text string
	for _, m := range conv.History() {
		if m.Role == "assistant" && m.Text != "" {
			text = m.Text
		}
	}
	return text
}

// TestTheDialogDropsARejectedReplyThatDidNotStream is the worse of the two
// shapes. The text-end arm writes only into an EMPTY message, so without the
// reset the rejected reply stayed and the ACCEPTED one was discarded - the
// dialog showed the answer the agent threw away.
func TestTheDialogDropsARejectedReplyThatDidNotStream(t *testing.T) {
	got := replyOf(t,
		subagentEventOf(agent.EventAssistant, "the rejected reply", ""),
		subagentEventOf(agent.EventAssistantReset, "", "schema_retry"),
		subagentEventOf(agent.EventAssistant, "the accepted reply", ""),
	)

	if got != "the accepted reply" {
		t.Errorf("the dialog shows %q; the reader is looking at the reply the "+
			"agent rejected, not the one it acted on", got)
	}
}

// TestTheDialogDropsARejectedReplyThatStreamed is the streaming shape: the two
// attempts concatenate into one answer.
func TestTheDialogDropsARejectedReplyThatStreamed(t *testing.T) {
	got := replyOf(t,
		subagentEventOf(agent.EventAssistant, "rejected", "delta"),
		subagentEventOf(agent.EventAssistantReset, "", "schema_retry"),
		subagentEventOf(agent.EventAssistant, "accepted", "delta"),
	)

	if got != "accepted" {
		t.Errorf("the dialog shows %q, which welds the abandoned attempt to its "+
			"replacement", got)
	}
}

// TestAResetKeepsTheToolCallsThatRan is the limit. A retry re-drives the
// answer; the tool calls before it ran, had effects, and are not replayed.
func TestAResetKeepsTheToolCallsThatRan(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	for _, ev := range []agent.Event{
		{
			Kind: agent.EventToolStart, ToolCallID: "c1", Name: "read_file",
			Origin: agent.EventOrigin{TaskID: "task-1", Agent: "reviewer", Depth: 1},
		},
		subagentEventOf(agent.EventAssistant, "the rejected reply", ""),
		subagentEventOf(agent.EventAssistantReset, "", "schema_retry"),
	} {
		threads.HandleEvent(ev, uiadapter.TranslateOptions{})
	}

	conv, ok := threads.Thread("task-1")
	if !ok {
		t.Fatal("no thread was registered for the run")
	}
	var calls int
	for _, m := range conv.History() {
		calls += len(m.ToolCalls)
	}
	if calls == 0 {
		t.Error("the reset removed the tool call that ran before it; that work is " +
			"not re-driven, so its record is the only one there will be")
	}
}
