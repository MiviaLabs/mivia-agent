package chatsync

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestProjectAssistant_EmptyContentIsANoop pins projectAssistant's own
// empty-content guard: a non-delta assistant event with no content must
// produce no wire event.
func TestProjectAssistant_EmptyContentIsANoop(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})
	ev := events.Event{
		Kind: events.KindAssistant, SessionID: "sess-1", TurnID: "t1",
		Content: "", Timestamp: time.Now(),
	}
	if we := p.Project(ev); we != nil {
		t.Fatalf("Project() = %v, want nil for an empty-content assistant event", we)
	}
}

// TestProjectThinking_EmptyContentIsANoop mirrors the assistant case for
// projectThinking's own empty-content guard.
func TestProjectThinking_EmptyContentIsANoop(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeThinking: true})
	ev := events.Event{
		Kind: events.KindThinking, SessionID: "sess-1", TurnID: "t1",
		Content: "", Timestamp: time.Now(),
	}
	if we := p.Project(ev); we != nil {
		t.Fatalf("Project() = %v, want nil for an empty-content thinking event", we)
	}
}
