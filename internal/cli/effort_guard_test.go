package cli

import (
	"context"
	"encoding/json"
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
	runHandles.Store(snap.RunID, &orchestrationHandle{
		coord:      c,
		handle:     h,
		repo:       repo,
		dispatcher: dispatcher,
		principal:  orchestrationPrincipal{sessionID: sessionID},
	})
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(gate)
		}
		runHandles.Delete(snap.RunID)
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
	cleanup, err := attachSessionDispatcher(sess, t.TempDir(), effortThinker, config.DefaultSubagentConfig,
		&agentSessionState{AllowProjectSkills: true}, nil, sessionRouting{})
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

	release()
	if err := sess.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatalf("SetReasoningEffort after the run finished: %v", err)
	}
}
