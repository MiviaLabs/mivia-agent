package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestCLISyncOptionsCarriesStreamAssistant pins that sync.stream_assistant
// reaches the projector on the plain-CLI surface too. Wiring one surface and
// not the other is how a config key becomes surface-dependent with no error.
func TestCLISyncOptionsCarriesStreamAssistant(t *testing.T) {
	sess := &chat.Session{SessionID: "s1", SessionDir: t.TempDir()}
	for _, want := range []bool{false, true} {
		res := &config.Resolved{Sync: config.SyncConfig{StreamAssistant: want}}
		got := cliSyncOptions(sess, res, nil).ProjectorOptions.StreamAssistant
		if got != want {
			t.Errorf("StreamAssistant = %v, want %v", got, want)
		}
	}
}
