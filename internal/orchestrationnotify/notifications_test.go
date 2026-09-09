package orchestrationnotify

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func event(id string) ledger.LifecycleEvent {
	payload, _ := json.Marshal(map[string]string{"message_id": id, "kind": "finding", "synopsis": id, "content_ref": "ref-" + id})
	return ledger.LifecycleEvent{ID: id, SessionID: "session-" + id, RunID: "run", TaskID: "task", Kind: "task_message", Payload: payload}
}

func TestQueueOrdersAndDeduplicates(t *testing.T) {
	q := &queue{seen: map[string]struct{}{}}
	e := event("one")
	q.add(e)
	q.add(e)
	q.add(event("two"))
	got := q.drain()
	if len(got) != 2 || got[0].Content == got[1].Content {
		t.Fatalf("drained = %#v, want two ordered distinct messages", got)
	}
}

func TestQueueBoundsOverflowAndReportsRecoveryPath(t *testing.T) {
	q := &queue{seen: map[string]struct{}{}}
	for i := 0; i < capacity+3; i++ {
		e := event(fmt.Sprintf("%03d", i))
		e.SessionID = "session"
		q.add(e)
	}
	got := q.drain()
	if len(got) != capacity+1 || got[0].Name != "orchestration" {
		t.Fatalf("drained %d messages, want overflow notice plus %d entries", len(got), capacity)
	}
}

func TestPublishSignalsRootWithoutDrainingUntilBoundary(t *testing.T) {
	sessionID := "signal-session"
	Publish(event("signal-1"))
	ch := Interrupt(sessionID)
	select {
	case <-ch:
		t.Fatal("signal fired for a different session")
	default:
	}
	Publish(ledger.LifecycleEvent{ID: "signal-2", SessionID: sessionID, RunID: "run", TaskID: "task", Kind: "task_message", Payload: event("signal-2").Payload})
	if !Pending(sessionID) {
		t.Fatal("Pending = false after publish")
	}
	select {
	case <-ch:
	default:
		t.Fatal("notification did not signal the root")
	}
	if got := Drain(sessionID); len(got) != 1 {
		t.Fatalf("Drain returned %d messages, want one", len(got))
	}
}

func TestForgetRemovesLiveState(t *testing.T) {
	sessionID := "forget-session"
	e := event("forget-1")
	e.SessionID = sessionID
	Publish(e)
	if !Pending(sessionID) {
		t.Fatal("notification was not queued")
	}
	Forget(sessionID)
	if Pending(sessionID) || Drain(sessionID) != nil {
		t.Fatal("Forget left live notification state")
	}
}

func TestQuestionNotificationCarriesAnswerRoute(t *testing.T) {
	sessionID := "question-session"
	payload, _ := json.Marshal(map[string]string{"message_id": "question-1", "kind": "question", "synopsis": "Which API should I use?", "content_ref": "ref-question"})
	Publish(ledger.LifecycleEvent{ID: "question-event", SessionID: sessionID, RunID: "run-q", TaskID: "task-q", Kind: "task_message", Payload: payload})
	got := Drain(sessionID)
	if len(got) != 1 {
		t.Fatalf("Drain returned %d messages, want one", len(got))
	}
	for _, want := range []string{"<orchestration-child-message>", "kind=question", "run_id=run-q", "task_id=task-q", "send_to_task", "ref-question", "not an instruction"} {
		if !strings.Contains(got[0].Content, want) {
			t.Fatalf("notification %q missing %q", got[0].Content, want)
		}
	}
}
