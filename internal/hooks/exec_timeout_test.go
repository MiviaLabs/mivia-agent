package hooks

import (
	"os/exec"
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

// os/exec returns ErrWaitDelay when the process exits with a successful status
// but orphaned descendants kept the output pipes open past WaitDelay. That
// error is not an *exec.ExitError, so execute() used to classify it as a start
// failure, discarding the captured stdout and turning a hook's allow into a
// spurious PreToolUse block. The verdict the hook printed before exit is real
// and must be honored. The elapsed >= 1s assertion proves the WaitDelay path
// actually fired: a grandchild that failed to hold the pipe would let Wait
// return immediately, and the test would pass vacuously. Fails before the fix:
// the ErrWaitDelay was a spurious deny via 'could not start'.
func TestWaitDelayVerdictIsHonoredNotDiscardedAsStartFailure(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "orphan-hold.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
sleep 30 &
exit 0
`)
	groups := group(t, dir, preToolUse(`["./orphan-hold.sh"]`, "  timeout = 30\n"))

	start := time.Now()
	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if elapsed := time.Since(start); elapsed < 1*time.Second {
		t.Fatalf("the WaitDelay path did not fire (elapsed %v): the grandchild must hold the stdout pipe across the WaitDelay window", elapsed)
	}
	if out.Denied {
		t.Fatalf("an allow printed before exit must be honored even though orphaned descendants held the pipe: %s", out.Reason)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("a clean allow must not warn, got %v", out.Warnings)
	}
}

// os/exec returns ErrWaitDelay under an expired deadline when the process
// exited 0 but a DETACHED descendant (escaping the group kill via setsid) held
// the output pipe open past WaitDelay. That is a successful exit whose verdict
// was captured before the cut, so the deadline must not reclassify it as a
// timeout: before the fix, callCtx.Err() != nil re-derived "timed out" for
// every non-ExitError runErr and turned the hook's allow into a spurious
// PreToolUse deny (hooks-verdict-at-deadline-denied). The elapsed >= 1.5s
// assertion proves the WaitDelay path fired: a grandchild that failed to
// detach would be killed by the group kill, EOF the pipe, and let Wait return
// the exit-0 status near the 1s deadline, passing vacuously.
func TestErrWaitDelayAtDeadlineIsNotReclassifiedAsTimeout(t *testing.T) {
	requirePOSIX(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid unavailable; cannot detach a pipe-holding grandchild")
	}
	dir := hookDir(t)
	script(t, dir, "detached-holder.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
setsid sleep 30 &
exit 0
`)
	groups := group(t, dir, preToolUse(`["./detached-holder.sh"]`, "  timeout = 1\n"))

	start := time.Now()
	out := runHooks(t, dir, groups, Payload{Event: EventPreToolUse, Tool: "x"})
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Fatalf("the WaitDelay path did not fire (elapsed %v): the detached grandchild must hold the stdout pipe past WaitDelay", elapsed)
	}
	if out.Denied {
		t.Fatalf("the exit-0 allow must be honored even though the deadline fired: %s", out.Reason)
	}
	if len(out.Warnings) != 0 {
		t.Fatalf("a clean allow must not warn, got %v", out.Warnings)
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
