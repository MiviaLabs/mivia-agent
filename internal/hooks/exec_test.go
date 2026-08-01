package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook fixtures are POSIX shell scripts")
	}
}

// hookDir returns a directory standing in for the declaring config file's
// directory, which is what argv[0] resolves against.
func hookDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func script(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// group builds a parsed group whose Source sits in dir, so "./name" resolves
// there. Building it through Parse keeps the tests honest about defaults.
func group(t *testing.T, dir, body string) []Group {
	t.Helper()
	groups, err := Parse([]byte(body), filepath.Join(dir, "mivia.toml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return groups
}

func preToolUse(argv, extra string) string {
	return "[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = " + argv + "\n" + extra
}

func postToolUse(argv, extra string) string {
	return "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = " + argv + "\n" + extra
}

func runHooks(t *testing.T, dir string, groups []Group, payload Payload) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return Runner{WorkspaceRoot: dir}.Run(ctx, groups, payload)
}

func TestArgvResolvesAgainstConfigDirectory(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "gate.sh", "printf 'ran'\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./gate.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "run_command"})
	if out.Denied {
		t.Fatalf("want allow, got denied: %s", out.Reason)
	}
	if !strings.Contains(out.Context, "ran") {
		t.Fatalf("hook did not run; context=%q warnings=%v", out.Context, out.Warnings)
	}
}

// A bare name must NOT resolve through PATH. "true" exists on every POSIX PATH,
// so if PATH were consulted the hook would silently succeed and the operator
// would believe their script ran.
func TestBareProgramNameIsNotFoundViaPATH(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	groups := group(t, dir, preToolUse(`["true"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "run_command"})
	if !out.Denied {
		t.Fatal("a PreToolUse hook whose program does not resolve must not allow the call")
	}
	if !strings.Contains(out.Reason, "true") {
		t.Errorf("reason must name the unresolved program, got %q", out.Reason)
	}
}

func TestAbsoluteArgvIsAllowed(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "abs.sh", "exit 0\n")
	abs := filepath.Join(dir, "abs.sh")
	groups := group(t, hookDir(t), preToolUse(`["`+abs+`"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if out.Denied {
		t.Fatalf("absolute argv[0] must run: %s", out.Reason)
	}
}

// Nothing in argv is ever interpreted as syntax: there is no shell, no
// shellwords pass, and no interpolation.
func TestShellMetacharactersArriveAsLiteralArgv(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "dump.sh", "for a in \"$@\"; do printf '[%s]' \"$a\"; done\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./dump.sh", "; rm -rf /", "&& id", "$(id)"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	for _, want := range []string{"[; rm -rf /]", "[&& id]", "[$(id)]"} {
		if !strings.Contains(out.Context, want) {
			t.Errorf("argv element %s must arrive literally; got %q", want, out.Context)
		}
	}
}

// A tool-derived filename reaches the hook through the environment, which is
// never re-parsed as syntax.
func TestToolFilenameWithShellSyntaxDoesNotExecute(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	canary := filepath.Join(dir, "pwned")
	script(t, dir, "file.sh", "printf '%s' \"$MIVIA_FILE\"\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./file.sh"]`, ""))

	payload := Payload{Event: EventPreToolUse, Tool: "write_file", File: "x; touch " + canary}
	out := runHooks(t, dir, groups, payload)
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("shell syntax in MIVIA_FILE executed")
	}
	if !strings.Contains(out.Context, "touch "+canary) {
		t.Fatalf("MIVIA_FILE must arrive verbatim, got %q", out.Context)
	}
}

func TestStdinCarriesThePayload(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "echo.sh", "cat\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./echo.sh"]`, ""))

	payload := Payload{
		Event: EventPreToolUse, Tool: "run_command",
		Input:     json.RawMessage(`{"argv":["git","commit"]}`),
		SessionID: "sess-1", TurnID: "turn-2", ToolCallID: "call-3",
	}
	out := runHooks(t, dir, groups, payload)
	var got Payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.Context)), &got); err != nil {
		t.Fatalf("stdin was not a single JSON object: %v (%q)", err, out.Context)
	}
	if got.Event != EventPreToolUse || got.Tool != "run_command" || got.SessionID != "sess-1" ||
		got.TurnID != "turn-2" || got.ToolCallID != "call-3" {
		t.Fatalf("payload round-trip mismatch: %+v", got)
	}
	if string(got.Input) != `{"argv":["git","commit"]}` {
		t.Fatalf("input = %s", got.Input)
	}
}

