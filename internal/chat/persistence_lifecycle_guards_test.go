package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestListSessions_DeleteSession_CatalogNotConfiguredGuards pins the
// not-configured guard shared by ListSessions, ListAllSessions, and
// DeleteSession: a session with no context store wired must report a clear
// error rather than a nil-pointer panic.
func TestListSessions_DeleteSession_CatalogNotConfiguredGuards(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, &fakeCompleter{out: "ok"})

	if _, err := sess.ListSessions(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ListSessions err = %v, want a not-configured error", err)
	}
	if _, err := sess.ListAllSessions(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ListAllSessions err = %v, want a not-configured error", err)
	}
	if err := sess.DeleteSession("some-session"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("DeleteSession err = %v, want a not-configured error", err)
	}
}
