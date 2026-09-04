package clichat

// chat_command_startup_errors_test.go covers the rejection and notice branches
// of chat_command_startup.go: every per-flag parse-error return in
// parseChatInvocation, the trailing-positional rejection, the full-disk
// warning, and both --json invocation refusals.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// isZeroInvocation reports whether inv is the zero chatInvocation. The struct
// carries slices, so it is not comparable with ==.
func isZeroInvocation(inv chatInvocation) bool {
	return reflect.DeepEqual(inv, chatInvocation{})
}

// TestParseChatInvocationSurfacesEveryFlagParseError pins that a value-taking
// chat flag written without its value fails closed, naming the offending flag,
// and returns the ZERO invocation. A branch that swallowed the seam error and
// carried on would start a session under partially parsed flags - e.g. a
// dropped --deny-program silently widening the run allowlist.
func TestParseChatInvocationSurfacesEveryFlagParseError(t *testing.T) {
	for _, flag := range []string{
		"--model", "--config", "--workspace", "--agent",
		"--allow-program", "--deny-program", "--disable-tool",
		"--allow-env-var", "--deny-env-var",
	} {
		t.Run(flag, func(t *testing.T) {
			inv, err := parseChatInvocation([]string{flag})
			if err == nil {
				t.Fatalf("parseChatInvocation([%s]) accepted a value-less flag", flag)
			}
			if !strings.Contains(err.Error(), flag) {
				t.Fatalf("err = %q, want it to name %s", err.Error(), flag)
			}
			if !isZeroInvocation(inv) {
				t.Fatalf("invocation = %+v, want the zero value on a parse error", inv)
			}
		})
	}
}

// TestParseChatInvocationRejectsUnexpectedPositionals pins that leftover
// positional arguments are refused rather than ignored: an operator who typed
// a prompt without -p must be told, not dropped into an empty REPL.
func TestParseChatInvocationRejectsUnexpectedPositionals(t *testing.T) {
	inv, err := parseChatInvocation([]string{"--quiet", "explain", "this"})
	if err == nil {
		t.Fatal("parseChatInvocation accepted trailing positional arguments")
	}
	if !strings.HasPrefix(err.Error(), "chat: unexpected arguments: ") {
		t.Fatalf("err = %q, want the unexpected-arguments prefix", err.Error())
	}
	for _, want := range []string{"explain", "this"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to list %q", err.Error(), want)
		}
	}
	if !isZeroInvocation(inv) {
		t.Fatalf("invocation = %+v, want the zero value", inv)
	}
	// The same flags without positionals parse, so the rejection is real.
	if _, err := parseChatInvocation([]string{"--quiet"}); err != nil {
		t.Fatalf("parseChatInvocation([--quiet]) = %v, want nil", err)
	}
}

// TestPrepareChatStartupFullDiskWarns pins the operator-visible notice for
// --full-disk: lifting workspace confinement must never be silent. --quiet
// suppresses it (it is an informational startup notice), so both directions
// are asserted.
func TestPrepareChatStartupFullDiskWarns(t *testing.T) {
	const want = "workspace: FULL DISK ACCESS"

	stop := captureStderr(t)
	useTools, err := prepareChatStartup(keyedResolved(), chatInvocation{fullDisk: true, quiet: true})
	if err != nil {
		t.Fatalf("prepareChatStartup: %v", err)
	}
	if !useTools {
		t.Fatal("useTools = false, want true when --no-tools was not passed")
	}
	loud := stop()
	if strings.Contains(loud, want) {
		t.Fatalf("--quiet still printed the full-disk notice: %q", loud)
	}

	stop = captureStderr(t)
	if _, err := prepareChatStartup(keyedResolved(), chatInvocation{fullDisk: true}); err != nil {
		t.Fatalf("prepareChatStartup: %v", err)
	}
	got := stop()
	if !strings.Contains(got, want) {
		t.Fatalf("stderr = %q, want it to contain %q", got, want)
	}
	if !strings.Contains(got, "file tools are not confined to the workspace") {
		t.Fatalf("stderr = %q, want it to say the tools are unconfined", got)
	}

	// Without --full-disk the notice must not appear at all.
	stop = captureStderr(t)
	if _, err := prepareChatStartup(keyedResolved(), chatInvocation{}); err != nil {
		t.Fatalf("prepareChatStartup: %v", err)
	}
	none := stop()
	if strings.Contains(none, want) {
		t.Fatalf("full-disk notice printed without --full-disk: %q", none)
	}
}

