package hub

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestRelayedKindsReturnsACopy proves a caller cannot reach into the package's
// own list. A renderer's test drives this to walk every relayed kind, and a
// test that could mutate the relay's configuration would change the behaviour
// of whatever ran after it.
func TestRelayedKindsReturnsACopy(t *testing.T) {
	first := RelayedKinds()
	if len(first) == 0 {
		t.Fatal("RelayedKinds is empty; nothing is relayed")
	}
	original := first[0]

	first[0] = events.Kind("mutated")

	if second := RelayedKinds(); second[0] != original {
		t.Fatalf("mutating the result changed the package list: %v", second[0])
	}
}

// TestRelayedKindsCoversTheSubagentLifecycle pins the kinds a remote viewer
// needs to see a subagent at all. Each was, at some point, published to the
// bus and relayed while no renderer had an arm for it.
func TestRelayedKindsCoversTheSubagentLifecycle(t *testing.T) {
	got := map[events.Kind]bool{}
	for _, k := range RelayedKinds() {
		got[k] = true
	}
	for _, want := range []events.Kind{
		events.KindSubagentBegin,
		events.KindSubagentStart,
		events.KindSubagentEnd,
		events.KindSubagentHeartbeat,
		events.KindSubagentDone,
	} {
		if !got[want] {
			t.Errorf("%s is not relayed, so a second surface cannot see it", want)
		}
	}
}
