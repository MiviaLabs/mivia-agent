package clichat

// Build-path failure contracts for the headless session, split from
// headless_session_test.go to keep both files inside the per-file LOC
// budget. Every test here drives one error branch of buildHeadlessCore or
// its helpers: those branches exist because a headless run that degrades
// silently is how this host once produced sessions with no tool policy at
// all, so each one must be proven to fail LOUDLY rather than fall through.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// pinUserConfig points config.UserConfigPath at a temp HOME and returns the
// user-level mivia.toml path, so a test can make the trusted user config
// unreadable or malformed without touching the developer's real one.
func pinUserConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows
	userCfg := config.UserConfigPath()
	if userCfg == "" {
		// Not a skip: UserConfigPath derives from the HOME/USERPROFILE
		// just set, so an empty result means path resolution itself broke
		// and every assertion below would be meaningless.
		t.Fatal("config.UserConfigPath() is empty with HOME set; cannot pin the user-config failure path")
	}
	if err := os.MkdirAll(filepath.Dir(userCfg), 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	return userCfg
}

// TestNewHeadlessSessionSurfacesAnAgentsConfigFailure covers
// loadHeadlessAgentState's config.LoadAgentsGlobal error wrap. The [agents]
// gate decides whether the run honors the workspace's own tool policy, so a
// config it cannot parse must stop the run rather than silently fall back
// to defaults that may be more permissive than the operator intended.
func TestNewHeadlessSessionSurfacesAnAgentsConfigFailure(t *testing.T) {
	userCfg := pinUserConfig(t)
	if err := os.WriteFile(userCfg, []byte("[agents\nthis is not valid toml"), 0o600); err != nil {
		t.Fatalf("write malformed user config: %v", err)
	}

	in := headlessInput(t)
	built, cleanup, err := NewHeadlessSession(in)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("NewHeadlessSession succeeded with a malformed [agents] config, want an error")
	}
	if !strings.Contains(err.Error(), "headless session agents config") {
		t.Fatalf("error = %v, want it wrapped as a headless agents-config failure", err)
	}
	if built != nil {
		t.Fatal("a failed build returned a session")
	}
}

// TestNewHeadlessSessionSurfacesAWorkspaceConfigFailure pins that a run
// directory whose committed .mivia/mivia.toml cannot be parsed stops the
// build instead of falling back to defaults. The failure lands in
// loadHeadlessAgentState's agent-definition load, which reads that same
// file before the tool registry is ever built - so a broken workspace
// config can never reach the session as a silently relaxed policy.
func TestNewHeadlessSessionSurfacesAWorkspaceConfigFailure(t *testing.T) {
	in := headlessInput(t)
	wsCfg := filepath.Join(in.RunDir, ".mivia", "mivia.toml")
	if err := os.WriteFile(wsCfg, []byte("[tools\nnot valid toml at all"), 0o600); err != nil {
		t.Fatalf("write malformed workspace config: %v", err)
	}

	built, cleanup, err := NewHeadlessSession(in)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("NewHeadlessSession succeeded with a malformed workspace config, want an error")
	}
	if !strings.Contains(err.Error(), "clichat: headless session") {
		t.Fatalf("error = %v, want it wrapped as a headless session failure", err)
	}
	if !strings.Contains(err.Error(), wsCfg) {
		t.Fatalf("error = %v, want it to name the offending config path %s", err, wsCfg)
	}
	if built != nil {
		t.Fatal("a failed build returned a session")
	}
}

// TestHeadlessStoreRootFallsBackToRunDir pins storeRoot's own branch: an
// omitted StoreRoot must resolve to RunDir, because the workspace id the
// checkpoint principal is minted from is derived from it - a wrong value
// there puts a run's saved session in a namespace /resume cannot read.
func TestHeadlessStoreRootFallsBackToRunDir(t *testing.T) {
	in := HeadlessSessionInput{RunDir: "/run/dir", StoreRoot: "/explicit/root"}
	if got := in.storeRoot(); got != "/explicit/root" {
		t.Fatalf("storeRoot() = %q, want the explicit StoreRoot", got)
	}
	in.StoreRoot = ""
	if got := in.storeRoot(); got != "/run/dir" {
		t.Fatalf("storeRoot() = %q, want it to fall back to RunDir", got)
	}
}
