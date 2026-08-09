package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A per-call timeout_seconds ABOVE the static [tools] run_timeout_seconds cap
// must extend past the cap up to the caller's step/run grant (the parent
// context deadline), so a legitimate long command (e.g. a full coverage run)
// is not killed at the static cap. Without a parent deadline the static cap
// remains the ceiling, and the parent deadline bounds the per-call arg when
// the grant is tighter.
func TestRunCommandPerCallTimeoutExtendsPastCapWithParentGrant(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}
	// Tool cap 2s, per-call timeout_seconds=10, parent grant 30s. The
	// per-call arg wins up to the parent deadline: 'sleep 4; echo done'
	// runs past the 2s cap and completes with exit=0. Pre-fix it was
	// clamped to the 2s cap and died with exit=timeout.
	out, _ := runTimeoutScenario(t, runTimeoutCase{capSec: 2, parentSec: 30, argv: "sleep 4; echo done", timeoutSeconds: 10})
	if strings.Contains(out, "exit=timeout") {
		t.Fatalf("per-call 10s was clamped to the 2s static cap: %q", out)
	}
	if !strings.Contains(out, "done") || !strings.Contains(out, "exit=0") {
		t.Fatalf("expected 'done' exit=0, got %q", out)
	}
}

// TestRunCommandParentDeadlineBoundsPerCallTimeout: requested (60s) exceeds
// the parent grant (5s): the parent deadline is the ceiling, so 'sleep 10' is
// killed at ~5s, not allowed to run the full 10s.
func TestRunCommandParentDeadlineBoundsPerCallTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}
	out, elapsed := runTimeoutScenario(t, runTimeoutCase{capSec: 2, parentSec: 5, argv: "sleep 10", timeoutSeconds: 60})
	if !strings.Contains(out, "exit=timeout") {
		t.Fatalf("expected exit=timeout at the parent grant, got %q", out)
	}
	if elapsed > 9*time.Second {
		t.Fatalf("parent grant not bounding, took %s", elapsed)
	}
}

// TestRunCommandNoParentDeadlineStaticCapCeiling: no parent grant, so even
// timeout_seconds=10 must not extend past the 2s static cap — 'sleep 10;
// echo done' is killed at ~2s.
func TestRunCommandNoParentDeadlineStaticCapCeiling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}
	out, elapsed := runTimeoutScenario(t, runTimeoutCase{capSec: 2, parentSec: 0, argv: "sleep 10; echo done", timeoutSeconds: 10})
	if !strings.Contains(out, "exit=timeout") {
		t.Fatalf("expected exit=timeout (static cap 2s is the ceiling without a parent grant), got %q", out)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("static cap not applied, took %s", elapsed)
	}
}

// TestRunCommandTighterPerCallArgWins: tool cap 30s, per-call timeout_seconds
// 2, parent grant 30s. The tighter per-call arg still wins over both.
func TestRunCommandTighterPerCallArgWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}
	out, elapsed := runTimeoutScenario(t, runTimeoutCase{capSec: 30, parentSec: 30, argv: "sleep 10", timeoutSeconds: 2})
	if !strings.Contains(out, "exit=timeout") {
		t.Fatalf("expected exit=timeout (per-call 2s honored), got %q", out)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("per-call timeout not applied, took %s", elapsed)
	}
}

// runTimeoutCase describes one per-call timeout scenario.
type runTimeoutCase struct {
	capSec         int
	parentSec      int // 0 = no parent deadline
	argv           string
	timeoutSeconds int
}

// runTimeoutScenario executes the run_command tool for a scenario and returns
// the result body and the wall-clock elapsed time.
func runTimeoutScenario(t *testing.T, c runTimeoutCase) (string, time.Duration) {
	t.Helper()
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: c.capSec})
	ctx := context.Background()
	var cancel context.CancelFunc
	if c.parentSec > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(c.parentSec)*time.Second)
		defer cancel()
	}
	start := time.Now()
	body := fmt.Sprintf(`{"argv":["sh","-c",%q],"timeout_seconds":%d}`, c.argv, c.timeoutSeconds)
	out, err := reg.Execute(ctx, "run_command", json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	return out, time.Since(start)
}

