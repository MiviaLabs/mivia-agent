package stream

import (
	"bytes"
	_ "embed"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

//go:embed testdata/conversation.json
var fixtureJSON []byte

// DefaultFixture returns the recorded-conversation fixture from
// wireframes-panes.md section 4, embedded so demo commands and tests work
// with no filesystem dependency and no cwd assumption.
func DefaultFixture() ([]uievent.Event, error) {
	return uievent.LoadFixture(bytes.NewReader(fixtureJSON))
}
