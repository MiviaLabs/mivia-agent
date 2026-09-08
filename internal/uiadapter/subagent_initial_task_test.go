package uiadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	uiadapter "github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// A dispatch whose only description travels in Event.Detail (TaskDescription
// empty) must still seed the thread with the parent task as its first user
// message: the recordInitialTask fallback reads Detail when TaskDescription
// is blank.
func TestSubagentInitialTaskFallsBackToEventDetail(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	origin := agent.EventOrigin{
		TaskID: "task-detail-fallback",
		Agent:  "auditor",
	}
	begin := agent.Event{Kind: agent.EventSubagentBegin, Origin: origin, Detail: "audit the diff from detail"}

	threads.HandleEvent(begin, uiadapter.TranslateOptions{})

	conv, ok := threads.Thread(origin.TaskID)
	if !ok {
		t.Fatal("expected subagent thread after begin")
	}
	history := conv.History()
	if len(history) == 0 {
		t.Fatal("history is empty, want the Detail fallback recorded as the initial user task")
	}
	if history[0].Role != "user" || history[0].Text != "audit the diff from detail" {
		t.Fatalf("initial history message = %+v, want user task %q from Event.Detail", history[0], "audit the diff from detail")
	}
}

// A begin event with no description anywhere (both TaskDescription and Detail
// blank) must record nothing: recordInitialTask's blank-text guard returns
// before touching history, so no empty user row may appear.
func TestSubagentInitialTaskBlankDescriptionRecordsNothing(t *testing.T) {
	threads := uiadapter.NewSubagentThreads()
	origin := agent.EventOrigin{
		TaskID: "task-blank-description",
		Agent:  "auditor",
	}
	begin := agent.Event{Kind: agent.EventSubagentBegin, Origin: origin, Detail: "   \t"}

	threads.HandleEvent(begin, uiadapter.TranslateOptions{})

	conv, ok := threads.Thread(origin.TaskID)
	if !ok {
		t.Fatal("expected subagent thread after begin")
	}
	for _, msg := range conv.History() {
		if msg.Role == "user" {
			t.Fatalf("blank description must not record a user message, got %+v", msg)
		}
	}
}
