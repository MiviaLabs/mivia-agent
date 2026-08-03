package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// assertDeclineReceived waits (2s bound) for the parked asker to be unblocked
// with the wire-format responder-terminal decline sentinel.
func assertDeclineReceived(t *testing.T, answerCh <-chan string) {
	t.Helper()
	want := agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonResponderTerminal
	select {
	case got := <-answerCh:
		if got != want {
			t.Fatalf("answer = %q, want decline %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked asker not unblocked by the decline")
	}
}

// assertAskDeclinedEvent asserts the ledger carries a task_ask_declined event
// attributed to the asker task with the given attempt and ask id.
func assertAskDeclinedEvent(t *testing.T, repo ledger.LedgerRepository, runID, askerTask, attemptID, askID string) {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind != LifecycleKindTaskAskDeclined {
			continue
		}
		found = true
		if e.TaskID != askerTask {
			t.Fatalf("decline event TaskID = %q, want asker %q", e.TaskID, askerTask)
		}
		if e.AttemptID != attemptID {
			t.Fatalf("decline event AttemptID = %q, want asker attempt", e.AttemptID)
		}
		var p struct {
			AskID string `json:"ask_id"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		if p.AskID != askID {
			t.Fatalf("decline event ask_id = %q", p.AskID)
		}
	}
	if !found {
		t.Fatal("no task_ask_declined event in ledger")
	}
}

// assertDeclineMessageSummary asserts ListRunMessages surfaces the decline as
// an ask_declined entry attributed to the asker: present unfiltered and under
// the asker filter, absent under the responder filter.
func assertDeclineMessageSummary(t *testing.T, c Coordinator, runID, askerTask, responderTask, askID string) {
	t.Helper()
	msgs, err := c.ListRunMessages(context.Background(), runID, "")
	if err != nil {
		t.Fatal(err)
	}
	var decl *MessageSummary
	for i := range msgs {
		if msgs[i].Kind == MessageKindAskDeclined {
			decl = &msgs[i]
		}
	}
	if decl == nil {
		t.Fatalf("ListRunMessages lacks ask_declined entry: %+v", msgs)
	}
	if decl.TaskID != askerTask || decl.MessageID != askID {
		t.Fatalf("decline summary = %+v, want asker %q ask %q", decl, askerTask, askID)
	}
	if decl.Synopsis != "ask declined: "+agentmsg.DeclineReasonResponderTerminal {
		t.Fatalf("decline synopsis = %q", decl.Synopsis)
	}
	// Filtering by the asker task keeps the entry; filtering by the responder
	// task drops it (it is attributed to the asker).
	byAsker, err := c.ListRunMessages(context.Background(), runID, askerTask)
	if err != nil {
		t.Fatal(err)
	}
	hasDecl := false
	for _, m := range byAsker {
		if m.Kind == MessageKindAskDeclined {
			hasDecl = true
		}
	}
	if !hasDecl {
		t.Fatalf("asker-filtered run_messages lost the decline entry: %+v", byAsker)
	}
	byResponder, err := c.ListRunMessages(context.Background(), runID, responderTask)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range byResponder {
		if m.Kind == MessageKindAskDeclined {
			t.Fatalf("responder-filtered run_messages must not include the decline: %+v", byResponder)
		}
	}
}
