package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

const twoHookConfig = `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./gate.sh"]

[[hooks]]
event = "PostToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./fmt.sh"]
`

// hookHome points HOME at a fresh directory holding a user config, so the
// trust store and the hook source both live in the fixture.
func hookHome(t *testing.T, body string) (home, ws string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	ws = filepath.Join(base, "ws")
	for _, dir := range []string{filepath.Join(home, ".mivia"), filepath.Join(ws, ".mivia")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".mivia", "mivia.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	return home, ws
}

func TestHooksListShowsEveryDiscoveredHookAsPendingOnAFreshInstall(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	if len(session.decisions) != 2 {
		t.Fatalf("want 2 hooks, got %d", len(session.decisions))
	}
	if len(session.runnable()) != 0 {
		t.Fatal("a fresh install runs zero hooks")
	}
	out := renderHookList(session)
	for _, want := range []string{"PreToolUse", "PostToolUse", "run_command", "gate.sh", "fmt.sh", string(hooks.StatusPending)} {
		if !strings.Contains(out, want) {
			t.Errorf("listing must mention %q; got:\n%s", want, out)
		}
	}
}

// The resolved timeout and on_timeout are displayed, not the blank the author
// left: an operator reading the list must see what actually applies.
func TestHooksListShowsResolvedTimeoutAndVerdict(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	out := renderHookList(session)
	if !strings.Contains(out, "on_timeout=block") {
		t.Errorf("PreToolUse resolves to on_timeout=block and must say so; got:\n%s", out)
	}
	if !strings.Contains(out, "on_timeout=allow") {
		t.Errorf("PostToolUse resolves to on_timeout=allow and must say so; got:\n%s", out)
	}
	if !strings.Contains(out, "timeout=10s") || !strings.Contains(out, "5s") && !strings.Contains(out, "10s") {
		t.Errorf("resolved timeouts must be shown; got:\n%s", out)
	}
}

// A reader who assumes a confirmation covers the script body has the wrong
// threat model, and the listing is where that gets corrected.
func TestHooksListStatesTheScriptBodyIsNotCovered(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	out := strings.ToLower(renderHookList(session))
	if !strings.Contains(out, "script") || !strings.Contains(out, "not") {
		t.Fatalf("the listing must state that the script body is not covered by trust; got:\n%s", renderHookList(session))
	}
}

func TestHooksTrustPromotesExactlyOneHook(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	msg := session.trust("1")
	if strings.Contains(strings.ToLower(msg), "error") || strings.Contains(msg, "not a hook") {
		t.Fatalf("trust 1 failed: %s", msg)
	}
	if got := len(session.runnable()); got != 1 {
		t.Fatalf("want exactly 1 runnable hook after promoting one, got %d", got)
	}
	if session.decisions[0].Status != hooks.StatusActive {
		t.Fatalf("hook 1 status = %q, want active", session.decisions[0].Status)
	}
	if session.decisions[1].Status != hooks.StatusPending {
		t.Fatalf("hook 2 must stay pending, got %q", session.decisions[1].Status)
	}
}

func TestHooksTrustPersistsAcrossSessions(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	first, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	first.trust("1")

	second, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(second.runnable()) != 1 {
		t.Fatal("a promotion must survive into the next session")
	}
}

// hash-changed is displayed distinctly from pending.
func TestHooksListDistinguishesHashChangedFromPending(t *testing.T) {
	home, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	session.trust("1")

	edited := strings.Replace(twoHookConfig, `argv = ["./gate.sh"]`, `argv = ["./gate.sh", "--yolo"]`, 1)
	if err := os.WriteFile(filepath.Join(home, ".mivia", "mivia.toml"), []byte(edited), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	reloaded, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.decisions[0].Status != hooks.StatusHashChanged {
		t.Fatalf("an edited confirmed hook must read as hash-changed, got %q", reloaded.decisions[0].Status)
	}
	if len(reloaded.runnable()) != 0 {
		t.Fatal("an edited hook must not run until re-confirmed")
	}
	out := renderHookList(reloaded)
	if !strings.Contains(out, string(hooks.StatusHashChanged)) {
		t.Fatalf("listing must show hash-changed distinctly; got:\n%s", out)
	}
}

func TestHooksTrustRejectsAnUnknownNumber(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	for _, arg := range []string{"0", "3", "-1", "abc", ""} {
		msg := session.trust(arg)
		if !strings.Contains(msg, "1-2") && !strings.Contains(strings.ToLower(msg), "usage") {
			t.Errorf("trust %q must explain the valid range, got %q", arg, msg)
		}
		if len(session.runnable()) != 0 {
			t.Fatalf("trust %q promoted something", arg)
		}
	}
}

// Workspace hooks are not loaded at all, and the reason is surfaced rather than
// left to look like a bug.
func TestHooksSessionWarnsAboutWorkspaceHooks(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	wsConfig := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.WriteFile(wsConfig, []byte(twoHookConfig), 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	if len(session.decisions) != 2 {
		t.Fatalf("workspace hooks must not be discovered; got %d", len(session.decisions))
	}
	if !strings.Contains(strings.Join(session.warnings, "\n"), wsConfig) {
		t.Fatalf("the ignored workspace file must be named in a warning, got %v", session.warnings)
	}
}

// A config that does not validate fails loudly rather than loading nothing and
// leaving the user to conclude hooks are broken.
func TestHooksSessionRejectsAnInvalidConfig(t *testing.T) {
	_, ws := hookHome(t, "[[hooks]]\nevent = \"SessionStart\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n")
	_, err := loadHookSession(ws)
	if err == nil {
		t.Fatal("an invalid hook config must fail loudly")
	}
	if !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("the error must explain the rejection, got %v", err)
	}
}

// A corrupt store means zero hooks, and the listing says why.
func TestHooksSessionFailsClosedOnACorruptStore(t *testing.T) {
	home, ws := hookHome(t, twoHookConfig)
	if err := os.WriteFile(filepath.Join(home, ".mivia", "hook-trust.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("a corrupt store must not stop startup: %v", err)
	}
	if len(session.runnable()) != 0 {
		t.Fatal("a corrupt store must yield zero hooks")
	}
	if !strings.Contains(strings.Join(session.warnings, "\n"), "trust store") {
		t.Fatalf("the corrupt store must be reported, got %v", session.warnings)
	}
}

func TestHooksTrustRefusesManagedHooks(t *testing.T) {
	if hooks.ManagedConfigPath() == "" {
		t.Skip("no managed provenance boundary on this platform")
	}
	session := &hookSession{
		decisions: []hooks.Decision{{
			Group:  hooks.Group{Event: hooks.EventStop, Source: hooks.ManagedConfigPath()},
			Tier:   hooks.TierManaged,
			Status: hooks.StatusActive,
		}},
		store: hooks.OpenStore(filepath.Join(t.TempDir(), "hook-trust.json")),
	}
	msg := session.trust("1")
	if !strings.Contains(strings.ToLower(msg), "operator") {
		t.Fatalf("managed hooks are operator-set and cannot be promoted here, got %q", msg)
	}
	if !strings.Contains(renderHookList(session), string(hooks.TierManaged)) {
		t.Fatalf("the listing must mark managed hooks as such:\n%s", renderHookList(session))
	}
}

func TestHooksListOnNoHooksSaysSo(t *testing.T) {
	_, ws := hookHome(t, "[provider]\nname = \"openai\"\n")
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	if out := renderHookList(session); !strings.Contains(strings.ToLower(out), "no lifecycle hooks") {
		t.Fatalf("an empty listing must say so, got %q", out)
	}
}

// /hooks must be reachable and discoverable on both surfaces. A command that
// exists but is not routed reports "unknown command", which is how a user
// concludes the trust gate has no UI at all.
func TestHooksSlashIsRoutedAndListed(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	previous := sessionHookState
	sessionHookState = session
	t.Cleanup(func() { sessionHookState = previous })

	handled, _, err := handleSlash("/hooks", nil, nil, true, nil)
	if err != nil {
		t.Fatalf("handleSlash: %v", err)
	}
	if !handled {
		t.Fatal("/hooks must be routed in the classic dispatcher")
	}

	var found bool
	for _, command := range builtInSlashCommands() {
		if command.Name == "/hooks" {
			found = true
			if command.Surface != slashSurfaceBoth {
				t.Errorf("/hooks must be offered on both surfaces, got %v", command.Surface)
			}
		}
	}
	if !found {
		t.Fatal("/hooks must appear in the slash catalog")
	}
}
