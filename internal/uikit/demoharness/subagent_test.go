package demoharness

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// drainThread reads a thread turn's events until the channel closes,
// collecting the progress updates and the final text along the way.
func drainThread(t *testing.T, events <-chan uievent.Event) (progress []*uievent.Progress, text string) {
	t.Helper()
	for ev := range events {
		switch b := ev.Body.(type) {
		case uievent.ToolOutputBody:
			if b.Progress != nil {
				progress = append(progress, b.Progress)
			}
		case uievent.TextEndBody:
			text = b.Text
		}
	}
	return progress, text
}

// TestSubagentScenarioFeedsProgressAndThreads: the fixture's dispatch
// turn streams subagent progress (the panel's live feed), and Thread
// resolves the scripted thread with its history and a working Send.
func TestSubagentScenarioFeedsProgressAndThreads(t *testing.T) {
	h, err := New("subagent", 0)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := h.Send(context.Background(), intent.Send{Text: "scout the config constants"})
	if err != nil {
		t.Fatal(err)
	}
	progress, _ := drainThread(t, handle.Events())
	if len(progress) < 3 {
		t.Fatalf("the dispatch turn streamed %d progress updates, want at least 3", len(progress))
	}
	if progress[len(progress)-1].Status != "done" {
		t.Errorf("last progress status %q, want done", progress[len(progress)-1].Status)
	}

	conv, ok := h.Thread("sa-1")
	if !ok {
		t.Fatal("Thread(sa-1) did not resolve the scripted thread")
	}
	if hist := conv.History(); len(hist) != 2 || hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Fatalf("thread history %+v, want the fixture's user+assistant pair", hist)
	}

	th, err := conv.Send(context.Background(), intent.Send{Text: "what next"})
	if err != nil {
		t.Fatal(err)
	}
	_, reply := drainThread(t, th.Events())
	if !strings.Contains(reply, "thresholds") {
		t.Errorf("thread reply %q does not carry the scripted text", reply)
	}
	if hist := conv.History(); len(hist) != 3 || hist[2].Text != "what next" {
		t.Fatalf("thread history after send %+v, want the sent message appended", hist)
	}
}

// TestThreadUnknownCallIDResolvesToNothing: an id the fixture does not
// carry gets no Conversation - the panel's step-log fallback path.
func TestThreadUnknownCallIDResolvesToNothing(t *testing.T) {
	h, err := New("subagent", 0)
	if err != nil {
		t.Fatal(err)
	}
	if conv, ok := h.Thread("nope"); ok {
		t.Errorf("Thread(nope) resolved %T, want nothing", conv)
	}
}

// TestThreadReplyIsPaced: a paced harness paces the thread's reply the
// same way it paces main turns, so the embedded screen's streaming path
// sees deltas arrive over time, not one dump.
func TestThreadReplyIsPaced(t *testing.T) {
	h, err := New("subagent", 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	conv, ok := h.Thread("sa-1")
	if !ok {
		t.Fatal("fixture thread missing")
	}
	th, err := conv.Send(context.Background(), intent.Send{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	events := th.Events()
	start := time.Now()
	var deltas int
	for ev := range events {
		if _, ok := ev.Body.(uievent.TextDeltaBody); ok {
			deltas++
		}
	}
	if deltas < 2 {
		t.Fatalf("reply streamed %d deltas, want a chunked stream", deltas)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("reply arrived in %v; pacing was not applied", elapsed)
	}
}
