package cliautomations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestOpenAutomationStoreUsesSharedContextStorePathWhenUnset proves
// openAutomationStore resolves the SAME path clichat.ContextStorePath
// resolves for an unset [subagents] store_path - the shared, HOME-scoped
// default every chat session also opens (openContextStore in
// internal/clichat) - not a package-private "automations.db". Sharing
// this resolver is the whole point of this slice: before it, the CLI
// opened its own automations.db while the TUI opened the session's own
// ContextStorePath, so neither surface could see the other's runs.
func TestOpenAutomationStoreUsesSharedContextStorePathWhenUnset(t *testing.T) {
	root := t.TempDir()
	res := &config.Resolved{Subagents: config.SubagentConfig{}}

	db, err := openAutomationStore(root, res)
	if err != nil {
		t.Fatalf("openAutomationStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	wantPath := clichat.ContextStorePath(root, res.Subagents)
	notWantPath := workspace.NamespacePath(root, "automations.db")
	if wantPath == notWantPath {
		t.Fatalf("test fixture bug: wantPath %q collides with the old automations.db path", wantPath)
	}
	if _, statErr := os.Stat(notWantPath); statErr == nil {
		t.Fatalf("openAutomationStore also created the old automations.db path %q, want only %q", notWantPath, wantPath)
	}
	if _, statErr := os.Stat(wantPath); statErr != nil {
		t.Fatalf("openAutomationStore did not create the expected shared store at %q: %v", wantPath, statErr)
	}
}

// TestOpenAutomationStoreHonorsExplicitRelativeStorePath proves an
// operator-configured [subagents] store_path (a relative path, as this
// repo's own dogfooded .mivia/mivia.toml sets) resolves joined onto
// root, exactly like clichat.ContextStorePath resolves it for a chat
// session - not against the process's current working directory.
func TestOpenAutomationStoreHonorsExplicitRelativeStorePath(t *testing.T) {
	root := t.TempDir()
	res := &config.Resolved{Subagents: config.SubagentConfig{StorePath: ".mivia/context.db"}}

	db, err := openAutomationStore(root, res)
	if err != nil {
		t.Fatalf("openAutomationStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	wantPath := filepath.Join(root, ".mivia", "context.db")
	if got := clichat.ContextStorePath(root, res.Subagents); got != wantPath {
		t.Fatalf("clichat.ContextStorePath(%q, ...) = %q, want %q", root, got, wantPath)
	}
	if _, statErr := os.Stat(wantPath); statErr != nil {
		t.Fatalf("openAutomationStore did not create the store at the configured relative path %q: %v", wantPath, statErr)
	}
}

// TestStorePathForIgnoresWorktreeDirArgument proves storePathFor
// resolves purely from (root, cfg) - the same store regardless of which
// worktree directory a run executes in. A worktree run must NOT get its
// own namespaced store: CLI and TUI run history must stay in the one
// root/global store no matter where a step's working directory points.
// storePathFor's signature itself enforces this (it takes no dir
// parameter at all), so this test pins that CreateFreshInDir's own
// StorePath argument does not vary with the dir it is building a
// session in - see spawner_test.go for CreateFreshInDir's own dir
// handling of workspace/session-id concerns, which are unaffected by
// this slice.
func TestStorePathForIgnoresWorktreeDirArgument(t *testing.T) {
	root := t.TempDir()
	cfg := config.SubagentConfig{}
	got := storePathFor(root, cfg)
	want := clichat.ContextStorePath(root, cfg)
	if got != want {
		t.Fatalf("storePathFor(%q, cfg) = %q, want %q (clichat.ContextStorePath)", root, got, want)
	}
}
