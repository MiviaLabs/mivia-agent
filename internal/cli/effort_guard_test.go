package cli

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// startBlockedOrchestration registers a run owned by sessionID that stays
// in-flight until the returned release is called. A fan-out with wait="none"
// leaves exactly this state: no active turn, work still running.
func startBlockedOrchestration(t *testing.T, sessionID string) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	err := dispatcher.Register(runtime.Subagent, "blocked", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "blocked"}}, "effort-guard")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	cliorchestrate.StoreTestRunHandle(snap.RunID, c, h, repo, dispatcher, sessionID)
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(gate)
		}
		cliorchestrate.RunHandlesForTest.Delete(snap.RunID)
	})
	return func() {
		if closed {
			return
		}
		closed = true
		close(gate)
		select {
		case <-h.Done():
		case <-time.After(5 * time.Second):
			t.Error("orchestration did not finish after release")
		}
	}
}

// /effort must refuse for the same reason /model does: nested handlers read the
// dial live on every step, so a fan-out still executing against this binding
// would have its depth changed underneath it.
func TestIntegrationEffortRefusedWhileOrchestrationIsActive(t *testing.T) {
	res := effortCatalogConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	cleanup, err := AttachSessionDispatcher(sess, t.TempDir(), effortThinker, config.DefaultSubagentConfig,
		&AgentSessionState{AllowProjectSkills: true}, nil, SessionRouting{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	release := startBlockedOrchestration(t, sess.SessionID)

	if sess.HasActiveTurn() {
		t.Fatal("the fan-out must outlive the turn for this to reproduce")
	}
	if err := sess.CheckSwitchAllowed(); err == nil {
		t.Fatal("the switch guard must refuse while orchestration is active")
	}
	err = sess.SetReasoningEffort(reasoning.Low)
	if err == nil {
		t.Fatal("/effort changed the dial of a run already in flight")
	}
	if !strings.Contains(err.Error(), "orchestration") {
		t.Fatalf("refusal = %v, want it to name the orchestration that owns the binding", err)
	}
	if got := sess.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("a refused change altered the effort to %q", got)
	}

	notice := SafeEffortError(err)
	if strings.Contains(notice, "model switching") {
		t.Fatalf("an effort refusal talks about switching models: %q", notice)
	}
	if !strings.Contains(notice, "orchestration") {
		t.Fatalf("refusal = %q, want it to name what holds the dial", notice)
	}
	// The picker footer is the narrowest place this lands: a 80-column terminal
	// leaves 52 columns inside the frame, and a longer notice is truncated.
	if len(notice) > 52 {
		t.Fatalf("notice is %d columns, too long for the dialog footer: %q", len(notice), notice)
	}

	release()
	if err := sess.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatalf("SetReasoningEffort after the run finished: %v", err)
	}
}

// The mapping itself, not the wording either side of it. Every earlier check on
// this refusal caught a reworded guard by accident - on the length of the
// notice, or on the substring "model switching" - so a copy-edit that shortened
// the guard while keeping "orchestration" passed and /effort went back to
// naming a command the user did not type.
func TestSafeEffortErrorRewritesTheGuardsOwnRefusal(t *testing.T) {
	guard := OrchestrationSwitchGuard("effort-mapping")
	release := startBlockedOrchestration(t, "effort-mapping")
	defer release()

	err := guard()
	if err == nil {
		t.Fatal("the guard allowed a switch while orchestration is active")
	}
	if got := SafeEffortError(err); got != EffortOrchestrationNotice {
		t.Fatalf("SafeEffortError(guard refusal) = %q, want %q", got, EffortOrchestrationNotice)
	}
}
