package chatsync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// StoredPayloadBound is the receiving column's bound:
// octet_length(payload::text) <= 65536, and the API's DTO measures the same
// escaped form. It is restated here because the producer has to hold itself to
// it - the enforcement is a whole batch away, and a rejection there fails every
// event in the batch, not the one that broke the rule.
const StoredPayloadBound = 65536

// worstCaseText is the content class that breaks a raw-byte budget: a tool
// reading a binary file, where most bytes are control bytes and each one
// escapes to six bytes as a \\u00XX sequence. A budget measured raw admits
// six times its own size of this.
func worstCaseText(runes int) string {
	return strings.Repeat(string(rune(7)), runes)
}

// TestNoEventExceedsTheStoredPayloadBound is the property, driven through the
// real projector rather than through the budgets in isolation.
//
// The budgets were set in RAW bytes while both enforcement points measure the
// ESCAPED form, so a 16 KiB tool output could be stored as 96 KiB - over the
// column bound. Postgres then rejects the INSERT, and because the API sends a
// batch as one multi-row statement, it rejects up to a hundred events with it.
func TestNoEventExceedsTheStoredPayloadBound(t *testing.T) {
	cases := []struct {
		name string
		opts ProjectorOptions
		ev   events.Event
	}{
		{"tool output of control bytes", toolIOOpts(), events.Event{
			Kind: events.KindToolEnd, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Output: worstCaseText(64 * 1024),
		}},
		{"tool input of control bytes", toolIOOpts(), events.Event{
			Kind: events.KindToolStart, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Input: worstCaseText(64 * 1024),
		}},
		{"an answer of control bytes", proseOpts(), events.Event{
			Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "turn:1",
			Content: worstCaseText(64 * 1024),
		}},
		{"a delta of control bytes", proseOpts(), events.Event{
			Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "turn:1",
			Content: worstCaseText(64 * 1024), Detail: "delta",
		}},
		{"reasoning of control bytes", proseOpts(), events.Event{
			Kind: events.KindThinking, SessionID: "sess-1", TurnID: "turn:1",
			Content: worstCaseText(64 * 1024),
		}},
		{"a subagent answer of control bytes", proseOpts(), events.Event{
			Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "turn:1",
			Content: worstCaseText(64 * 1024),
		}.WithAgentAttribution("task-1", "builder", 1)},
		// The status field derives from the detail. Built from the raw event it
		// carried no budget at all, so a long detail produced a payload six
		// times over the column bound.
		{"a tool end detail of control bytes", toolIOOpts(), events.Event{
			Kind: events.KindToolEnd, SessionID: "sess-1", TurnID: "turn:1",
			ToolCallID: "c1", Name: "read_file", Detail: worstCaseText(64 * 1024),
		}},
		{"a prompt of control bytes", proseOpts(), events.Event{
			Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1",
			Detail: worstCaseText(64 * 1024),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, tc.opts)
			if tc.ev.Kind != events.KindTurnStart {
				p.Project(events.Event{
					Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1",
					Detail: "the prompt",
				})
			}

			got := p.Project(tc.ev)
			if len(got) == 0 {
				t.Fatalf("%s produced no wire event; this case proves nothing", tc.name)
			}
			for _, we := range got {
				data, err := json.Marshal(we.Payload)
				if err != nil {
					t.Fatalf("marshal %s: %v", we.Type, err)
				}
				if len(data) > StoredPayloadBound {
					t.Errorf("%s stores %d bytes, over the %d-byte column bound. The "+
						"insert is rejected, and it takes every other event of its "+
						"batch with it.", we.Type, len(data), StoredPayloadBound)
				}
			}
		})
	}
}

// TestABudgetIsMeasuredInStoredBytes states the unit directly, so the reason
// the bound above holds is pinned and not incidental.
func TestABudgetIsMeasuredInStoredBytes(t *testing.T) {
	env := Envelope{}
	kept := applyTruncation(&env, "output", worstCaseText(64*1024), BudgetToolOutput)

	encoded, err := json.Marshal(kept)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Minus the two quotes json.Marshal adds around the string.
	if stored := len(encoded) - 2; stored > BudgetToolOutput {
		t.Errorf("a field inside its %d-byte budget stores %d bytes; the budget is "+
			"being measured in a unit the store does not use",
			BudgetToolOutput, stored)
	}
	if env.Trunc == nil {
		t.Fatal("a field far over its budget was not recorded as truncated")
	}
	if f := env.Trunc.Fields["output"]; f.Kept > f.Total {
		t.Errorf("the truncation record says %d kept of %d total", f.Kept, f.Total)
	}
}
