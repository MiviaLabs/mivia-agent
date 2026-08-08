package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// The referral tracker exists so executeResumedRun's Join (and the run claim
// release that follows it) never races a still-running referral task. The
// original tracker only waited for referrals already registered, so a
// referral spawned after waitReferrals observed zero in-flight referrals
// escaped the join: h.done closed - and the claim was released - while the
// referral goroutine still mutated ledger state and had not closed its ask.
// TestReferralFailClosesAsk hit exactly this window. These tests pin the
// settled-window fix: once waitReferrals settles, referralAdd refuses the
// asynchronous path and SpawnReferral runs the referral inline.

// TestReferralTrackerSettlesAfterWait: waitReferrals closes the referral
// window, and every later referralAdd must refuse the async path.
func TestReferralTrackerSettlesAfterWait(t *testing.T) {
	h := &RunHandle{}
	h.waitReferrals()
	if h.referralAdd() {
		t.Fatal("referralAdd after settle must refuse the async path")
	}
	h.referralDone()
	if h.referralAdd() {
		t.Fatal("referralAdd after settle+done must refuse the async path")
	}
	// Settling twice is idempotent.
	h.waitReferrals()
	if h.referralAdd() {
		t.Fatal("referralAdd after second settle must refuse the async path")
	}
}

// TestSpawnReferralAfterRunDoneCompletesInline: a referral spawned against a
// run whose DAG already finished (Join returned, the referral window settled)
// must complete its terminal side effects - including CloseAsk for a failing
// handler - before SpawnReferral returns. Before the fix the referral ran on
// an escaping goroutine, so the ask could still be open when the spawner
// returned and Join had already been satisfied.
func TestSpawnReferralAfterRunDoneCompletesInline(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "aud", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return nil, fmt.Errorf("boom")
	}))
	_ = d.Register(runtime.Subagent, "p", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}))
	c := New(repo, subagents.New(d, subagents.Policy{Workers: 2}))
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "p1", Name: "p", AgentName: "p", Timeout: 3 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Fully join the parent run first: executeResumedRun's waitReferrals has
	// settled the window, so any referral spawned now must run inline.
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	ask, _ := agentmsg.NewMessage(h.RunID(), agentmsg.KindAsk,
		agentmsg.Party{TaskID: "p1", Role: "rev"}, agentmsg.Party{Role: "aud"},
		"body", nil, agentmsg.Options{})
	c.RegisterAsk(h.RunID(), "p1", "rev", ask.ID, nil)
	if _, err := c.SpawnReferralFromAsk(context.Background(), h.RunID(), "aud", ask); err != nil {
		t.Fatal(err)
	}
	if !c.IsAskAnswered(ask.ID) {
		t.Fatal("referral spawned after the run finished must close its ask before SpawnReferral returns")
	}
}
