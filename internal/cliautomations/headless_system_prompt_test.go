package cliautomations

// Prompt and advertised-snapshot contracts for a headless automation
// session. The session's surface parity assertions live beside these in
// headless_session_parity_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeadlessSessionKeepsAConfiguredSystemPrompt pins that the compiled
// root prompt is a FALLBACK: a workspace that sets its own [chat]
// system_prompt keeps it.
func TestHeadlessSessionKeepsAConfiguredSystemPrompt(t *testing.T) {
	root := writeAutomationsFixture(t, "prompt-configured")
	appendWorkspaceConfig(t, root, "\n[chat]\nsystem_prompt = \"WORKSPACE PROMPT\"\n")

	gotRoot, res, err := resolveWorkspaceAndConfig(root, "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig: %v", err)
	}
	spawn, err := NewHeadlessSpawner(gotRoot, res)
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if got := spawn.lastSession.BaseSystemPrompt; !strings.Contains(got, "WORKSPACE PROMPT") {
		t.Fatalf("configured system_prompt did not reach the session: %q", got)
	}
}

// TestHeadlessSpawnedSessionRefreshesPrefixIdentity pins that the session's
// cached prefix identity describes the advertised snapshot it actually
// carries. The cache hashes that snapshot, so a surface that pins without
// refreshing makes the next identity comparison - sess.Load on a resume, or
// a mid-turn admission - report a "tools" prefix reset for an unchanged
// wire array (INV-68-2).
func TestHeadlessSpawnedSessionRefreshesPrefixIdentity(t *testing.T) {
	sess, _, _ := spawnFixtureSession(t, "prompt-identity")

	current := sess.PrefixIdentity().ToolSchemaDigest
	if current == "" {
		t.Fatal("the cached tool-schema digest is empty after the attach")
	}
	sess.RefreshPrefixIdentity()
	if again := sess.PrefixIdentity().ToolSchemaDigest; again != current {
		t.Fatalf("digest moved on a refresh (%q -> %q): the cache was not current after the attach", current, again)
	}
}

// appendWorkspaceConfig appends extra TOML to root's own .mivia/mivia.toml,
// the file writeAutomationsFixture wrote and resolveWorkspaceAndConfig
// loads as the base config.
func appendWorkspaceConfig(t *testing.T, root, extra string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", "mivia.toml")
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(existing, []byte(extra)...), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
