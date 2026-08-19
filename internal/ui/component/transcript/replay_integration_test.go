package transcript_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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
//
// Committed content leaves the model via CommitMsg (see the package doc
// comment), so this collects every CommitMsg a real caller would print,
// rather than reading them back off View().
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
	var printed []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-handle.Events():
			if !ok {
				got := strings.Join(printed, "\n")
				if got == "" {
					t.Fatal("expected a non-empty printed transcript after a full replay")
				}
				t.Logf("printed transcript:\n%s", got)
				return
			}
			var cmd tea.Cmd
			m, cmd = m.HandleEvent(ev)
			if cmd == nil {
				continue
			}
			if msg, ok := cmd().(transcript.CommitMsg); ok {
				printed = append(printed, msg.Text)
			}
		case <-deadline:
			t.Fatal("replay did not close its event channel within 5s")
		}
	}
}
