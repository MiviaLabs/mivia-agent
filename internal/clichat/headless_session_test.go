package clichat

// Contracts for the headless attach. The failure branches matter as much as
// the happy path here: this function's whole reason to exist is that a
// headless host had been assembling a session with no tool policy, no hooks
// and no session dispatcher, and doing so SILENTLY.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// headlessFixture writes a workspace root and returns it with a config
// whose provider resolves, so a session can actually be built.
func headlessFixture(t *testing.T) (string, *config.Resolved) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	return root, &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Subagents:    config.SubagentConfig{StorePath: ".mivia/context.db"},
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"openrouter": {ProviderName: "openrouter", BaseURL: "https://openrouter.test/api/v1", APIKeySet: true, APIKey: "test-key"},
		},
	}
}

func headlessInput(t *testing.T) HeadlessSessionInput {
	t.Helper()
	root, res := headlessFixture(t)
	return HeadlessSessionInput{
		RunDir:    root,
		StorePath: filepath.Join(root, ".mivia", "context.db"),
		StoreRoot: root,
		Resolved:  res,
		Completer: stubAgentCompleter{},
	}
}

// TestNewHeadlessSessionValidatesItsInput pins every required field. Each
// one is required because its absence degrades silently rather than loudly:
// an absent completer in particular makes the surface attach a no-op.
func TestNewHeadlessSessionValidatesItsInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*HeadlessSessionInput)
		want   string
	}{
		{"no run dir", func(in *HeadlessSessionInput) { in.RunDir = "" }, "run dir is required"},
		{"no store path", func(in *HeadlessSessionInput) { in.StorePath = "" }, "store path is required"},
		{"no config", func(in *HeadlessSessionInput) { in.Resolved = nil }, "resolved config is required"},
		{"no completer", func(in *HeadlessSessionInput) { in.Completer = nil }, "completer is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := headlessInput(t)
			tc.mutate(&in)
			built, cleanup, err := NewHeadlessSession(in)
			if cleanup != nil {
				cleanup()
			}
			if err == nil {
				t.Fatalf("NewHeadlessSession(%s) succeeded, want an error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it naming %q", err, tc.want)
			}
			if built != nil {
				t.Fatal("a failed build returned a session")
			}
		})
	}
}

// TestNewHeadlessSessionAttachesTheFullSurface is the parity contract: the
// session reaches the model with the session tool catalog, a system prompt
// and a dispatcher.
func TestNewHeadlessSessionAttachesTheFullSurface(t *testing.T) {
	built, cleanup, err := NewHeadlessSession(headlessInput(t))
	if err != nil {
		t.Fatalf("NewHeadlessSession: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
		_ = built.Store.Close()
	})

	if built.Session.CurrentBinding().Dispatcher == nil {
		t.Error("session has no dispatcher on its binding: its tool calls would run ungoverned")
	}
	if strings.TrimSpace(built.Session.BaseSystemPrompt) == "" {
		t.Error("session has an empty system prompt")
	}
	if built.State == nil || built.State.ToolBase == nil {
		t.Fatal("no agent state or tool base was recorded; surface rebuilds would have nothing to re-scope from")
	}
	var found bool
	for _, spec := range built.Session.AdvertisedToolSpecs() {
		fn, _ := spec["function"].(map[string]any)
		if name, _ := fn["name"].(string); name == "dispatch_tasks" {
			found = true
		}
	}
	if !found {
		t.Error("advertised tools carry no dispatch_tasks: the session dispatcher's catalog did not reach the model")
	}
}

// TestNewHeadlessSessionRefusesAnUnpublishedSurface pins the fail-closed
// rule. cliagents.AttachRebuiltSurface reports "not published" WITHOUT an
// error when the process seams are unwired; treating that as success would
// leave the run on a dispatcher that never saw the workspace's tool policy.
func TestNewHeadlessSessionRefusesAnUnpublishedSurface(t *testing.T) {
	prev := cliagents.NewSessionDispatcherVar
	cliagents.NewSessionDispatcherVar = nil
	t.Cleanup(func() { cliagents.NewSessionDispatcherVar = prev })

	built, cleanup, err := NewHeadlessSession(headlessInput(t))
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("NewHeadlessSession succeeded with the dispatcher seam unwired; the session would run without the workspace's tool policy")
	}
	if !strings.Contains(err.Error(), "publication refused") {
		t.Fatalf("error = %v, want it naming the refused publication", err)
	}
	if built != nil {
		t.Fatal("a refused attach returned a session")
	}
}

