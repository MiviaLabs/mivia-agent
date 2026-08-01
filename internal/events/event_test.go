package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestCompactionEventIsSealedAndContentFree(t *testing.T) {
	rangeValue := compactionTestRange(t)
	event, err := NewCompactionEvent("threshold", 82, 49, rangeValue, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{"content", "input", "output", "secret-sentinel"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("typed event contains forbidden field/value %q: %s", forbidden, encoded)
		}
	}
	if event.Trigger != "threshold" || event.BeforeTokens != 82 || event.AfterTokens != 49 {
		t.Fatalf("event fields = %+v", event)
	}
}

func compactionTestRange(t *testing.T) contextstate.SourceRange {
	t.Helper()
	id := contextstate.SourceID{SessionID: "event-session", Sequence: 2}
	rangeValue, err := contextstate.NewSourceRange(id, id)
	if err != nil {
		t.Fatal(err)
	}
	return rangeValue
}
