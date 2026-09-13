package newtui

// Failure branches of the automation wiring. Every one of these is a
// "the TUI still starts" path: automation is an optional backend, so a
// wiring fault must disable it and log, never take the whole UI down with
// it. That contract is only real if the branches are exercised.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cli "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// TestWireAutomationBackendSurvivesAnUnopenableContextStore drives the
// fallback store-open error branch: the session carries no *storage.SQLite,
// so wiring resolves a path from the workspace root and opens it itself.
// Pointing that path at a directory makes the open fail. Automation must
// then be disabled with a working no-op closer, and the TUI must still come
// up.
func TestWireAutomationBackendSurvivesAnUnopenableContextStore(t *testing.T) {
	root := t.TempDir()
	// ContextStorePath resolves under the workspace root; make the target a
	// directory so OpenSQLite cannot create a database there.
	storePath := filepath.Join(root, ".mivia", "context.db")
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		t.Fatalf("prepare unopenable store path: %v", err)
	}

	res := &config.Resolved{Subagents: config.SubagentConfig{StorePath: ".mivia/context.db"}}
	sess := chat.NewSession(res, nil) // no context store: forces the fallback open
	agentState := &cli.AgentSessionState{WorkspaceRoot: root}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	closeFn, svc := wireAutomationBackend(store, pool, sess, agentState, res)
	if closeFn == nil {
		t.Fatal("wireAutomationBackend returned a nil closer after a failed store open")
	}
	if svc != nil {
		t.Fatal("wireAutomationBackend returned a service after a failed store open, want nil (automation disabled)")
	}
	closeFn() // must not panic on the disabled path
}

// TestWireAutomationBackendSkipsTheOpenWithoutAWorkspaceRoot pins the
// guard that avoids resolving a store path against an empty root: with no
// workspace there is nothing to open, automation stays disabled, and the
// closer is still safe to call.
func TestWireAutomationBackendSkipsTheOpenWithoutAWorkspaceRoot(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)
	agentState := &cli.AgentSessionState{WorkspaceRoot: ""}
	store := uiadapter.NewSettingsStore(sess, res, agentState)
	pool := uiadapter.NewCommandRunner(sess, res, agentState).Pool()

	closeFn, svc := wireAutomationBackend(store, pool, sess, agentState, res)
	if closeFn == nil {
		t.Fatal("wireAutomationBackend returned a nil closer with no workspace root")
	}
	if svc != nil {
		t.Fatal("wireAutomationBackend returned a service with no workspace root, want nil")
	}
	closeFn()
}

// TestWireRunActivityGuardWithoutAServiceRefusesNothing covers the nil-service
// arm of the predicate: automation wiring failed, so the installed source
// must report no session as run-owned - otherwise the live view would
// refuse sends for a backend that is not even running.
func TestWireRunActivityGuardWithoutAServiceRefusesNothing(t *testing.T) {
	screen := conversation.New(theme.Theme{}, theme.TierTrueColor, nil, nil, nil, 80, nil)
	wireRunActivityGuard(&screen, nil)

	// The predicate must be installed even with no service, so the screen
	// always has something to consult; its nil-service arm is what makes it
	// answer false. Reading the closure itself is not possible from here
	// (unexported field), so this asserts installation the same way
	// TestWireRunActivityGuardInstallsSource does.
	field := reflect.ValueOf(screen).FieldByName("runActivity")
	if field.IsNil() {
		t.Fatal("wireRunActivityGuard installed no predicate for a nil service")
	}
}

// TestWireRunActivityGuardIgnoresANilScreen pins the early return: wiring
// with no screen must return instead of dereferencing it.
func TestWireRunActivityGuardIgnoresANilScreen(t *testing.T) {
	panicked := func() (p any) {
		defer func() { p = recover() }()
		wireRunActivityGuard(nil, nil)
		return nil
	}()

	if panicked != nil {
		t.Fatalf("wireRunActivityGuard(nil, nil) panicked: %v, want the nil-screen guard to return", panicked)
	}
}
