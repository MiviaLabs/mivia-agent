package uiadapter_test

// A SaveHandle's contract is a terminal Saved or Failed before close, and
// SaveFailed is the ONLY failure surface the settings screens have - every one
// of them branches solely on it and clears its notice on success.
//
// Eleven apply paths discarded the persist error with `_ = config.Update...`
// and then returned nil, so the handle emitted SaveSaved for a write that
// never landed. The user is told "Saved", the row shows the new value, and the
// setting reverts at next launch with no warning. For the approval default
// that is a security control which appears to have been tightened and was not.
//
// The write really can fail: updateConfigFile reads the TOML (fails on a
// hand-edited file), then MkdirAll + temp file + rename (fails on a read-only
// directory, a full disk, or a config on another mount).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// unwritableConfigPath returns a config path whose PARENT is a regular file,
// so every write through it fails at MkdirAll. Nothing in the test depends on
// permissions, which makes it behave the same for root and in CI.
func unwritableConfigPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "mivia.toml")
}

func settingsStoreWithUnwritableConfig(t *testing.T) *uiadapter.SettingsStore {
	t.Helper()
	res := &config.Resolved{Model: "test-model", ConfigPath: unwritableConfigPath(t)}
	sess := chat.NewSession(res, nil)
	return uiadapter.NewSettingsStore(sess, res, nil)
}

// awaitTerminal drains a SaveHandle to its terminal event.
func awaitTerminal(t *testing.T, handle ports.SaveHandle) ports.SaveEvent {
	t.Helper()
	var last ports.SaveEvent
	for event := range handle.Events() {
		last = event
	}
	return last
}

// The approval default is a security control. Reporting it saved when the
// write failed is the worst case in this family.
func TestSettingsApprovalDefaultSurfacesPersistFailure(t *testing.T) {
	store := settingsStoreWithUnwritableConfig(t)
	handle, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetApprovalDefault{Mode: "always-ask"})
	if err != nil {
		return // a synchronous refusal is also an honest failure
	}

	event := awaitTerminal(t, handle)
	if event.State != ports.SaveFailed {
		t.Fatalf("terminal state = %v, want SaveFailed: the setting did not persist and the user was told it did", event.State)
	}
}

// The same contract for the rest of the general section.
func TestSettingsGeneralEditSurfacesPersistFailure(t *testing.T) {
	store := settingsStoreWithUnwritableConfig(t)
	handle, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetTheme{Name: "dark"})
	if err != nil {
		return
	}

	event := awaitTerminal(t, handle)
	if event.State != ports.SaveFailed {
		t.Fatalf("terminal state = %v, want SaveFailed", event.State)
	}
}

// Removing an MCP server removes an untrusted tool source. A remove that
// silently did not persist reloads the server at next launch.
func TestSettingsRemoveMCPServerSurfacesPersistFailure(t *testing.T) {
	store := settingsStoreWithUnwritableConfig(t)
	handle, err := store.Settings().MCP.Apply(context.Background(), ports.ScopeUser, ports.RemoveMCPServer{ID: "some-server"})
	if err != nil {
		return
	}

	event := awaitTerminal(t, handle)
	if event.State != ports.SaveFailed {
		t.Fatalf("terminal state = %v, want SaveFailed", event.State)
	}
}

// A provider the user believes they deleted must not come back.
func TestSettingsRemoveProviderSurfacesPersistFailure(t *testing.T) {
	store := settingsStoreWithUnwritableConfig(t)
	handle, err := store.Settings().Providers.Apply(context.Background(), ports.ScopeUser, ports.RemoveProvider{Name: "openrouter"})
	if err != nil {
		return
	}

	event := awaitTerminal(t, handle)
	if event.State != ports.SaveFailed {
		t.Fatalf("terminal state = %v, want SaveFailed", event.State)
	}
}
