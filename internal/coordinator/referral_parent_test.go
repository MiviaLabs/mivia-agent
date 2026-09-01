package coordinator

import (
	"testing"
)

// TestAskerTaskIDNamesTheTaskThatAsked proves the registry can report the
// relationship a referral needs.
//
// A referral is started BECAUSE a task asked, so the asking task is its
// parent. Nothing else records that: the referral's own request carries the
// role it targets, not the task that wanted it.
func TestAskerTaskIDNamesTheTaskThatAsked(t *testing.T) {
	c := &coordinator{asks: newAskRegistry()}
	c.RegisterAsk("run-1", "task-asker", "planner", "ask-1", nil)

	if got := c.askerTaskID("ask-1"); got != "task-asker" {
		t.Errorf("askerTaskID = %q, want task-asker", got)
	}
}

// TestAskerTaskIDIsEmptyForAnUnknownAsk proves the lookup degrades to "no
// parent" rather than to a wrong one. An unknown ask must read as a top-level
// run, which is what a consumer already handles.
func TestAskerTaskIDIsEmptyForAnUnknownAsk(t *testing.T) {
	c := &coordinator{asks: newAskRegistry()}

	if got := c.askerTaskID("never-registered"); got != "" {
		t.Errorf("askerTaskID = %q for an unknown ask, want empty", got)
	}
}

// TestAskerTaskIDIsEmptyAfterTheSlotIsReleased pins the same degradation for
// an ask whose owner was purged at a retry boundary. Reporting a stale parent
// would attach a live run to a task that is no longer running it.
func TestAskerTaskIDIsEmptyAfterTheSlotIsReleased(t *testing.T) {
	c := &coordinator{asks: newAskRegistry()}
	c.RegisterAsk("run-1", "task-asker", "planner", "ask-1", nil)

	c.asks.mu.Lock()
	c.asks.releaseAskSlotLocked("ask-1")
	c.asks.mu.Unlock()

	if got := c.askerTaskID("ask-1"); got != "" {
		t.Errorf("askerTaskID = %q after release, want empty", got)
	}
}
