package contextmgr

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// preservedFrameName is the sentinel provider.Message.Name a session-owned
// frame carries (the chat layer passes it through PlanInput.PreserveNames).
const preservedFrameName = "core-memory-context"

// preservedFrameFixture builds a history in which a named user-role frame sits
// at index 1, immediately after the system message and ahead of several
// conversation turns. A forced compaction must drop most of that conversation,
// so the frame survives only when the planner keeps it on purpose.
func preservedFrameFixture() []provider.Message {
	frame := provider.Message{
		Role:    provider.RoleUser,
		Content: "<core-memory-context>\nremember the promoted fact\n</core-memory-context>",
		Name:    preservedFrameName,
	}
	messages := []provider.Message{{Role: provider.RoleSystem, Content: "system"}}
	messages = append(messages, frame)
	for turn := 0; turn < 6; turn++ {
		messages = append(messages,
			provider.Message{Role: provider.RoleUser, Content: "objective"},
			provider.Message{Role: provider.RoleAssistant, Content: "answer"},
		)
	}
	return messages
}

// TestPlanPreservesNamedMessagesThroughCompaction pins the BUG 3 planner
// seam: mandatoryIndexes and retainMessages keep every message whose
// provider.Message.Name appears in PlanInput.PreserveNames, so a
// session-owned frame at index 1 survives a forced compaction in the retained
// messages and in the checkpoint candidate's active context bytes. The
// control case (no PreserveNames) proves the seam is what preserves it: the
// same message is then an ordinary early user message and IS dropped, which
// planner_retention_test.go pins for unnamed messages too.
func TestPlanPreservesNamedMessagesThroughCompaction(t *testing.T) {
	messages := preservedFrameFixture()
	if kept := countPlannerNamed(messages, preservedFrameName); kept != 1 {
		t.Fatalf("fixture frame count = %d, want 1", kept)
	}

	plan, err := Plan(PlanInput{
		Messages: messages, Budget: 1000, Force: true,
		PreserveNames: []string{preservedFrameName},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatal("forced plan was not compacted")
	}
	if kept := countPlannerNamed(plan.Messages, preservedFrameName); kept != 1 {
		t.Fatalf("retained messages carry the frame %d times, want exactly 1", kept)
	}
	// The durable checkpoint candidate must carry the frame too: those bytes
	// are what a resume restores.
	active := decodePlannerActiveContext(t, plan.Candidate.ActiveContext)
	if kept := countPlannerNamed(active, preservedFrameName); kept != 1 {
		t.Fatalf("candidate active context carries the frame %d times, want exactly 1", kept)
	}

	// Control: without PreserveNames the same early user-role message is
	// dropped by the recent-tail retention, exactly like an unnamed one.
	control, err := Plan(PlanInput{Messages: messages, Budget: 1000, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if kept := countPlannerNamed(control.Messages, preservedFrameName); kept != 0 {
		t.Fatalf("control retained the frame %d times, want 0", kept)
	}
}

func countPlannerNamed(messages []provider.Message, name string) int {
	count := 0
	for _, message := range messages {
		if message.Name == name {
			count++
		}
	}
	return count
}

func decodePlannerActiveContext(t *testing.T, body []byte) []provider.Message {
	t.Helper()
	if len(body) == 0 {
		return nil
	}
	var messages []provider.Message
	if err := contextstate.UnmarshalCanonical(body, &messages); err != nil {
		t.Fatalf("decode active context: %v", err)
	}
	return messages
}
