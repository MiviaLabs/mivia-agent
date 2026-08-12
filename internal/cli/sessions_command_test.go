package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// seedCatalogSession opens a context-catalog session under ws and saves it as
// name with the given messages, mirroring how a real chat turn (or an
// explicit /save) lands a snapshot in the same storage "mivia sessions" reads.
func seedCatalogSession(t *testing.T, ws, name string, msgs []provider.Message) {
	t.Helper()
	sess, store, err := newCatalogSession(ws)
	if err != nil {
		t.Fatalf("newCatalogSession(%s): %v", ws, err)
	}
	defer store.Close()
	sess.Messages = msgs
	if err := sess.Save(name); err != nil {
		t.Fatalf("Save(%s): %v", name, err)
	}
}

// isolatedSessionsWorkspace returns a HOME-isolated, non-git temp workspace
// so "mivia sessions" resolves the plain per-workspace context store
// (setupSessionContext) rather than a repository-scoped one, and so this
// test's session catalog never collides with the developer's real ~/.mivia.
//
// A minimal single-provider config is written because config.Load's default
// provider (deepseek) requires a non-empty configured model list even under
// AllowMissingConfig; newCatalogSession never dials a provider (nil
// completer, read-only catalog ops only), so which provider is named here is
// unimportant.
func isolatedSessionsWorkspace(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLLAMA_API_KEY", "")
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := `[provider]
name = "ollama"

[providers.ollama]
base_url = "http://127.0.0.1:1/v1"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]
`
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	// newCatalogSession's config.Load call has no --config flag of its own
	// (sessions only takes --workspace); MIVIA_CONFIG is config.Load's other
	// discovery path (see DefaultConfigCandidates), and is what points it at
	// this fixture regardless of the test binary's cwd.
	t.Setenv("MIVIA_CONFIG", cfgPath)
	return ws
}

// seedThreeCatalogSessions seeds the fixed alpha/beta/gamma sessions the
// list/show/delete sub-tests below all share, so each sub-test's setup is one
// line instead of a repeated block.
func seedThreeCatalogSessions(t *testing.T, ws string) {
	t.Helper()
	seedCatalogSession(t, ws, "alpha", []provider.Message{
		{Role: provider.RoleUser, Content: "hello alpha"},
		{Role: provider.RoleAssistant, Content: "hi back"},
	})
	seedCatalogSession(t, ws, "beta", []provider.Message{
		{Role: provider.RoleUser, Content: "hello beta"},
	})
	seedCatalogSession(t, ws, "gamma", []provider.Message{
		{Role: provider.RoleUser, Content: "hello gamma"},
	})
}

func TestSessionsList(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var listBuf bytes.Buffer
	if err := runSessionsList([]string{"--workspace", ws, "--json"}, &listBuf); err != nil {
		t.Fatalf("sessions list: %v", err)
	}
	var infos []chat.SessionInfo
	if err := json.Unmarshal(listBuf.Bytes(), &infos); err != nil {
		t.Fatalf("decode sessions list JSON: %v\nraw: %s", err, listBuf.String())
	}
	if len(infos) != 3 {
		t.Fatalf("sessions list: got %d entries, want 3: %+v", len(infos), infos)
	}
	names := map[string]bool{}
	for _, info := range infos {
		names[info.Name] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !names[want] {
			t.Errorf("sessions list: missing %q, got %+v", want, infos)
		}
	}
}

