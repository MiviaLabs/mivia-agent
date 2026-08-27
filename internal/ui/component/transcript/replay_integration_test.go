package transcript_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

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
	m.SetSize(80, 24)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-handle.Events():
			if !ok {
				got := m.Dump()
				if strings.TrimSpace(got) == "" {
					t.Fatal("expected a non-empty transcript after a full replay")
				}
				// Non-empty proves construction. These prove the events
				// crossed the boundary and were rendered as themselves:
				// one landmark per event family in the fixture.
				plain := ansi.Strip(got)
				for _, want := range []string{
					"Add retry with exponential backoff", // turn.start, the user input
					"reasoning",                          // reasoning summary
					"read_file",                          // tool lifecycle
					"edit",
					"s3_uploader",       // a tool detail
					"plan",              // plan block
					"context 62%",       // notice
					"transport refused", // error
					"1284 in",           // usage
				} {
					if !strings.Contains(plain, want) {
						t.Errorf("replayed transcript is missing %q:\n%s", want, plain)
					}
				}
				// And it stayed inside the terminal it was given.
				for _, line := range strings.Split(got, "\n") {
					if w := ansi.StringWidth(line); w > 80 {
						t.Errorf("row is %d columns, wider than the 80-column terminal: %q", w, line)
					}
				}
				return
			}
			m, _ = m.HandleEvent(ev)
		case <-deadline:
			t.Fatal("replay did not close its event channel within 5s")
		}
	}
}
