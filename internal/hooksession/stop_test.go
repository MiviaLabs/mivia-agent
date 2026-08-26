package hooksession

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// stopSession installs a session whose only hook is a Stop hook running the
// given shell body.
func stopSession(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	scriptName, scriptBody := "stop.sh", "#!/bin/sh\n"+body
	if runtime.GOOS == "windows" {
		// Windows cannot execute POSIX shell scripts; run the same fixture
		// as a cmd.exe batch file so the Stop hook behavior is exercised on
		// the supported platform contract.
		scriptName, scriptBody = "stop.cmd", windowsStopHookBody(body)
	}
	script := filepath.Join(dir, scriptName)
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	groups, err := hooks.Parse([]byte("[[hooks]]\nevent = \"Stop\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./"+scriptName+"\"]\n"),
		filepath.Join(dir, "mivia.toml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	session := &Session{groups: groups}
	restore := SetForTest(session)
	t.Cleanup(restore)
	return dir
}

// windowsStopHookBody translates the POSIX stop-hook fixture bodies to batch
// syntax so the same behaviors are exercised on Windows: a printf+exit body
// becomes an echo+exit /b body, and the bounded-output loop becomes a batch
// for /l loop that writes 40,000 characters without a newline.
func windowsStopHookBody(body string) string {
	if strings.HasPrefix(body, "i=0\nwhile") {
		return "@echo off\r\nfor /l %%i in (1,1,4000) do @<nul set /p \"=0123456789\"\r\nexit /b 0\r\n"
	}
	trimmed := strings.TrimSuffix(body, "\n")
	if strings.HasPrefix(trimmed, "printf '") {
		if marker := strings.LastIndex(trimmed, "\nexit "); marker >= 0 {
			text := strings.TrimSuffix(trimmed[len("printf '"):marker], "'")
			exitCode := trimmed[marker+len("\nexit "):]
			return "@echo off\r\necho " + text + "\r\nexit /b " + exitCode + "\r\n"
		}
	}
	return "@echo off\r\n" + strings.ReplaceAll(trimmed, "\n", "\r\n") + "\r\n"
}

func TestStopHookOutputBecomesAnAttributedContinuationPrompt(t *testing.T) {
	dir := stopSession(t, "printf 'turn cost: 1420 tokens'\nexit 0\n")
	got := RunStopEvent(context.Background(), dir, "sess-1", "turn-1")
	if !strings.Contains(got, "turn cost: 1420 tokens") {
		t.Fatalf("Stop hook output = %q", got)
	}
}

// Stop is pure observation. A non-zero exit or a decision:"block" warns and the
// turn still ends: a Stop hook can log a turn's cost, never affect whether the
// turn ended.
func TestStopHookCannotBlockTheTurn(t *testing.T) {
	dir := stopSession(t, "printf '{\"decision\":\"block\",\"reason\":\"no\"}'\nexit 2\n")
	// The contract is that this returns without denial, and that the hook
	// actually ran (produced output or warnings). A test that only checks for
	// the absence of "denied" would also pass if the hook were silently skipped.
	got := RunStopEvent(context.Background(), dir, "s", "t")

	session := Current()
	session.mu.Lock()
	warnings := strings.Join(session.runWarnings, "\n")
	session.mu.Unlock()
	if got == "" && warnings == "" {
		t.Fatal("the Stop hook must have run; empty context and no warnings means it was skipped")
	}
	if strings.Contains(got, "denied") {
		t.Fatalf("Stop must have no denial path, got %q", got)
	}
}

func TestStopHookOutputIsBounded(t *testing.T) {
	dir := stopSession(t, "i=0\nwhile [ $i -lt 4000 ]; do printf '0123456789'; i=$((i+1)); done\nexit 0\n")
	got := RunStopEvent(context.Background(), dir, "s", "t")
	if len(got) > hooks.MaxOutputBytes+256 {
		t.Fatalf("Stop output = %d bytes, past the bound", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("truncation must be announced")
	}
}

// A canceled turn does not silently skip its Stop hook; the run is recorded so
// /hooks can say it did not happen.
func TestStopHookOnACanceledTurnIsRecordedNotSilent(t *testing.T) {
	dir := stopSession(t, "printf 'x'\nexit 0\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunStopEvent(ctx, dir, "s", "t")

	session := Current()
	session.mu.Lock()
	warnings := strings.Join(session.runWarnings, "\n")
	session.mu.Unlock()
	if warnings == "" {
		t.Fatal("a Stop hook that could not run on a canceled turn must be recorded")
	}
}

func TestStopHookWithNoConfiguredHooksReturnsNothing(t *testing.T) {
	restore := SetForTest(nil)
	t.Cleanup(restore)
	if got := RunStopEvent(context.Background(), t.TempDir(), "s", "t"); got != "" {
		t.Fatalf("no hooks configured, got %q", got)
	}
}

// Stop means "the assistant is done", which is true once per user-visible turn.
// A Stop hook that fired per subagent turn would run N times and its semantics
// would be false every time but the last. Today RunStopEvent has no production
// caller at all (see its doc comment) - this test only guards that the event
// constant itself is named in exactly one file, so a future wiring attempt
// cannot accidentally duplicate the call site while adding one.
func TestStopEventIsFiredFromExactlyOneProductionSite(t *testing.T) {
	var sites []string
	for _, dir := range []string{".", "../cli", "../agent", "../subagents", "../runtime", "../coordinator"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if strings.Contains(string(data), "hooks.EventStop") {
				sites = append(sites, filepath.Join(dir, name))
			}
		}
	}
	if len(sites) != 1 {
		t.Fatalf("hooks.EventStop must be named by exactly one production file (the root turn path); found %v", sites)
	}
}

// RunStopEvent has no production caller anywhere today (see its doc
// comment) - a wider scan than the constant-uniqueness check above, since
// internal/sdkadapter already references the hooks.EventStop CONSTANT
// (isReactive's comparison, not a firing site) without calling the
// function, and that reference would wrongly trip the narrower check if
// its directory were added there. This assertion will need updating the
// day a real call site is wired in; it exists so that day is a deliberate,
// visible edit here, not a silent gap the way the original "TUI only" claim
// was.
func TestRunStopEventHasNoProductionCallerYet(t *testing.T) {
	var callers []string
	for _, dir := range []string{".", "../cli", "../agent", "../subagents", "../runtime", "../coordinator", "../chat", "../clichat", "../uiadapter", "../sdkadapter"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if dir == "." && name == "stop.go" {
				continue // the definition itself
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if strings.Contains(string(data), "RunStopEvent(") {
				callers = append(callers, filepath.Join(dir, name))
			}
		}
	}
	if len(callers) != 0 {
		t.Fatalf("RunStopEvent is documented as having no production caller; found %v - update this test and the doc comments together", callers)
	}
}
