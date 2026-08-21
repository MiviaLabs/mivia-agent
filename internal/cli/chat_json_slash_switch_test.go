package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// withActiveJSONSlashSink activates the --json sink for the duration of a
// test, mirroring what replLineMode does for a real `mivia chat --json`
// process, and restores whatever was active before (nil in every current
// caller, but this stays correct if that ever changes).
func withActiveJSONSlashSink(t *testing.T) (*bytes.Buffer, *JSONSlashSink) {
	t.Helper()
	prev := activeJSONSlashSink
	var buf bytes.Buffer
	sink := &JSONSlashSink{w: &buf}
	activeJSONSlashSink = sink
	t.Cleanup(func() { activeJSONSlashSink = prev })
	return &buf, sink
}

// decodeNDJSONEvents decodes every non-empty line in buf as an ndjsonEvent.
func decodeNDJSONEvents(t *testing.T, buf *bytes.Buffer) []ndjsonEvent {
	t.Helper()
	var events []ndjsonEvent
	for _, line := range splitNonEmptyLines(buf.String()) {
		var ev ndjsonEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid ndjsonEvent: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// TestModelSlashOverJSONEmitsModelChangedOnSuccess pins the wire contract a
// GUI switcher relies on: a successful /model switch, driven through
// handleSlash exactly as replLineMode would with activeJSONSlashSink set,
// emits slash_info (prose) followed by a typed model_changed event.
func TestModelSlashOverJSONEmitsModelChangedOnSuccess(t *testing.T) {
	buf, _ := withActiveJSONSlashSink(t)
	res := &config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}}
	sess := chat.NewSession(res, nil)

	if _, _, err := handleSlash("/model B", sess, res, false, nil); err != nil {
		t.Fatal(err)
	}

	events := decodeNDJSONEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (slash_info, model_changed): %+v", len(events), events)
	}
	if events[0].Type != "slash_info" {
		t.Fatalf("first event = %+v, want slash_info", events[0])
	}
	last := events[len(events)-1]
	if last.Type != "model_changed" || last.Model != "B" || last.Provider != "p" {
		t.Fatalf("model_changed event = %+v", last)
	}
	if got := sess.CurrentModel(); got != "B" {
		t.Fatalf("session model = %q, want B", got)
	}
}

// TestModelSlashOverJSONEmitsSlashErrorOnRejection pins the failure-side
// discriminator chat_json_writer.go's doc comment promises: an unavailable
// model must surface as slash_error (not slash_info), and no model_changed
// event, so a --json caller doesn't need to string-match prose to detect a
// rejected switch.
func TestModelSlashOverJSONEmitsSlashErrorOnRejection(t *testing.T) {
	buf, _ := withActiveJSONSlashSink(t)
	res := &config.Resolved{ProviderName: "p", Model: "A", Models: []string{"A", "B"}}
	sess := chat.NewSession(res, nil)

	if _, _, err := handleSlash("/model Z", sess, res, false, nil); err != nil {
		t.Fatal(err)
	}

	events := decodeNDJSONEvents(t, buf)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (slash_error only): %+v", len(events), events)
	}
	if events[0].Type != "slash_error" {
		t.Fatalf("event = %+v, want slash_error", events[0])
	}
	if got := sess.CurrentModel(); got != "A" {
		t.Fatalf("rejected switch changed the session model to %q", got)
	}
}

// TestEffortSlashOverJSONEmitsEffortChangedOnSuccess mirrors the /model test
// above for /effort.
func TestEffortSlashOverJSONEmitsEffortChangedOnSuccess(t *testing.T) {
	buf, _ := withActiveJSONSlashSink(t)
	res := effortCatalogConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})

	if _, _, err := handleSlash("/effort low", sess, res, false, nil); err != nil {
		t.Fatal(err)
	}

	events := decodeNDJSONEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (slash_info, effort_changed): %+v", len(events), events)
	}
	last := events[len(events)-1]
	if last.Type != "effort_changed" || last.Effort != "low" {
		t.Fatalf("effort_changed event = %+v", last)
	}
}

// TestEffortSlashOverJSONEmitsSlashErrorOnRejection mirrors the /model
// rejection test above for /effort: an unparseable level must surface as
// slash_error, not slash_info.
func TestEffortSlashOverJSONEmitsSlashErrorOnRejection(t *testing.T) {
	buf, _ := withActiveJSONSlashSink(t)
	res := effortCatalogConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})

	if _, _, err := handleSlash("/effort turbo", sess, res, false, nil); err != nil {
		t.Fatal(err)
	}

	events := decodeNDJSONEvents(t, buf)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (slash_error only): %+v", len(events), events)
	}
	if events[0].Type != "slash_error" {
		t.Fatalf("event = %+v, want slash_error", events[0])
	}
}
