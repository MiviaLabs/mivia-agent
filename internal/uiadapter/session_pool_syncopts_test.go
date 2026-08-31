package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestPoolSyncOptionsCarriesStreamAssistant pins that sync.stream_assistant
// reaches the projector. ProjectorOptions.StreamAssistant was set nowhere in
// production, which is the dead-option class: the key parses, normalizes and
// documents, and the runtime does the default thing with no error.
func TestPoolSyncOptionsCarriesStreamAssistant(t *testing.T) {
	sess := &chat.Session{SessionID: "s1", SessionDir: t.TempDir()}
	for _, want := range []bool{false, true} {
		res := &config.Resolved{Sync: config.SyncConfig{StreamAssistant: want}}
		got := poolSyncOptions(sess, "s1", res, nil).ProjectorOptions.StreamAssistant
		if got != want {
			t.Errorf("StreamAssistant = %v, want %v", got, want)
		}
	}
}
