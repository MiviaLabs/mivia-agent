package clichat

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestRenderExternalSubagentEvent_UnmappedKindAndEmptyContentAreNoOps pins
// renderExternalSubagentEvent's two early returns: an event.Kind with no
// mapped line type, and an assistant/thinking event with empty content.
func TestRenderExternalSubagentEvent_UnmappedKindAndEmptyContentAreNoOps(t *testing.T) {
	var buf bytes.Buffer
	renderExternalSubagentEvent(&buf, events.Event{Kind: events.KindError})
	if buf.Len() != 0 {
		t.Fatalf("unmapped kind wrote %q, want nothing", buf.String())
	}
	renderExternalSubagentEvent(&buf, events.Event{Kind: events.KindAssistant, Content: ""})
	if buf.Len() != 0 {
		t.Fatalf("empty-content assistant wrote %q, want nothing", buf.String())
	}
}

// TestRenderExternalTurnEvent_EmptyAssistantContentIsANoOp pins the
// break-not-write branch for an empty-content assistant delta.
func TestRenderExternalTurnEvent_EmptyAssistantContentIsANoOp(t *testing.T) {
	var buf bytes.Buffer
	r := &externalRun{}
	renderExternalTurnEvent(&buf, r, events.Event{Kind: events.KindAssistant, Content: ""})
	if buf.Len() != 0 {
		t.Fatalf("empty-content assistant turn event wrote %q, want nothing", buf.String())
	}
}

// TestRenderExternalTurnEvent_ErrorWritesExternalError pins the
// events.KindError branch, distinct from the KindTurnEnd case every
// existing chat_hub_tolerance_test.go test drives.
func TestRenderExternalTurnEvent_ErrorWritesExternalError(t *testing.T) {
	var buf bytes.Buffer
	r := &externalRun{}
	renderExternalTurnEvent(&buf, r, events.Event{Kind: events.KindError, TurnID: "run-1", Detail: "boom"})
	if !strings.Contains(buf.String(), "external_error") {
		t.Fatalf("output = %q, want an external_error line", buf.String())
	}
}
