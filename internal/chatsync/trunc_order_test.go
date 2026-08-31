package chatsync

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// applyTruncation mutates *env, so calling it from inside a composite literal
// that ALSO copies `Envelope: env` reads an unsequenced operand: the Go spec
// does not order a non-call operand against a function call in the same
// literal. When the copy wins, Trunc is nil and an amputated field renders as
// COMPLETE, which the event contract (section 5) calls a product failure -
// "trunc is present only when something was cut... there is no third reading".
//
// HONESTY NOTE: this test cannot be made to fail on the current toolchain.
// gc happens to evaluate the call first, so it was green before the hoist as
// well as after. Its value is as a structural pin on the fixed shape; the
// gate that can actually reject the hazard is the Semgrep rule
// mivia.go.no-truncation-call-inside-envelope-literal.
func TestTruncationReachesEnvelopeCopy(t *testing.T) {
	longDetail := strings.Repeat("d", BudgetShortField+50)

	cases := []struct {
		name  string
		ev    events.Event
		field string
		trunc func(WireEvent) *Truncation
	}{
		{
			name: "tool.ended detail",
			ev: events.Event{
				Kind: events.KindToolEnd, SessionID: "sess-1", TurnID: "turn:1",
				ToolCallID: "call_1", Name: "cat", Detail: longDetail,
			},
			field: "detail",
			trunc: func(w WireEvent) *Truncation { return w.Payload.(*ToolEndedPayload).Trunc },
		},
		{
			name: "subagent.tool.ended detail",
			ev: events.Event{
				Kind: events.KindSubagentEnd, SessionID: "sess-1", TurnID: "turn:1",
				ToolCallID: "call_2", Name: "task", Detail: longDetail,
			},
			field: "detail",
			trunc: func(w WireEvent) *Truncation { return w.Payload.(*SubagentToolEndedPayload).Trunc },
		},
		{
			name: "subagent.progress detail",
			ev: events.Event{
				Kind: events.KindSubagentHeartbeat, SessionID: "sess-1", TurnID: "turn:1",
				ToolCallID: "call_3", Detail: longDetail,
			},
			field: "detail",
			trunc: func(w WireEvent) *Truncation { return w.Payload.(*SubagentProgressPayload).Trunc },
		},
		{
			name: "context.compacted message",
			ev: events.Event{
				Kind: events.KindCompaction, SessionID: "sess-1", TurnID: "turn:1",
				Detail: longDetail,
			},
			field: "message",
			trunc: func(w WireEvent) *Truncation { return w.Payload.(*ContextCompactedPayload).Trunc },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, ProjectorOptions{})
			ev := tc.ev
			ev.Timestamp = time.Now()

			got := p.Project(ev)
			if len(got) != 1 {
				t.Fatalf("produced %d events, want 1", len(got))
			}
			trunc := tc.trunc(got[0])
			if trunc == nil {
				t.Fatalf("trunc is nil on the wire payload; the %q field was cut to "+
					"%d bytes but the envelope copy did not carry the record, so an "+
					"amputated field renders as complete", tc.field, BudgetShortField)
			}
			f, ok := trunc.Fields[tc.field]
			if !ok {
				t.Fatalf("trunc.fields has no %q entry, got %+v", tc.field, trunc.Fields)
			}
			if f.Kept != BudgetShortField || f.Total != len(longDetail) {
				t.Errorf("trunc.fields[%q] = {kept:%d total:%d}, want {kept:%d total:%d}",
					tc.field, f.Kept, f.Total, BudgetShortField, len(longDetail))
			}
		})
	}
}