// TestPrepareChatStartupUserConfigFullDiskWarns pins the notice for the
// OTHER provenance (audit required-fix #2): a full-disk grant persisted in
// the operator's USER config must print the same loud startup notice - the
// disclosure keys off the EFFECTIVE grant, never off which source granted
// it. A workspace config carrying the key must NOT trip it: the TUI-
// persisted setting is user-config-only, so this also catches anyone
// re-wiring the read onto the merged/workspace config.
func TestPrepareChatStartupUserConfigFullDiskWarns(t *testing.T) {
	const want = "workspace: FULL DISK ACCESS"

	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stop := captureStderr(t)
	if _, err := prepareChatStartup(keyedResolved(), chatInvocation{workspacePath: t.TempDir()}); err != nil {
		t.Fatalf("prepareChatStartup: %v", err)
	}
	got := stop()
	if !strings.Contains(got, want) {
		t.Fatalf("stderr = %q, want the notice for a user-config full-disk grant", got)
	}

	// --quiet suppresses the informational notice for this source too.
	stop = captureStderr(t)
	if _, err := prepareChatStartup(keyedResolved(), chatInvocation{workspacePath: t.TempDir(), quiet: true}); err != nil {
		t.Fatalf("prepareChatStartup: %v", err)
	}
	if quiet := stop(); strings.Contains(quiet, want) {
		t.Fatalf("--quiet still printed the full-disk notice: %q", quiet)
	}

	// No grant anywhere: no notice.
	t.Setenv("HOME", t.TempDir())
	stop = captureStderr(t)
	if _, err := prepareChatStartup(keyedResolved(), chatInvocation{workspacePath: t.TempDir()}); err != nil {
		t.Fatalf("prepareChatStartup: %v", err)
	}
	if none := stop(); strings.Contains(none, want) {
		t.Fatalf("full-disk notice printed with no grant: %q", none)
	}
}

// TestPrepareChatStartupRequiresAPIKey pins the API-key gate: an unset key
// fails closed and names the environment variable the operator must set.
func TestPrepareChatStartupRequiresAPIKey(t *testing.T) {
	res := &config.Resolved{APIKeyEnv: "MIVIA_TEST_KEY"}
	useTools, err := prepareChatStartup(res, chatInvocation{quiet: true})
	if err == nil {
		t.Fatal("prepareChatStartup accepted a missing API key")
	}
	if useTools {
		t.Fatal("useTools = true on the failure path")
	}
	if !strings.Contains(err.Error(), "missing API key") || !strings.Contains(err.Error(), "MIVIA_TEST_KEY") {
		t.Fatalf("err = %q, want it to name the missing key env", err.Error())
	}
}

// TestValidateJSONModeInvocationRejectsOneShotPrompt pins that --json is
// refused for -p/--prompt: one-shot mode never reaches the line-mode REPL that
// produces the NDJSON stream, so accepting it would emit raw text under a
// contract that promises framed events.
func TestValidateJSONModeInvocationRejectsOneShotPrompt(t *testing.T) {
	err := validateJSONModeInvocation(chatInvocation{jsonMode: true, prompt: "hello"})
	if err == nil {
		t.Fatal("validateJSONModeInvocation accepted --json with -p")
	}
	if !strings.Contains(err.Error(), "--json is not supported with -p/--prompt") {
		t.Fatalf("err = %q, want the one-shot refusal", err.Error())
	}
}

// TestValidateJSONModeInvocationRejectsInteractiveTerminal pins the second
// --json refusal: when stdin is a real terminal the REPL/TUI writes prompts
// and rendered UI to stdout, which would be indistinguishable from the NDJSON
// stream. The refusal must name the pipe-stdin remedy.
func TestValidateJSONModeInvocationRejectsInteractiveTerminal(t *testing.T) {
	withPtyStdin(t, func() {
		err := validateJSONModeInvocation(chatInvocation{jsonMode: true})
		if err == nil {
			t.Fatal("validateJSONModeInvocation accepted --json with a terminal stdin")
		}
		if !strings.Contains(err.Error(), "--json is not supported for the interactive REPL/TUI") {
			t.Fatalf("err = %q, want the interactive refusal", err.Error())
		}
		if !strings.Contains(err.Error(), "pipe input via stdin") {
			t.Fatalf("err = %q, want it to name the pipe-stdin remedy", err.Error())
		}
	})
	// With a non-terminal stdin (the test binary's own piped stdin) the same
	// invocation is accepted, so the refusal above is terminal-conditional.
	if err := validateJSONModeInvocation(chatInvocation{jsonMode: true}); err != nil {
		t.Fatalf("validateJSONModeInvocation(non-tty) = %v, want nil", err)
	}
}

// keyedResolved is a Resolved that clears prepareChatStartup's API-key gate.
func keyedResolved() *config.Resolved {
	return &config.Resolved{APIKeySet: true, APIKey: "sk-test", APIKeyEnv: "MIVIA_TEST_KEY"}
}
