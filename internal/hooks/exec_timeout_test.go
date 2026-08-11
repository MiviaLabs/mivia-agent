package hooks

import (
	"strings"
	"testing"
	"time"
)

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

// A hook whose deadline fires in the same instant its verdict lands must not
// have that verdict discarded as a timeout. The grandchild below holds the
// stdout pipe open, so cmd.Wait stays blocked while the deadline fires and the
// group kill EOFs the pipe; startAndWait then returns the parent's genuine
// exit status (nil, exit 0), and the allow it already printed must be honored.
// Before the fix, execute() re-derived "timed out" from callCtx.Err() alone and
// turned the allow into a spurious block (DC-7/DC-9).
func TestVerdictNearDeadlineIsNotDiscardedAsTimeout(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "slow-allow.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
sleep 30 &
exit 0
`)
	groups := group(t, dir, preToolUse(`["./slow-allow.sh"]`, "  timeout = 1\n"))

	start := time.Now()
	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("deadline did not reclaim the run, took %v", elapsed)
	}
	if out.Denied {
		t.Fatalf("the hook's allow must be honored even though the deadline fired: %s", out.Reason)
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
