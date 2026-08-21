package legacytui

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// The picker footer and a session refusal describe the same state, so they must
// use the same words rather than two vocabularies for "wait".
func TestEffortBusyRefusalMatchesThePickerWording(t *testing.T) {
	// The session's verbatim wording, which reaches both surfaces unchanged.
	notice := cli.SafeEffortError(errors.New("reasoning effort cannot change while work is active"))
	d := newEffortDialog("m", []reasoning.Level{reasoning.High}, reasoning.High, reasoning.High, true)
	if !strings.Contains(cli.StripANSI(d.footer()), notice) {
		t.Fatalf("session refusal %q does not match the busy footer %q", notice, cli.StripANSI(d.footer()))
	}
}
