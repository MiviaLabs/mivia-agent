package transcript_test

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// TestReplayDrivesTranscript is the "components against a replay fake"
// proof: it goes through the real ports.Conversation/TurnHandle contract
// (not a direct HandleEvent call) - Send, then range over the channel
// TurnHandle.Events() returns - exactly the shape a future Screen's
// tea.Cmd will read from. External test package (transcript_test) so it
// only sees transcript's public API, same as any real caller would.
func TestReplayDrivesTranscript(t *testing.T) {
	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var th theme.Theme
	for _, c := range themes {
		if c.Name == "mivia-dark" {
			th = c
		}
	}

	var conv ports.Conversation = replay.New(events, 0)
	handle, err := conv.Send(context.Background(), intent.Send{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	m := transcript.New(th, theme.TierTrueColor)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-handle.Events():
			if !ok {
				got := m.View()
				if got == "" {
					t.Fatal("expected a non-empty rendered transcript after a full replay")
				}
				t.Logf("rendered transcript:\n%s", got)
				return
			}
			m, _ = m.HandleEvent(ev)
		case <-deadline:
			t.Fatal("replay did not close its event channel within 5s")
		}
	}
}