func TestEnvCarriesMiviaVariables(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "env.sh", "printf '%s|%s|%s|%s' \"$MIVIA_HOOK_EVENT\" \"$MIVIA_TOOL\" \"$MIVIA_SESSION_ID\" \"$MIVIA_WORKSPACE_ROOT\"\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./env.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "grep", SessionID: "s9"})
	want := "PreToolUse|grep|s9|" + dir
	if strings.TrimSpace(out.Context) != want {
		t.Fatalf("env = %q, want %q", strings.TrimSpace(out.Context), want)
	}
}

func TestStructuredDenyBlocksWithReason(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "deny.sh", `cat <<'JSON'
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"commit uses a forbidden bypass flag"}}
JSON
exit 0
`)
	groups := group(t, dir, preToolUse(`["./deny.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "run_command"})
	if !out.Denied {
		t.Fatal("structured deny must block")
	}
	if !strings.Contains(out.Reason, "forbidden bypass flag") {
		t.Fatalf("reason must reach the caller, got %q", out.Reason)
	}
}

func TestStructuredAllowDoesNotBlock(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "allow.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
exit 0
`)
	groups := group(t, dir, preToolUse(`["./allow.sh"]`, ""))

	if out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"}); out.Denied {
		t.Fatalf("structured allow must not block: %s", out.Reason)
	}
}

// Only exit 0 parses stdout as JSON. At exit 2 the JSON is ignored and stderr
// is the reason, so a hook cannot block and return a contradictory body.
func TestExitTwoBlocksAndIgnoresStdoutJSON(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "block.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
printf 'policy forbids this argv' >&2
exit 2
`)
	groups := group(t, dir, preToolUse(`["./block.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "run_command"})
	if !out.Denied {
		t.Fatal("exit 2 must block")
	}
	if !strings.Contains(out.Reason, "policy forbids this argv") {
		t.Fatalf("stderr must be the reason, got %q", out.Reason)
	}
	if strings.Contains(out.Context, "permissionDecision") {
		t.Fatalf("stdout JSON must be ignored at exit 2, got context %q", out.Context)
	}
}

func TestOtherNonZeroExitDeniesOnPreToolUse(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "boom.sh", "printf 'script broke' >&2\nexit 7\n")
	groups := group(t, dir, preToolUse(`["./boom.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if !out.Denied {
		t.Fatal("a non-zero, non-2 exit on PreToolUse must deny — the hook did not produce a decision")
	}
	// For a reactive event the same exit is a warning, not a block.
	postGroups := group(t, dir, postToolUse(`["./boom.sh"]`, ""))
	out = runHooks(t, dir, postGroups, Payload{Event: EventPostToolUse, Tool: "x"})
	if out.Denied {
		t.Fatal("a non-zero, non-2 exit on a reactive event is a warning, not a block")
	}
	if len(out.Warnings) == 0 {
		t.Fatal("an unrecognised exit on a reactive event must produce an operator warning")
	}
}

// A hook that tried to make a decision mivia cannot honour must not have that
// attempt read as permission. Coercing an unknown decision onto the permissive
// branch is exactly the schema drift that made the first draft fail open.
func TestUnsupportedPermissionDecisionIsDeniedNotAllowed(t *testing.T) {
	requirePOSIX(t)
	for _, decision := range []string{"ask", "defer", "maybe"} {
		t.Run(decision, func(t *testing.T) {
			dir := hookDir(t)
			script(t, dir, "d.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"`+decision+`"}}'
exit 0
`)
			groups := group(t, dir, preToolUse(`["./d.sh"]`, ""))
			out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
			if !out.Denied {
				t.Fatalf("permissionDecision %q must deny, never allow", decision)
			}
			if !strings.Contains(out.Reason, decision) {
				t.Errorf("reason must name the unsupported decision, got %q", out.Reason)
			}
		})
	}
}

func TestUpdatedInputIsRejectedNotIgnored(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "rewrite.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"argv":["rm","-rf","/"]}}}'
exit 0
`)
	groups := group(t, dir, preToolUse(`["./rewrite.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "run_command"})
	if !out.Denied {
		t.Fatal("updatedInput must be rejected, not silently ignored while the call proceeds")
	}
	if !strings.Contains(out.Reason, "updatedInput") {
		t.Fatalf("reason must name updatedInput, got %q", out.Reason)
	}
}

// Decision-shaped stdout that does not parse is a warning plus exit-code
// semantics; it is never read as a decision.
func TestUnparseableDecisionJSONFallsBackToExitCode(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "bad.sh", "printf '{\"hookSpecificOutput\": '\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./bad.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if out.Denied {
		t.Fatal("unparseable stdout falls back to exit-code semantics, and exit 0 is allow")
	}
	if len(out.Warnings) == 0 {
		t.Fatal("unparseable stdout must warn")
	}
}

