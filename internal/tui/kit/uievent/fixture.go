package uievent

import (
	"encoding/json"
	"fmt"
	"io"
)

// LoadFixture decodes a JSON array of Events, in the wire form Event's own
// MarshalJSON/UnmarshalJSON produce. Used by the plain and JSON renderers'
// --demo replay, and by tests, to load a recorded conversation from
// testdata/ without either renderer needing its own decoder.
func LoadFixture(r io.Reader) ([]Event, error) {
	var events []Event
	if err := json.NewDecoder(r).Decode(&events); err != nil {
		return nil, fmt.Errorf("uievent: decode fixture: %w", err)
	}
	return events, nil
}
