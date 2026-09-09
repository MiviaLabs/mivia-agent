package chat

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/orchestrationnotify"
)

// TestBuildAgentTurnOptionsUsesConfiguredRequestTimeout pins the wiring
// config -> session -> snapshot -> agent.Options: a Resolved that carries a
// [chat] request_timeout_seconds value must reach the agent loop as
// Options.RequestTimeout, replacing the old compiled 15-minute literal.
func TestBuildAgentTurnOptionsUsesConfiguredRequestTimeout(t *testing.T) {
	res := &config.Resolved{
		ProviderName:       "test",
		Model:              "test-model",
		ChatRequestTimeout: 1200 * time.Second,
	}
	sess := NewSession(res, &fakeCompleter{out: "ok"})
	if sess.RequestTimeout != 1200*time.Second {
		t.Fatalf("session request timeout = %s, want 1200s", sess.RequestTimeout)
	}
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if snapshot.requestTimeout != 1200*time.Second {
		t.Fatalf("snapshot request timeout = %s, want session value 1200s", snapshot.requestTimeout)
	}
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	if opts.RequestTimeout != 1200*time.Second {
		t.Fatalf("agent.Options.RequestTimeout = %s, want the configured 1200s", opts.RequestTimeout)
	}
}

func TestBuildAgentTurnOptionsDrainsChildNotificationAtStepBoundary(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	payload, _ := json.Marshal(map[string]string{"message_id": "msg-1", "kind": "question", "synopsis": "need input", "content_ref": "ref-1"})
	orchestrationnotify.Publish(ledger.LifecycleEvent{ID: "evt-1", SessionID: snapshot.sessionID, RunID: "run-1", TaskID: "task-1", Payload: payload})
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	got := opts.BeforeStep()
	if len(got) != 1 || got[0].Name != "orchestration" || got[0].Content == "" {
		t.Fatalf("BeforeStep() = %#v, want one orchestration notification", got)
	}
	if again := opts.BeforeStep(); again != nil {
		t.Fatalf("second BeforeStep() = %#v, want drained", again)
	}
	if opts.InterruptCh == nil || opts.MailboxPending == nil || opts.MailboxPendingInterrupt == nil {
		t.Fatal("root notification wake/pending hooks are not wired")
	}
}

// TestBuildAgentTurnOptionsDefaultsZeroRequestTimeout proves a session built
// without config (hand-built Resolved, zero ChatRequestTimeout) falls back
// to DefaultRequestTimeout instead of handing the loop a zero deadline.
func TestBuildAgentTurnOptionsDefaultsZeroRequestTimeout(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	if opts.RequestTimeout != DefaultRequestTimeout {
		t.Fatalf("agent.Options.RequestTimeout = %s, want fallback %s", opts.RequestTimeout, DefaultRequestTimeout)
	}
}

// TestBuildAgentTurnOptions_MailboxPendingReflectsOrchestrationState pins
// the MailboxPending closure's own body: it must actually delegate to
// orchestrationnotify.Pending for this session, not just exist.
func TestBuildAgentTurnOptions_MailboxPendingReflectsOrchestrationState(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, &fakeCompleter{out: "ok"})
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	opts := sess.buildAgentTurnOptions(snapshot, "probe", io.Discard, nil, nil)
	if opts.MailboxPending() {
		t.Fatal("MailboxPending() = true before anything was published")
	}
	payload, _ := json.Marshal(map[string]string{"message_id": "msg-1", "kind": "question", "synopsis": "need input", "content_ref": "ref-1"})
	orchestrationnotify.Publish(ledger.LifecycleEvent{ID: "evt-2", SessionID: snapshot.sessionID, RunID: "run-1", TaskID: "task-1", Payload: payload})
	if !opts.MailboxPending() {
		t.Fatal("MailboxPending() = false after a lifecycle event was published for this session")
	}
	if !opts.MailboxPendingInterrupt() {
		t.Fatal("MailboxPendingInterrupt() = false after a lifecycle event was published for this session")
	}
}