func TestSessionsShow(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var showBuf bytes.Buffer
	if err := runSessionsShow([]string{"alpha", "--workspace", ws, "--json"}, &showBuf); err != nil {
		t.Fatalf("sessions show alpha: %v", err)
	}
	var msgs []provider.Message
	if err := json.Unmarshal(showBuf.Bytes(), &msgs); err != nil {
		t.Fatalf("decode sessions show JSON: %v\nraw: %s", err, showBuf.String())
	}
	if len(msgs) != 2 {
		t.Fatalf("sessions show alpha: got %d messages, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != provider.RoleUser || msgs[0].Content != "hello alpha" {
		t.Errorf("sessions show alpha: message[0] = %+v, want the seeded user turn", msgs[0])
	}
	if msgs[1].Role != provider.RoleAssistant || msgs[1].Content != "hi back" {
		t.Errorf("sessions show alpha: message[1] = %+v, want the seeded assistant turn", msgs[1])
	}

	// --limit trims to the most recent N messages.
	var limitedBuf bytes.Buffer
	if err := runSessionsShow([]string{"alpha", "--workspace", ws, "--json", "--limit", "1"}, &limitedBuf); err != nil {
		t.Fatalf("sessions show alpha --limit 1: %v", err)
	}
	var limited []provider.Message
	if err := json.Unmarshal(limitedBuf.Bytes(), &limited); err != nil {
		t.Fatalf("decode limited sessions show JSON: %v", err)
	}
	if len(limited) != 1 || limited[0].Content != "hi back" {
		t.Fatalf("sessions show alpha --limit 1 = %+v, want just the last message", limited)
	}
}

func TestSessionsDelete(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var deleteStderr bytes.Buffer
	if err := runSessionsDelete([]string{"beta", "--workspace", ws}, &deleteStderr); err != nil {
		t.Fatalf("sessions delete beta: %v (stderr: %s)", err, deleteStderr.String())
	}

	var afterDeleteBuf bytes.Buffer
	if err := runSessionsList([]string{"--workspace", ws, "--json"}, &afterDeleteBuf); err != nil {
		t.Fatalf("sessions list after delete: %v", err)
	}
	var afterDelete []chat.SessionInfo
	if err := json.Unmarshal(afterDeleteBuf.Bytes(), &afterDelete); err != nil {
		t.Fatalf("decode post-delete sessions list JSON: %v", err)
	}
	if len(afterDelete) != 2 {
		t.Fatalf("sessions list after delete: got %d entries, want 2: %+v", len(afterDelete), afterDelete)
	}
	for _, info := range afterDelete {
		if info.Name == "beta" {
			t.Fatalf("sessions list after delete: %q still present", "beta")
		}
	}

	// Deleting an already-deleted (or never-existing) session must fail
	// clearly, not silently succeed.
	var missingStderr bytes.Buffer
	if err := runSessionsDelete([]string{"beta", "--workspace", ws}, &missingStderr); err == nil {
		t.Fatal("sessions delete beta (already gone): want an error, got nil")
	}
}

// runSessionsRename targets the live context-session catalog
// (chat.Session.SetContextSessionTitle -> the context_sessions table), the
// only kind of session a real `mivia chat` conversation - and so the only
// kind mivia-agent-desktop's sidebar ever shows - actually produces. A named
// snapshot (`seedCatalogSession`'s chat_sessions-table rows, from an explicit
// `/save <name>`) has no row there, so renaming one fails clearly rather
// than silently doing nothing; TestIntegrationSetContextSessionTitle in
// internal/chat covers the success path against a real live session, which
// this package's test fixtures (a nil completer - see newCatalogSession's
// doc comment) cannot construct.
func TestSessionsRenameUnknownOrNamedSnapshotFails(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var snapshotStderr bytes.Buffer
	if err := runSessionsRename([]string{"alpha", "Project kickoff", "--workspace", ws}, &snapshotStderr); err == nil {
		t.Fatal("sessions rename alpha (a named snapshot, not a live session): want an error, got nil")
	}

	var missingStderr bytes.Buffer
	if err := runSessionsRename([]string{"does-not-exist", "New title", "--workspace", ws}, &missingStderr); err == nil {
		t.Fatal("sessions rename does-not-exist: want an error, got nil")
	}
}

func TestSessionsShowUnknownNameFails(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	var buf bytes.Buffer
	err := runSessionsShow([]string{"does-not-exist", "--workspace", ws, "--json"}, &buf)
	if err == nil {
		t.Fatal("sessions show does-not-exist: want an error, got nil")
	}
}

func TestSessionsListEmptyWorkspace(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	var buf bytes.Buffer
	if err := runSessionsList([]string{"--workspace", ws, "--json"}, &buf); err != nil {
		t.Fatalf("sessions list (empty): %v", err)
	}
	var infos []chat.SessionInfo
	if err := json.Unmarshal(buf.Bytes(), &infos); err != nil {
		t.Fatalf("decode empty sessions list JSON: %v\nraw: %s", err, buf.String())
	}
	if len(infos) != 0 {
		t.Fatalf("sessions list (empty workspace): got %d entries, want 0: %+v", len(infos), infos)
	}
}

func TestSessionsCommandArgParsing(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	if err := runSessionsWithIO(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions with no subcommand: want an error, got nil")
	}
	if err := runSessionsWithIO([]string{"bogus"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions bogus: want an error, got nil")
	}
	if err := runSessionsWithIO([]string{"show"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions show (no name): want an error, got nil")
	}
	if err := runSessionsWithIO([]string{"show", "a", "b", "--workspace", ws}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions show with two positional names: want an error, got nil")
	}
	if err := runSessionsWithIO([]string{"list", "--unknown-flag"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions list --unknown-flag: want an error, got nil")
	}
	if err := runSessionsWithIO([]string{"rename", "a"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions rename (no title): want an error, got nil")
	}
}
