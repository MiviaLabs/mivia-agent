package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// setTestReasoningRedactionPolicy installs a minimal policy matching
// secret-<digits> and restores whatever policy was active before, so a test
// never leaves a process-wide policy installed for its neighbors. The policy
// is a process-wide global: no test in this package may call t.Parallel while
// depending on it.
func setTestReasoningRedactionPolicy(t *testing.T, patterns ...string) {
	t.Helper()
	old := redact.Current()
	policy, err := redact.Compile(patterns, nil, "[redacted]")
	if err != nil {
		t.Fatalf("compile redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(old) })
}

// reasoningSecretMessages is the shared fixture: one assistant turn whose
// visible Content must never change while its ReasoningContent must pass
// through the workspace redaction policy.
func reasoningSecretMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleAssistant, Content: "keep", ReasoningContent: "secret-1234"},
	}
}

// assertRedactedReasoningPersisted checks the common shape of the first three
// tests: the persisted record kept Content intact and redacted the secret.
func assertRedactedReasoningPersisted(t *testing.T, persisted []provider.Message) {
	t.Helper()
	if len(persisted) != 1 {
		t.Fatalf("persisted %d messages, want 1", len(persisted))
	}
	if persisted[0].Content != "keep" {
		t.Fatalf("visible content changed: %q", persisted[0].Content)
	}
	if !strings.Contains(persisted[0].ReasoningContent, "[redacted]") {
		t.Fatalf("persisted reasoning not redacted: %q", persisted[0].ReasoningContent)
	}
	if strings.Contains(persisted[0].ReasoningContent, "secret-1234") {
		t.Fatalf("persisted reasoning leaked secret: %q", persisted[0].ReasoningContent)
	}
}

// TestCatalogMessagesRedactsReasoning pins the context-catalog funnel
// (context_catalog.go catalogMessages → chat_sessions.messages SQLite): the
// canonical payload that lands in the catalog row must carry redacted
// reasoning and untouched content. catalogMessages builds the exact bytes
// saveContextSession passes to SaveSession, so the record is round-tripped
// through the real SQLite row the same way a context-enabled Session.Save
// persists it.
func TestCatalogMessagesRedactsReasoning(t *testing.T) {
	setTestReasoningRedactionPolicy(t, `(?i)secret-[0-9]+`)

	msgs := reasoningSecretMessages()
	data, err := catalogMessages(msgs)
	if err != nil {
		t.Fatalf("catalogMessages: %v", err)
	}

	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session-1", "local-user")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), principal, "s1", data, "model", "provider", 1, 10, 1, contextstate.SessionSaveOptions{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	raw, _, err := store.LoadSession(context.Background(), principal, "s1")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	var persisted []provider.Message
	if err := contextstate.UnmarshalCanonical(raw, &persisted); err != nil {
		t.Fatalf("decode persisted catalog record: %v", err)
	}
	assertRedactedReasoningPersisted(t, persisted)
}