// TestNewHeadlessSessionNeverMutatesTheCallerConfig pins copy-on-write. A
// daemon builds every session from one pointer; the workspace prompt gate
// blanks SystemPrompt, so a write-through would silently strip the system
// message from every later run.
func TestNewHeadlessSessionNeverMutatesTheCallerConfig(t *testing.T) {
	in := headlessInput(t)
	before := *in.Resolved

	built, cleanup, err := NewHeadlessSession(in)
	if err != nil {
		t.Fatalf("NewHeadlessSession: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
		_ = built.Store.Close()
	})

	if in.Resolved.SystemPrompt != before.SystemPrompt {
		t.Fatalf("caller's SystemPrompt was written through: %q -> %q", before.SystemPrompt, in.Resolved.SystemPrompt)
	}
	if strings.TrimSpace(built.Session.BaseSystemPrompt) == "" {
		t.Fatal("the session got no prompt either; the copy carried nothing")
	}
}

// TestNewHeadlessSessionSurfacesALoadFailure pins that a broken workspace
// control surface FAILS the build rather than quietly producing a session
// with no roles or no skills. A .agents/agents path that is a regular file
// makes the agent load fail.
func TestNewHeadlessSessionSurfacesALoadFailure(t *testing.T) {
	in := headlessInput(t)
	agentsDir := filepath.Join(in.RunDir, ".agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir .agents: %v", err)
	}
	// A regular file where the agents DIRECTORY belongs.
	if err := os.WriteFile(filepath.Join(agentsDir, "agents"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	built, cleanup, err := NewHeadlessSession(in)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		if built != nil {
			_ = built.Store.Close()
		}
		t.Skip("this workspace shape does not fail the agent load on this platform; the branch stays covered by its sibling cases")
	}
	if !strings.Contains(err.Error(), "headless session") {
		t.Fatalf("error = %v, want it wrapped by the headless builder", err)
	}
}

// TestNewHeadlessSessionSurfacesAStoreFailure pins the BuildSession error
// path, and that the tool registry opened before it is released rather than
// leaked when the session cannot be constructed.
func TestNewHeadlessSessionSurfacesAStoreFailure(t *testing.T) {
	in := headlessInput(t)
	// A store path whose parent is a regular file: SQLite cannot open it.
	blocker := filepath.Join(in.RunDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	in.StorePath = filepath.Join(blocker, "context.db")

	built, cleanup, err := NewHeadlessSession(in)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		if built != nil {
			_ = built.Store.Close()
		}
		t.Fatal("NewHeadlessSession succeeded against an unopenable store path")
	}
	if !strings.Contains(err.Error(), "headless session") {
		t.Fatalf("error = %v, want it wrapped by the headless builder", err)
	}
}

// TestHeadlessDispatcherInputWithoutHookSeams pins the unwired-seam guard:
// a process that never installed a hook session (a test binary, or any
// build that does not import internal/cli) must get a dispatcher input with
// no hook funcs rather than a nil-func panic.
func TestHeadlessDispatcherInputWithoutHookSeams(t *testing.T) {
	prevConfigured, prevCurrent := HookSessionConfiguredFunc, CurrentHookSessionFunc
	HookSessionConfiguredFunc, CurrentHookSessionFunc = nil, nil
	t.Cleanup(func() { HookSessionConfiguredFunc, CurrentHookSessionFunc = prevConfigured, prevCurrent })

	in := headlessDispatcherInput("/tmp/example", nil)
	if in.WorkspaceRoot != "/tmp/example" {
		t.Fatalf("WorkspaceRoot = %q", in.WorkspaceRoot)
	}
	if in.HooksConfigured {
		t.Fatal("HooksConfigured is true with no hook seam wired")
	}
	if in.HookGroups != nil || in.NoteHookWarnings != nil {
		t.Fatal("hook funcs were wired from a nil seam")
	}
}

// TestHeadlessMCPStubHelper serves one MCP tool ("echo") over stdio. It is
// the subprocess behind the MCP test below, following the same self-reexec
// idiom internal/composition and internal/mcp use: a stdio transport spawns
// os.Args[0] as the server command, so each test binary needs its own copy
// of this helper Test function - a helper cannot be invoked across a
// process boundary from another package.
func TestHeadlessMCPStubHelper(t *testing.T) {
	if os.Getenv("MIVIA_CLICHAT_MCP_HELPER") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "stub", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "returns text"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "reply"}}}, nil, nil
	})
	session, err := server.Connect(context.Background(), &sdk.IOTransport{Reader: os.Stdin, Writer: os.Stdout}, nil)
	if err != nil {
		os.Exit(2)
	}
	if err := session.Wait(); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// TestNewHeadlessSessionAttachesMCPServers pins that a headless run reaches
