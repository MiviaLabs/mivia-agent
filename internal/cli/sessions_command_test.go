package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
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

// TestSessionsShowRedactsToolCallsAndResults pins the fix for a real leak:
// "sessions show" used to print provider.Message straight from storage,
// never consulting the workspace's configured redaction policy at all - a
// live turn's NDJSON preview of the same tool call always does (see
// redactToolInput/redactToolOutputForTool in
// internal/agent/loop_tool_preview.go). Per redact's own "nothing is a
// secret until configuration says so" contract, this test brings its own
// policy (as every redaction test in this codebase must) rather than relying
// on a compiled-in default - the assertion here is that the policy fires at
// all through this call site, not that any particular pattern ships.
func TestSessionsShowRedactsToolCallsAndResults(t *testing.T) {
	policy, err := redact.Compile([]string{`sk-ant-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("redact.Compile: %v", err)
	}
	old := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(old) })

	ws := isolatedSessionsWorkspace(t)
	seedCatalogSession(t, ws, "secretive", []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "run_command",
						Arguments: `{"argv":["curl","-H","Authorization: Bearer sk-ant-abc123"]}`,
					},
				},
			},
		},
		{
			Role:       provider.RoleTool,
			ToolCallID: "call_1",
			Content:    "api_key=sk-ant-abc123 succeeded",
		},
	})

	var buf bytes.Buffer
	if err := runSessionsShow([]string{"secretive", "--workspace", ws, "--json"}, &buf); err != nil {
		t.Fatalf("sessions show secretive: %v", err)
	}
	if strings.Contains(buf.String(), "sk-ant-abc123") {
		t.Fatalf("sessions show secretive: unredacted secret leaked into output: %s", buf.String())
	}

	var msgs []provider.Message
	if err := json.Unmarshal(buf.Bytes(), &msgs); err != nil {
		t.Fatalf("decode sessions show JSON: %v\nraw: %s", err, buf.String())
	}
	if len(msgs) != 2 {
		t.Fatalf("sessions show secretive: got %d messages, want 2: %+v", len(msgs), msgs)
	}
	if got := msgs[0].ToolCalls[0].Function.Arguments; strings.Contains(got, "sk-ant-abc123") {
		t.Errorf("tool call arguments not redacted: %q", got)
	}
	if got := msgs[1].Content; strings.Contains(got, "sk-ant-abc123") {
		t.Errorf("tool result content not redacted: %q", got)
	}
}

// TestRedactSessionMessagesForDisplayDoesNotMutateOriginal pins the
// contract redactSessionMessagesForDisplay's doc comment claims: it must
// return copies, never mutate a shared ToolCalls backing array MessagesCopy
// only shallow-copies the slice header of - the caller's original slice's
// tool-call arguments must be unchanged after redaction.
func TestRedactSessionMessagesForDisplayDoesNotMutateOriginal(t *testing.T) {
	original := []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{
					ID: "call_1",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "run_command", Arguments: "api_key=sk-ant-abc123"},
				},
			},
		},
	}

	_ = redactSessionMessagesForDisplay(original)

	if got := original[0].ToolCalls[0].Function.Arguments; got != "api_key=sk-ant-abc123" {
		t.Fatalf("redactSessionMessagesForDisplay mutated the original message: %q", got)
	}
}

// TestSessionsUsageReportsContextEstimate pins the read-only context
// accounting query the desktop app seeds its context indicator from when a
// saved thread is reopened: the SAME numbers a resumed chat session's TUI
// status dialog shows (Session.ContextUsage over the loaded, post-compaction
// messages), not the stale whole-session token_count the catalog carries.
// Assertions run on the raw JSON map so the contract an external consumer
// parses is what is pinned.
func TestSessionsUsageReportsContextEstimate(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var buf bytes.Buffer
	if err := runSessionsWithIO([]string{"usage", "alpha", "--workspace", ws, "--json"}, &buf, &bytes.Buffer{}); err != nil {
		t.Fatalf("sessions usage alpha: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("decode sessions usage JSON: %v\nraw: %s", err, buf.String())
	}
	for _, field := range []string{"used_tokens", "budget_tokens", "context_window_tokens", "output_reserve_tokens", "percent"} {
		if _, ok := raw[field]; !ok {
			t.Fatalf("sessions usage JSON missing %q: %s", field, buf.String())
		}
	}
	// alpha carries two seeded messages, so the estimate must be non-zero.
	if raw["used_tokens"].(float64) <= 0 {
		t.Fatalf("sessions usage used_tokens = %v, want > 0 for a seeded session", raw["used_tokens"])
	}
	budget := raw["budget_tokens"].(float64)
	used := raw["used_tokens"].(float64)
	if budget <= 0 {
		t.Fatalf("sessions usage budget_tokens = %v, want > 0", budget)
	}
	wantPercent := int(used * 100 / budget)
	if raw["percent"].(float64) != float64(wantPercent) {
		t.Fatalf("sessions usage percent = %v, want %d (used*100/budget)", raw["percent"], wantPercent)
	}
}

// TestSessionsUsageUnknownNameFails keeps the query's failure shape aligned
// with the other read-only sessions subcommands.
func TestSessionsUsageUnknownNameFails(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	if err := runSessionsWithIO([]string{"usage", "nope", "--workspace", ws, "--json"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("sessions usage on an unknown session should fail")
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

// TestSessionsShowSurvivesModelSwitchedAwayFromConfigDefault pins a real
// regression: a session saved under a provider/model that differs from the
// workspace config's currently-active default (e.g. after switching models
// mid-conversation, then reopening the thread later - see
// mivia-agent-desktop's ModelSwitcherButton) used to fail "sessions show"
// outright ("incomplete model binding", then "stale binding: context
// binding changed") - the raw messages were never actually lost, "show"
// just couldn't reach them without behaving like a live resume. LoadReadOnly
// (chat/context_catalog.go) fixes this by never requiring a working
// completer or durably advancing a binding just to display history.
func TestSessionsShowSurvivesModelSwitchedAwayFromConfigDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := `[provider]
name = "ollama"

[providers.ollama]
base_url = "http://127.0.0.1:1/v1"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]

[providers.openrouter]
base_url = "http://127.0.0.1:1/v1"
models = [{ name = "some/other-model", context_window_tokens = 128000 }]
`
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIVIA_CONFIG", cfgPath)

	// Seed a session, then switch its binding to the SECOND provider/model
	// (not the config's active "ollama" default) before saving - mirroring
	// a session that was resumed and switched mid-conversation.
	sess, store, err := newCatalogSession(ws)
	if err != nil {
		t.Fatalf("newCatalogSession: %v", err)
	}
	defer store.Close()
	res, err := config.Load(config.LoadOptions{WorkspaceRoot: ws, AllowMissingConfig: true})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	profile, ok := configuredProfile(res, "openrouter", "some/other-model")
	if !ok {
		t.Fatalf("configuredProfile did not find the seeded openrouter model")
	}
	if err := sess.SwitchBinding(chat.ModelBinding{
		ProviderName: "openrouter",
		Model:        "some/other-model",
		Completer:    catalogReadOnlyCompleter{providerName: "openrouter"},
		Profile:      profile,
	}); err != nil {
		t.Fatalf("SwitchBinding to openrouter: %v", err)
	}
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "hello from openrouter"},
		{Role: provider.RoleAssistant, Content: "hi from openrouter"},
	}
	if err := sess.Save("switched"); err != nil {
		t.Fatalf("Save(switched): %v", err)
	}

	var showBuf bytes.Buffer
	if err := runSessionsShow([]string{"switched", "--workspace", ws, "--json"}, &showBuf); err != nil {
		t.Fatalf("sessions show switched: %v", err)
	}
	var msgs []provider.Message
	if err := json.Unmarshal(showBuf.Bytes(), &msgs); err != nil {
		t.Fatalf("decode sessions show JSON: %v\nraw: %s", err, showBuf.String())
	}
	if len(msgs) != 2 || msgs[0].Content != "hello from openrouter" || msgs[1].Content != "hi from openrouter" {
		t.Fatalf("sessions show switched = %+v, want the seeded openrouter turn", msgs)
	}
}

func TestSessionsDelete(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var deleteStderr bytes.Buffer
	if err := runSessionsDelete([]string{"beta", "--workspace", ws}, io.Discard, &deleteStderr); err != nil {
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
	if err := runSessionsDelete([]string{"beta", "--workspace", ws}, io.Discard, &missingStderr); err == nil {
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
	if err := runSessionsRename([]string{"alpha", "Project kickoff", "--workspace", ws}, io.Discard, &snapshotStderr); err == nil {
		t.Fatal("sessions rename alpha (a named snapshot, not a live session): want an error, got nil")
	}

	var missingStderr bytes.Buffer
	if err := runSessionsRename([]string{"does-not-exist", "New title", "--workspace", ws}, io.Discard, &missingStderr); err == nil {
		t.Fatal("sessions rename does-not-exist: want an error, got nil")
	}
}

// TestSessionsRenameJSONEnvelope pins the --json success shape a frontend
// consumes: without it, rename reported success only through the exit code,
// and a caller had no confirmation of the title actually stored.
func TestSessionsRenameJSONEnvelope(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	sess, done := seedLiveCompactableSession(t, ws)
	defer done()
	name := sess.SessionID

	var stdout, stderr bytes.Buffer
	if err := runSessionsRename([]string{name, "Project kickoff", "--workspace", ws, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("sessions rename %s: %v (stderr: %s)", name, err, stderr.String())
	}
	var envelope struct {
		Renamed struct {
			Session string `json:"session"`
			Title   string `json:"title"`
		} `json:"renamed"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode rename JSON: %v\nraw: %s", err, stdout.String())
	}
	if envelope.Renamed.Session != name || envelope.Renamed.Title != "Project kickoff" {
		t.Fatalf("rename envelope = %+v", envelope.Renamed)
	}
}

// TestSessionsDeleteJSONEnvelope pins the --json success shape: the deleted
// session name on stdout, so a frontend confirms the removal without
// inferring it from the exit code.
func TestSessionsDeleteJSONEnvelope(t *testing.T) {
	ws := isolatedSessionsWorkspace(t)
	seedThreeCatalogSessions(t, ws)

	var stdout, stderr bytes.Buffer
	if err := runSessionsDelete([]string{"beta", "--workspace", ws, "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("sessions delete beta: %v (stderr: %s)", err, stderr.String())
	}
	var envelope struct {
		Deleted string `json:"deleted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode delete JSON: %v\nraw: %s", err, stdout.String())
	}
	if envelope.Deleted != "beta" {
		t.Fatalf("delete envelope = %+v, want beta", envelope)
	}

	// Without --json the success path keeps writing nothing to stdout.
	var plainStdout bytes.Buffer
	if err := runSessionsDelete([]string{"alpha", "--workspace", ws}, &plainStdout, &stderr); err != nil {
		t.Fatalf("sessions delete alpha (plain): %v", err)
	}
	if plainStdout.Len() != 0 {
		t.Fatalf("plain delete wrote to stdout: %s", plainStdout.String())
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
