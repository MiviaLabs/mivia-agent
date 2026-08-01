package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// hookHome points HOME at a fresh directory holding a user config, which is the
// only place a hook can come from.
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

func loadHooksIn(t *testing.T, ws string) *hookSession {
	t.Helper()
	session, err := loadHookSession(ws)
	if err != nil {
		t.Fatalf("loadHookSession: %v", err)
	}
	return session
}

// Declaring a hook is the decision. There is no second step, and a session that
// discovered two hooks runs two hooks.
func TestEveryConfiguredHookRunsWithoutConfirmation(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session := loadHooksIn(t, ws)

	if len(session.groups) != 2 {
		t.Fatalf("want 2 hooks discovered, got %d", len(session.groups))
	}
	if got := len(session.runnable()); got != 2 {
		t.Fatalf("want 2 runnable hooks, got %d: a configured hook runs", got)
	}
	out := renderHookList(session)
	for _, want := range []string{"PreToolUse", "PostToolUse", "run_command", "gate.sh", "fmt.sh"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing must mention %q; got:\n%s", want, out)
		}
	}
}

// Running programs unprompted is only defensible if the session says which
// programs. The notice is what replaces the confirmation, so its absence would
// leave the change with neither a prompt nor a disclosure.
func TestStartupNamesEveryArmedHook(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session := loadHooksIn(t, ws)

	notice := strings.Join(session.armedNotice(), "\n")
	for _, want := range []string{"gate.sh", "fmt.sh", "PreToolUse", "PostToolUse"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the armed notice must name %q; got %q", want, notice)
		}
	}
}

func TestNoHooksConfiguredAnnouncesNothing(t *testing.T) {
	_, ws := hookHome(t, "[provider]\nname = \"openai\"\n")
	session := loadHooksIn(t, ws)

	if got := session.armedNotice(); len(got) != 0 {
		t.Fatalf("a session with no hooks must stay quiet, got %v", got)
	}
}

// The resolved timeout and on_timeout are displayed, not the blank the author
// left: an operator reading the list must see what actually applies.
func TestHooksListShowsResolvedTimeoutAndVerdict(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	out := renderHookList(loadHooksIn(t, ws))

	if !strings.Contains(out, "on_timeout=block") {
		t.Errorf("PreToolUse resolves to on_timeout=block and must say so; got:\n%s", out)
	}
	if !strings.Contains(out, "on_timeout=allow") {
		t.Errorf("PostToolUse resolves to on_timeout=allow and must say so; got:\n%s", out)
	}
	if !strings.Contains(out, "timeout=10s") {
		t.Errorf("resolved timeouts must be shown; got:\n%s", out)
	}
}

// With no confirmation step, the listing is the only place the scope is ever
// stated - and the thing most likely to be assumed wrongly is that mivia
// watches the script file.
func TestHooksListStatesTheScriptBodyIsNotTracked(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	out := renderHookList(loadHooksIn(t, ws))
	lower := strings.ToLower(out)

	if !strings.Contains(lower, "argv[0]") || !strings.Contains(lower, "does not track") {
		t.Fatalf("the listing must state that the script's contents are not tracked; got:\n%s", out)
	}
}

// The subcommand was real and will be in muscle memory. "unknown argument"
// would read as a broken listing rather than as a removed concept.
func TestHooksTrustExplainsThatConfirmationIsGone(t *testing.T) {
	out := strings.ToLower(hooksSlashOutput([]string{"/hooks", "trust", "1"}))
	if !strings.Contains(out, "removed") {
		t.Fatalf("/hooks trust must say the concept is gone, got %q", out)
	}
	if !strings.Contains(out, "mivia.toml") {
		t.Fatalf("/hooks trust must point at the file that arms a hook, got %q", out)
	}
}

// Workspace hooks are not loaded at all, and the reason is surfaced rather than
// left to look like a bug. This restriction is what makes unprompted execution
// defensible, so it is asserted here and not only in the config package.
func TestHooksSessionWarnsAboutWorkspaceHooks(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	wsConfig := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.WriteFile(wsConfig, []byte(twoHookConfig), 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	session := loadHooksIn(t, ws)

	if len(session.groups) != 2 {
		t.Fatalf("workspace hooks must not be discovered; got %d", len(session.groups))
	}
	if !strings.Contains(strings.Join(session.warnings, "\n"), wsConfig) {
		t.Fatalf("the ignored workspace file must be named in a warning, got %v", session.warnings)
	}
}

// A config that does not validate fails loudly rather than loading nothing and
// leaving the user to conclude hooks are broken.
func TestHooksSessionRejectsAnInvalidConfig(t *testing.T) {
	_, ws := hookHome(t, "[[hooks]]\nevent = \"SessionStart\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./h.sh\"]\n")
	if _, err := loadHookSession(ws); err == nil {
		t.Fatal("an invalid hook config must fail loudly")
	} else if !strings.Contains(err.Error(), "deferred") {
		t.Fatalf("the error must explain the rejection, got %v", err)
	}
}

func TestHooksListOnNoHooksSaysSo(t *testing.T) {
	_, ws := hookHome(t, "[provider]\nname = \"openai\"\n")
	if out := renderHookList(loadHooksIn(t, ws)); !strings.Contains(strings.ToLower(out), "no lifecycle hooks") {
		t.Fatalf("an empty listing must say so, got %q", out)
	}
}

// A stale --bypass-hook-trust in a CI config must not fail the run, and must
// not be silently swallowed either: the operator needs to know it stopped
// meaning anything.
func TestStaleBypassFlagIsAcceptedAndReported(t *testing.T) {
	noTools, plainUI, staleBypass, rest := chatFlags([]string{"--bypass-hook-trust", "keep"})
	if !staleBypass {
		t.Fatal("the removed flag must still parse rather than land in rest")
	}
	if noTools || plainUI {
		t.Fatal("the removed flag must not set unrelated flags")
	}
	if len(rest) != 1 || rest[0] != "keep" {
		t.Fatalf("other arguments must survive, got %v", rest)
	}
}

// /hooks must be reachable and discoverable on both surfaces. A command that
// exists but is not routed reports "unknown command", which is how a user
// concludes hooks have no UI at all.
func TestHooksSlashIsRoutedAndListed(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session := loadHooksIn(t, ws)
	previous := sessionHookState.Load()
	sessionHookState.Store(session)
	t.Cleanup(func() { sessionHookState.Store(previous) })

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