func TestPlainTextStdoutBecomesContext(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "note.sh", "printf 'gofmt rewrote 2 files'\nexit 0\n")
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./note.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if !strings.Contains(out.Context, "gofmt rewrote 2 files") {
		t.Fatalf("plain stdout must become context, got %q", out.Context)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("plain text is not a malformed decision and must not warn, got %v", out.Warnings)
	}
}

// A reactive event cannot block, however it exits or whatever it returns.
func TestPostToolUseCannotBlock(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "veto.sh", `printf '{"decision":"block","reason":"too late","additionalContext":"tests failed"}'
exit 2
`)
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./veto.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if out.Denied {
		t.Fatal("PostToolUse must never deny: the tool already ran")
	}
}

// A hung gate must not be an open gate.
func TestPreToolUseTimeoutBlocksByDefault(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "hang.sh", "sleep 30\n")
	groups := group(t, dir, preToolUse(`["./hang.sh"]`, "  timeout = 1\n"))

	start := time.Now()
	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if !out.Denied {
		t.Fatal("a timed-out PreToolUse hook must deny: hanging the gate must not disable it")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("timeout did not kill the process, took %v", elapsed)
	}
	if !strings.Contains(out.Reason, "timed out") {
		t.Fatalf("reason must say the hook timed out, got %q", out.Reason)
	}
}

func TestExplicitOnTimeoutAllowWarnsInsteadOfBlocking(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "hang.sh", "sleep 30\n")
	groups := group(t, dir, preToolUse(`["./hang.sh"]`, "  timeout = 1\n  on_timeout = \"allow\"\n"))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if out.Denied {
		t.Fatal("an explicit on_timeout = allow must not block")
	}
	if len(out.Warnings) == 0 {
		t.Fatal("a timed-out hook is reported, never silently dropped")
	}
}

// A handler that cannot start produced no verdict, exactly as a timed-out one
// did, so it resolves the same way rather than silently allowing.
func TestUnstartableHandlerUsesTheOnTimeoutVerdict(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	groups := group(t, dir, preToolUse(`["./absent.sh"]`, "  on_timeout = \"allow\"\n"))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if out.Denied {
		t.Fatal("on_timeout = allow must apply to an unstartable handler too")
	}
	if len(out.Warnings) == 0 {
		t.Fatal("an unstartable handler must be reported")
	}
}

func TestOversizedStdoutIsTruncatedWithNotice(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "loud.sh", "i=0\nwhile [ $i -lt 4000 ]; do printf '0123456789'; i=$((i+1)); done\nexit 0\n")
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./loud.sh\"]\n  timeout = 30\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "x"})
	if out.Context == "" {
		t.Fatal("oversized stdout must be truncated, not dropped")
	}
	if len(out.Context) > MaxOutputBytes+256 {
		t.Fatalf("context = %d bytes, must stay within the bound plus its notice", len(out.Context))
	}
	if !strings.Contains(out.Context, "truncated") {
		t.Fatalf("truncation must be announced, got tail %q", out.Context[max(0, len(out.Context)-120):])
	}
}

// Truncation must not cut a rune in half. Hook context and block reasons are
// model-visible text, and a trailing partial rune is invalid UTF-8 in a
// payload the provider has to encode.
func TestTruncationNeverSplitsARune(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "utf8.sh", "i=0\nwhile [ $i -lt 4000 ]; do printf 'ありがとう'; i=$((i+1)); done\nexit 0\n")
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./utf8.sh\"]\n  timeout = 30\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "x"})
	if !utf8.ValidString(out.Context) {
		t.Fatalf("truncated hook context is not valid UTF-8 (%d bytes)", len(out.Context))
	}
}

func TestBlockReasonNeverSplitsARune(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "deny.sh", "i=0\nwhile [ $i -lt 3000 ]; do printf 'ありがとう' >&2; i=$((i+1)); done\nexit 2\n")
	groups := group(t, dir, preToolUse(`["./deny.sh"]`, "  timeout = 30\n"))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if !out.Denied {
		t.Fatal("want deny")
	}
	if !utf8.ValidString(out.Reason) {
		t.Fatalf("truncated block reason is not valid UTF-8 (%d bytes)", len(out.Reason))
	}
}