// TestCallContextClampsToAbsoluteCeiling: the 24h absolute ceiling cannot be
// exercised end-to-end (that would sleep a day), so pin it directly on
// callContext: a per-call request beyond 24h is clamped even when the parent
// grant is far longer.
func TestCallContextClampsToAbsoluteCeiling(t *testing.T) {
	tool := &runCommandTool{timeoutSec: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 30*24*time.Hour)
	defer cancel()
	callCtx, callCancel := tool.callContext(ctx, int(25*time.Hour/time.Second))
	if callCancel == nil {
		t.Fatal("expected a cancel func: the clamped request (24h) is tighter than the parent grant")
	}
	defer callCancel()
	if d, ok := callCtx.Deadline(); !ok {
		t.Fatal("expected a deadline")
	} else if until := time.Until(d); until > 25*time.Hour || until < 23*time.Hour {
		t.Fatalf("request not clamped to the 24h absolute ceiling: %s", until)
	}
}

// TestRunCommandHugeTimeoutSecondsClampedBeforeMultiply pins the overflow
// guard on the per-call timeout. A model-supplied timeout_seconds value too
// large for time.Duration (>= maxDurationSeconds+1, about 292 years) wraps
// negative when multiplied by time.Second, and the wrapped value bypasses the
// `timeout > absoluteCeiling` clamp that sits after the multiply - the
// command then runs under an already-expired context and dies before it
// starts. The agent loop clamps its own copy of the arg (requestedToolTimeout
// in internal/agent), so the tool must clamp the raw value the same way or
// the two disagree on the very same call (DC-6/DC-7: a huge model-supplied
// timeout_seconds must never wrap time.Duration negative).
func TestRunCommandHugeTimeoutSecondsClampedBeforeMultiply(t *testing.T) {
	tool := &runCommandTool{timeoutSec: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 30*24*time.Hour)
	defer cancel()

	cases := []struct {
		name        string
		secs        int
		wantCeiling bool // true expects the 24h absolute ceiling, false the static 2s cap
	}{
		{"zero means no override", 0, false},
		{"negative means no override", -1, false},
		{"max representable seconds", int(maxDurationSeconds), true},
		// Red case: one second past the representable range overflowed the
		// multiply and armed an already-expired context.
		{"one past representable seconds", int(maxDurationSeconds + 1), true},
		{"max int seconds", int(^uint64(0) >> 1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callCtx, callCancel := tool.callContext(ctx, tc.secs)
			if callCancel == nil {
				t.Fatal("expected a cancel func")
			}
			defer callCancel()
			if err := callCtx.Err(); err != nil {
				t.Fatalf("timeout_seconds=%d armed an already-expired context (%v); the command would die before running", tc.secs, err)
			}
			d, ok := callCtx.Deadline()
			if !ok {
				t.Fatal("expected a deadline")
			}
			until := time.Until(d)
			if tc.wantCeiling {
				if until > 25*time.Hour || until < 23*time.Hour {
					t.Fatalf("timeout_seconds=%d not clamped to the 24h absolute ceiling: %s", tc.secs, until)
				}
			} else if until > 3*time.Second || until <= 0 {
				t.Fatalf("timeout_seconds=%d should keep the static 2s cap, got %s", tc.secs, until)
			}
		})
	}
}

// TestRunCommandHugeTimeoutSecondsCommandStillRuns is the end-to-end shape of
// the same defect: with a huge (but syntactically valid) timeout_seconds the
// command must still run to completion under the clamped ceiling. Before the
// fix the overflowed context killed the command instantly, so the result
// never carried the command's output.
func TestRunCommandHugeTimeoutSecondsCommandStillRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh path")
	}
	out, _ := runTimeoutScenario(t, runTimeoutCase{
		capSec: 2, parentSec: 30, argv: "echo done", timeoutSeconds: int(maxDurationSeconds + 1),
	})
	// runTimeoutScenario fatals on an Execute error internally, so the
	// duration slot is deliberately discarded here (same shape as the other
	// end-to-end case in this file).
	if !strings.Contains(out, "done") || !strings.Contains(out, "exit=0") {
		t.Fatalf("expected 'done' exit=0 under the clamp, got %q", out)
	}
}
