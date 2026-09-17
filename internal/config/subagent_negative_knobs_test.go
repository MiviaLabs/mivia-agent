package config

import (
	"strings"
	"testing"
)

// TestNegativeSubagentKnobsRejected pins that the two [subagents] knobs with no
// defined negative meaning fail the load instead of being silently
// reinterpreted.
//
// Neither was a reachable defect: BelowInlineThreshold treats any threshold
// <= 0 as "always use refs", and the dispatch loop only staggers while the
// duration is positive, so a negative already behaved exactly like an explicit
// 0. That equivalence is what makes it worth rejecting - `spawn_stagger_ms =
// -150` quietly means "staggering disabled" when the operator meant 150ms, and
// no layer reports it. The load is the only place that still knows what was
// written.
func TestNegativeSubagentKnobsRejected(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantMsg string
	}{
		{
			name:    "negative inline_output_bytes",
			toml:    "inline_output_bytes = -1",
			wantMsg: "inline_output_bytes must not be negative",
		},
		{
			name:    "negative spawn_stagger_ms",
			toml:    "spawn_stagger_ms = -150",
			wantMsg: "spawn_stagger_ms must not be negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(LoadOptions{ConfigPath: writeSubagentsTOML(t, tt.toml)})
			if err == nil {
				t.Fatalf("%s loaded without error, want rejection", tt.toml)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

// TestNegativeSubagentKnobsRejectedThroughOverlay pins that the check sees the
// MERGED value, not the base file's. A workspace overlay is the layer most
// likely to carry a hand-edited typo, and it wins on value, so a check placed
// before the merge would miss exactly the case that matters most.
func TestNegativeSubagentKnobsRejectedThroughOverlay(t *testing.T) {
	base := writeMinimalConfig(t, "\n[subagents]\nspawn_stagger_ms = 150\n")
	root := t.TempDir()
	writeWorkspaceOverlayConfig(t, root, "spawn_stagger_ms = -150\n")

	_, err := Load(LoadOptions{ConfigPath: base, WorkspaceRoot: root})
	if err == nil {
		t.Fatal("overlay spawn_stagger_ms = -150 loaded without error, want rejection")
	}
	if !strings.Contains(err.Error(), "spawn_stagger_ms must not be negative") {
		t.Fatalf("error = %q, want it to name spawn_stagger_ms", err)
	}
}

// TestNonNegativeSubagentKnobsStillLoad is the negative guard for the guard:
// the values either side of the new boundary must be unaffected. Zero is a real
// configuration for both keys and must not be swept up by a `<= 0` slip.
func TestNonNegativeSubagentKnobsStillLoad(t *testing.T) {
	tests := []struct {
		name        string
		toml        string
		wantInline  int
		wantStagger int
	}{
		{"both explicit zero", "inline_output_bytes = 0\nspawn_stagger_ms = 0", 0, 0},
		{"both explicit positive", "inline_output_bytes = 100\nspawn_stagger_ms = 500", 100, 500},
		{"both absent", "schema_retry_max = 2", defaultInlineOutputBytes, defaultSpawnStaggerMs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Load(LoadOptions{ConfigPath: writeSubagentsTOML(t, tt.toml)})
			if err != nil {
				t.Fatal(err)
			}
			if res.Subagents.InlineOutputBytes != tt.wantInline {
				t.Errorf("inline_output_bytes = %d, want %d", res.Subagents.InlineOutputBytes, tt.wantInline)
			}
			if res.Subagents.SpawnStaggerMs != tt.wantStagger {
				t.Errorf("spawn_stagger_ms = %d, want %d", res.Subagents.SpawnStaggerMs, tt.wantStagger)
			}
		})
	}
}

// TestNegativeTotalTimeoutStillAccepted guards the blast radius of the new
// check. default_total_timeout_seconds documents negative as a REAL value - an
// explicit operator opt-out of the last-resort termination bound - so a blanket
// "no negatives under [subagents]" rule would break a supported configuration.
// This fails the moment someone generalizes rejectNegativeSubagentKnobs.
func TestNegativeTotalTimeoutStillAccepted(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeSubagentsTOML(t, "default_total_timeout_seconds = -1")})
	if err != nil {
		t.Fatalf("default_total_timeout_seconds = -1 must stay loadable (documented opt-out): %v", err)
	}
	if res.Subagents.DefaultTotalTimeoutSec != -1 {
		t.Fatalf("default_total_timeout_seconds resolved to %d, want -1 preserved", res.Subagents.DefaultTotalTimeoutSec)
	}
}