// A block reason reaches the model. It names the hook program, and the name is
// enough: the absolute path runs through the user's home directory, and the
// model has no use for it. Operator warnings keep the full path.
func TestModelVisibleReasonDoesNotCarryTheFilesystemPath(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "gate.sh", "exit 2\n")
	groups := group(t, dir, preToolUse(`["./gate.sh"]`, ""))

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if !out.Denied {
		t.Fatal("want deny")
	}
	if strings.Contains(out.Reason, dir) {
		t.Errorf("block reason must not carry the hook's absolute path, got %q", out.Reason)
	}
	if !strings.Contains(out.Reason, "gate.sh") {
		t.Errorf("block reason must still name the hook, got %q", out.Reason)
	}
}

func TestOperatorWarningKeepsTheFullPath(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "boom.sh", "exit 7\n")
	postGroups := group(t, dir, postToolUse(`["./boom.sh"]`, ""))

	// On a reactive event, an unrecognised exit code is an operator warning
	// that carries the full path so the operator can find the file.
	out := runHooks(t, dir, postGroups, Payload{Event: EventPostToolUse, Tool: "x"})
	if len(out.Warnings) == 0 {
		t.Fatal("an unrecognised exit must produce an operator warning")
	}
	warnings := strings.Join(out.Warnings, "\n")
	if !strings.Contains(warnings, filepath.Join(dir, "boom.sh")) {
		t.Fatalf("an operator warning must name the exact file that failed, got %v", warnings)
	}
}

// An already-canceled context must be detected and reported. A hook that
// silently never executed is indistinguishable from one that allowed.
func TestCanceledContextIsReportedNotSilentlySkipped(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "gate.sh", "exit 0\n")
	groups := group(t, dir, preToolUse(`["./gate.sh"]`, ""))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := Runner{WorkspaceRoot: dir}.Run(ctx, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if !out.Denied && len(out.Warnings) == 0 {
		t.Fatal("a hook that could not run on a canceled context must be reported")
	}
}

func TestMatcherSelectsWhichGroupsRun(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "hit.sh", "printf 'hit'\nexit 0\n")
	body := "[[hooks]]\nevent = \"PreToolUse\"\nmatcher = \"^run_command$\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./hit.sh\"]\n"
	groups := group(t, dir, body)

	if out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "write_file"}); out.Context != "" {
		t.Fatalf("a non-matching tool must not run the hook, got %q", out.Context)
	}
	if out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "run_command"}); !strings.Contains(out.Context, "hit") {
		t.Fatalf("a matching tool must run the hook, got %q", out.Context)
	}
}

func TestEventSelectsWhichGroupsRun(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "pre.sh", "printf 'pre'\nexit 0\n")
	groups := group(t, dir, preToolUse(`["./pre.sh"]`, ""))

	if out := runHooks(t, dir, groups, Payload{Event: EventStop}); out.Context != "" {
		t.Fatalf("a PreToolUse group must not fire on Stop, got %q", out.Context)
	}
}

// A gate that already denied must not keep running handlers: the call is not
// happening, and the remaining scripts have side effects.
func TestPreToolUseShortCircuitsOnFirstDeny(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	marker := filepath.Join(dir, "second-ran")
	script(t, dir, "deny.sh", "printf 'nope' >&2\nexit 2\n")
	script(t, dir, "second.sh", "touch "+marker+"\nexit 0\n")
	body := "[[hooks]]\nevent = \"PreToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./deny.sh\"]\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./second.sh\"]\n"
	groups := group(t, dir, body)

	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if !out.Denied {
		t.Fatal("want deny")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("handlers after a deny must not run")
	}
}

// Reactive handlers all run: each has its own side effect and none can veto.
func TestPostToolUseRunsEveryHandler(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "a.sh", "printf 'A'\nexit 1\n")
	script(t, dir, "b.sh", "printf 'B'\nexit 0\n")
	body := "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./a.sh\"]\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./b.sh\"]\n"
	groups := group(t, dir, body)

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "x"})
	if !strings.Contains(out.Context, "B") {
		t.Fatalf("a failing reactive handler must not stop the next one, got %q", out.Context)
	}
}
