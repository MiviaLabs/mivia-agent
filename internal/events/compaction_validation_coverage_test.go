package events

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestCompactionEventValidateRejectsInvalidFields(t *testing.T) {
	valid := CompactionEvent{
		Trigger: "threshold", BeforeTokens: 10, AfterTokens: 5,
		SourceRange: compactionTestRange(t), SummaryVersion: 1, sealed: true,
	}
	cases := []struct {
		name   string
		mutate func(*CompactionEvent)
	}{
		{name: "missing constructor seal", mutate: func(e *CompactionEvent) { e.sealed = false }},
		{name: "blank trigger", mutate: func(e *CompactionEvent) { e.Trigger = "  " }},
		{name: "overlong trigger", mutate: func(e *CompactionEvent) { e.Trigger = strings.Repeat("x", 257) }},
		{name: "control character trigger", mutate: func(e *CompactionEvent) { e.Trigger = "threshold\n" }},
		{name: "negative before tokens", mutate: func(e *CompactionEvent) { e.BeforeTokens = -1 }},
		{name: "negative after tokens", mutate: func(e *CompactionEvent) { e.AfterTokens = -1 }},
		{name: "after exceeds before", mutate: func(e *CompactionEvent) { e.AfterTokens = e.BeforeTokens + 1 }},
		{name: "negative elided messages", mutate: func(e *CompactionEvent) { e.ElidedMessages = -1 }},
		{name: "negative elided bytes", mutate: func(e *CompactionEvent) { e.ElidedBytes = -1 }},
		{name: "invalid source range", mutate: func(e *CompactionEvent) { e.SourceRange = contextstate.SourceRange{} }},
		{name: "missing summary version", mutate: func(e *CompactionEvent) { e.SummaryVersion = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := valid
			tc.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("invalid CompactionEvent was accepted")
			}
		})
	}
}
