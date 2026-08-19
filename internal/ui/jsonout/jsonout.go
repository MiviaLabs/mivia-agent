// Package jsonout is the --output json renderer: newline-delimited JSON,
// one uievent.Event per line, in the same wire form testdata/ fixtures
// use. It performs no interpretation of Body — that is the point: a
// scriptable consumer gets the raw typed event stream.
package jsonout

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Render writes one JSON line per event to w.
func Render(w io.Writer, events []uievent.Event) error {
	enc := json.NewEncoder(w)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return fmt.Errorf("jsonout: encode %s: %w", ev.Kind, err)
		}
	}
	return nil
}
