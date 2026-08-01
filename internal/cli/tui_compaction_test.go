package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestRenderCompactionNoticeOmitsContent(t *testing.T) {
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: "session", Sequence: 1},
		End:   contextstate.SourceID{SessionID: "session", Sequence: 3},
	}
	event, err := events.NewCompactionEvent("threshold", 80, 40, rangeValue, 1)
	if err != nil {
		t.Fatal(err)
	}
	notice := renderCompactionNotice(event)
	if !strings.Contains(notice, "80 -> 40") || strings.Contains(notice, "summary") {
		t.Fatalf("notice = %q", notice)
	}
}
