package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// boundaryCompactionSession wires a durable-context agent session against a
// shared store, the same shape the other context integration tests use.
func boundaryCompactionSession(t *testing.T) (*Session, contextstate.Principal, *[]agent.Event) {
	t.Helper()
	store, _ := openSharedContextStore(t)
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	var compactions []agent.Event
	sess.OnAgentEvent = func(ev agent.Event) {
		if ev.Kind == agent.EventCompaction {
			compactions = append(compactions, ev)
		}
	}
	return sess, principal, &compactions
}

// TestTurnBoundaryCompactionKeepsTheSessionUnderTheTrigger pins the gap the
// mid-turn pass cannot close.
//
// Preparation runs before every provider call, so the REQUEST is never over
// budget. What it never sees is whatever the final step appends afterwards -
// the closing assistant message, and the whole last tool-result batch when a
// turn ends on a step ceiling or a work limit. Nothing ran between turns, so
// that history stayed over the trigger in both the committed checkpoint and
// the user's context gauge until the next turn's first preparation, which is
// how a session came to sit at 112% with no compaction in sight.
func TestTurnBoundaryCompactionKeepsTheSessionUnderTheTrigger(t *testing.T) {
	sess, _, _ := boundaryCompactionSession(t)

	if _, err := sess.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	// Pin the budget to the exact cost of the history plus the next prompt, so
	// the turn both crosses the trigger mid-turn AND lands back over it once
	// its own reply is appended.
	forceCompactionBudget(t, sess, "follow up")
	if _, err := sess.SendUser(context.Background(), "follow up", io.Discard); err != nil {
		t.Fatal(err)
	}

	usage := sess.ContextUsage()
	if usage.BudgetTokens <= 0 {
		t.Fatalf("budget = %d, want a positive prompt budget", usage.BudgetTokens)
	}
	// 80% is contextmgr.Plan's trigger (PercentFloor(budget, 4, 5)). The whole
	// point of the boundary pass is that a turn cannot END above it.
	if usage.Percent >= 80 {
		t.Errorf("session left at %d%% (used %d of %d) after the turn, want below the 80%% trigger",
			usage.Percent, usage.UsedTokens, usage.BudgetTokens)
	}
}

// TestCompactIfNeededIsANoOpBelowTheTrigger keeps the boundary pass from
// churning: below the trigger the planner decides there is nothing to do, and
// that must be an ordinary non-compacting outcome, not the error manual
// /compact reports for the same plan.
func TestCompactIfNeededIsANoOpBelowTheTrigger(t *testing.T) {
	sess, _, compactions := boundaryCompactionSession(t)
	if _, err := sess.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	before := len(*compactions)

	compacted, err := sess.CompactIfNeeded(context.Background())
	if err != nil {
		t.Fatalf("CompactIfNeeded below the trigger returned an error: %v", err)
	}
	if compacted {
		t.Errorf("CompactIfNeeded compacted a session at %d%% of budget", sess.ContextUsage().Percent)
	}
	if len(*compactions) != before {
		t.Errorf("a no-op pass emitted %d compaction events, want 0", len(*compactions)-before)
	}
}

// TestCompactIfNeededDoesNotRepeatAPlanThatFreesNothing pins the guard against
// the pathological case: a history whose MANDATORY set alone (system, the
// core-memory frame, the current objective, the latest tool unit) already
// exceeds the compaction target crosses the trigger on every pass and retains
// exactly the same messages every time. Committing that at each boundary would
// announce a compaction, spend a summarizer call, and bump the revision
// forever without freeing a single token.
func TestCompactIfNeededDoesNotRepeatAPlanThatFreesNothing(t *testing.T) {
	sess, _, compactions := boundaryCompactionSession(t)
	if _, err := sess.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}
	forceCompactionBudget(t, sess, "follow up")
	if _, err := sess.SendUser(context.Background(), "follow up", io.Discard); err != nil {
		t.Fatal(err)
	}
	settled := len(*compactions)

	// The turn has already settled at or below the planner's floor. Repeated
	// passes must all decline: each one either falls below the trigger or
	// produces a plan that reduces nothing.
	for i := range 3 {
		compacted, err := sess.CompactIfNeeded(context.Background())
		if err != nil {
			t.Fatalf("repeat pass %d returned an error: %v", i, err)
		}
		if compacted {
			t.Fatalf("repeat pass %d compacted an already-settled history (now %d%%)",
				i, sess.ContextUsage().Percent)
		}
	}
	if len(*compactions) != settled {
		t.Errorf("repeat passes emitted %d further compaction events, want 0", len(*compactions)-settled)
	}
}

// TestCompactIfNeededDeclinesAPlanThatFreesNothing drives the session to the
// no-progress steady state and pins that the pass declines there.
//
// The case is not defensive: contextmgr.Plan sets Compacted whenever the
// trigger is crossed and planCompact runs, whether or not the retained set is
// any smaller. Once a history has been reduced to its MANDATORY floor - the
// system prompt and the current objective, which structural retention may
// never drop - a budget that leaves that floor above the trigger makes every
// further pass re-plan, retain exactly the same messages, and report
// before == after.
//
// Compacted alone is therefore not a usable commit condition for a pass that
// runs at every turn boundary. Without the reduction check this session would
// commit a checkpoint, emit a compaction event, and spend a summarizer call
// forever, freeing nothing each time.
func TestCompactIfNeededDeclinesAPlanThatFreesNothing(t *testing.T) {
	sess, _, compactions := boundaryCompactionSession(t)
	if _, err := sess.SendUser(context.Background(), "first", io.Discard); err != nil {
		t.Fatal(err)
	}

	// Pin the budget to the committed history's own cost, putting it at 100%
	// of budget - well above the 80% trigger - and hold it there.
	if err := sess.SetPromptBudget(sess.ContextUsage().UsedTokens); err != nil {
		t.Fatalf("SetPromptBudget: %v", err)
	}
	if pct := sess.ContextUsage().Percent; pct < 80 {
		t.Fatalf("fixture left the session at %d%%, want it above the 80%% trigger", pct)
	}

	// The first pass has real work to do: the turn's assistant reply is
	// droppable, so it reduces and commits.
	compacted, err := sess.CompactIfNeeded(context.Background())
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if !compacted {
		t.Fatalf("first pass did not compact a session at %d%% of budget", sess.ContextUsage().Percent)
	}
	// Everything droppable is now gone. Re-pin the budget to what remains, so
	// the MANDATORY floor itself sits above the trigger: the planner runs
	// again and can only retain all of it.
	if err := sess.SetPromptBudget(sess.ContextUsage().UsedTokens); err != nil {
		t.Fatalf("SetPromptBudget after the first pass: %v", err)
	}
	if pct := sess.ContextUsage().Percent; pct < 80 {
		t.Fatalf("history settled at %d%%, want it above the trigger for the no-progress case", pct)
	}
	settled := len(*compactions)

	compacted, err = sess.CompactIfNeeded(context.Background())
	if err != nil {
		t.Fatalf("second pass on a no-progress plan returned an error: %v", err)
	}
	if compacted {
		t.Errorf("committed a compaction that frees nothing")
	}
	if len(*compactions) != settled {
		t.Errorf("emitted %d compaction events for a plan that frees nothing, want 0", len(*compactions)-settled)
	}
}