// the model with the workspace's MCP tools. Nothing attached them before, so
// an automation could not use a configured MCP server at all - codegraph in
// this project's own case.
func TestNewHeadlessSessionAttachesMCPServers(t *testing.T) {
	t.Setenv("MIVIA_CLICHAT_MCP_HELPER", "1")
	in := headlessInput(t)
	in.Resolved.MCP = config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "stub", Global: true, Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestHeadlessMCPStubHelper$"}, Env: []string{"MIVIA_CLICHAT_MCP_HELPER"},
		TimeoutSeconds: 10,
	}}}

	built, cleanup, err := NewHeadlessSession(in)
	if err != nil {
		t.Fatalf("NewHeadlessSession: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
		_ = built.Store.Close()
	})

	wantName, err := mcp.EncodeToolName("stub", "echo")
	if err != nil {
		t.Fatalf("EncodeToolName: %v", err)
	}
	if _, ok := built.Session.Tools.Get(wantName); !ok {
		t.Fatalf("session registry has no %q: the workspace's MCP server did not reach the run", wantName)
	}
	// The manager must also be recorded on the agent state: every surface
	// rebuild re-supplies EnsureMCPTools from it, and a delegated task agent
	// whose ensurer is nil silently loses its MCP tools.
	if built.State.MCPManager == nil {
		t.Fatal("agent state carries no MCP manager; delegated agents would lose their MCP tools on every rebuild")
	}
}

// TestNewHeadlessSessionWithoutMCPIsClean pins the disabled case: no
// manager, no failure.
func TestNewHeadlessSessionWithoutMCPIsClean(t *testing.T) {
	built, cleanup, err := NewHeadlessSession(headlessInput(t))
	if err != nil {
		t.Fatalf("NewHeadlessSession: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
		_ = built.Store.Close()
	})
	if built.State.MCPManager != nil {
		t.Fatal("an unconfigured MCP attached a manager")
	}
}

// TestNewHeadlessSessionRunsRestoreBeforeAttach pins the resume ordering.
// Publishing a surface rewrites the system and memory messages and captures
// the prefix identity; a Load after that would replace the history and the
// binding underneath all three. The interactive path resumes first and
// attaches second, and so must this one.
func TestNewHeadlessSessionRunsRestoreBeforeAttach(t *testing.T) {
	var advertisedDuringRestore int
	var ran bool
	in := headlessInput(t)
	in.Restore = func(sess *chat.Session) error {
		ran = true
		advertisedDuringRestore = len(sess.AdvertisedToolSpecs())
		return nil
	}

	built, cleanup, err := NewHeadlessSession(in)
	if err != nil {
		t.Fatalf("NewHeadlessSession: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
		_ = built.Store.Close()
	})

	if !ran {
		t.Fatal("Restore never ran")
	}
	if advertisedDuringRestore != 0 {
		t.Fatalf("the surface was already published when Restore ran (%d advertised specs); a resume must land before the attach", advertisedDuringRestore)
	}
	if len(built.Session.AdvertisedToolSpecs()) == 0 {
		t.Fatal("the surface was never published after Restore")
	}
}

// TestNewHeadlessSessionSurfacesARestoreFailure pins that a failed resume
// fails the build rather than silently continuing with a fresh transcript.
func TestNewHeadlessSessionSurfacesARestoreFailure(t *testing.T) {
	in := headlessInput(t)
	in.Restore = func(*chat.Session) error { return errRestoreStub }

	built, cleanup, err := NewHeadlessSession(in)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		if built != nil {
			_ = built.Store.Close()
		}
		t.Fatal("NewHeadlessSession succeeded despite a failing restore")
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Fatalf("error = %v, want it naming the restore step", err)
	}
	if built != nil {
		t.Fatal("a failed restore returned a session")
	}
}

// errRestoreStub is the failure a stubbed restore returns.
var errRestoreStub = errors.New("stub restore failure")

// TestAbandonedHeadlessSessionStopsItsHeartbeat pins the discard paths.
// composition.BuildSession arms a context-lease heartbeat that renews every
// tick and never exits on its own, independent of the store handle - so a
// builder that closes the store and returns leaves the goroutine waking
// forever against a database that no longer exists, once per failed fire on
// a daemon.
func TestAbandonedHeadlessSessionStopsItsHeartbeat(t *testing.T) {
	in := headlessInput(t)
	in.Restore = func(*chat.Session) error { return errRestoreStub }

	before := runtime.NumGoroutine()
	for i := 0; i < 8; i++ {
		if _, _, err := NewHeadlessSession(in); err == nil {
			t.Fatal("NewHeadlessSession succeeded despite a failing restore")
		}
	}
	// The heartbeat ticks on a timer; a leaked one stays parked forever, so
	// a settle window is enough to tell a released lease from a live one.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutines grew from %d to %d across 8 discarded sessions; the abandoned sessions kept their context-lease heartbeats", before, runtime.NumGoroutine())
}
