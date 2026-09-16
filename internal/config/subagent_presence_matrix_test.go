package config

import (
	"testing"
)

// The contract for probeSubagentPresence: the single raw-byte probe that
// records which presence-sensitive [subagents] keys each config layer actually
// declared. Two keys share the probe, and each keeps its own resolution rule:
//   - inline_output_bytes: explicit 0 means "always use refs", no upper clamp.
//   - spawn_stagger_ms: explicit 0 means "staggering disabled", clamped at
//     maxSpawnStaggerMs.
//
// Every row below asserts BOTH resolved values, including the field the row
// does not touch. That is what makes the single-key rows an independence proof
// rather than two unrelated single-key tests: a probe that collapsed the flags
// into one "the [subagents] table was present" flag would still pass a test
// that only checked the key it set.

// presenceCase is one base/overlay pair and the pair of values it must resolve
// to. Both string fields are bare keys - loadSubagentPresence adds the headers,
// which are asymmetric between the two layers.
type presenceCase struct {
	name        string
	base        string
	overlay     string
	wantInline  int
	wantStagger int
}

// loadSubagentPresence writes a base config plus a distinct workspace overlay
// and returns the resolved (inline_output_bytes, spawn_stagger_ms) pair.
//
// The two fixtures take their input differently, and getting it backwards makes
// rows pass vacuously: writeMinimalConfig appends after a [chat] table, so a
// base row needs its own [subagents] header; writeWorkspaceOverlayConfig already
// emits [subagents] plus store_backend/store_path and appends under it, so an
// overlay row must be bare keys. An empty string means the layer declares
// neither key - never write it as an explicit 0.
func loadSubagentPresence(t *testing.T, base, overlay string) (int, int) {
	t.Helper()
	if base != "" {
		base = "\n[subagents]\n" + base
	}
	res, err := Load(LoadOptions{
		ConfigPath:    writeMinimalConfig(t, base),
		WorkspaceRoot: overlayRoot(t, overlay),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Subagents.InlineOutputBytes, res.Subagents.SpawnStaggerMs
}

func overlayRoot(t *testing.T, overlay string) string {
	t.Helper()
	root := t.TempDir()
	writeWorkspaceOverlayConfig(t, root, overlay)
	return root
}

func runPresenceCases(t *testing.T, cases []presenceCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			inline, stagger := loadSubagentPresence(t, tt.base, tt.overlay)
			if inline != tt.wantInline {
				t.Errorf("inline_output_bytes = %d, want %d", inline, tt.wantInline)
			}
			if stagger != tt.wantStagger {
				t.Errorf("spawn_stagger_ms = %d, want %d", stagger, tt.wantStagger)
			}
		})
	}
}

// TestSubagentPresenceOneLayerSpeaks covers the rows where at most one layer
// declares anything. These pin that presence survives a silent overlay - the
// regression the whole mechanism exists for - and that each key's own
// resolution rule still applies.
func TestSubagentPresenceOneLayerSpeaks(t *testing.T) {
	runPresenceCases(t, []presenceCase{{
		name:       "both absent everywhere take their defaults",
		wantInline: defaultInlineOutputBytes, wantStagger: defaultSpawnStaggerMs,
	}, {
		// Before the flag was monotonic, the overlay's ABSENCE reset the base's
		// presence and the operator's ref-only mode reverted to 4096.
		name:       "base explicit inline 0 survives a silent overlay",
		base:       "inline_output_bytes = 0\n",
		wantInline: 0, wantStagger: defaultSpawnStaggerMs,
	}, {
		// The same contract for stagger, which had no overlay coverage at all
		// before the two probes were merged.
		name:       "base explicit stagger 0 survives a silent overlay",
		base:       "spawn_stagger_ms = 0\n",
		wantInline: defaultInlineOutputBytes, wantStagger: 0,
	}, {
		name:       "base declares both explicit zeros",
		base:       "inline_output_bytes = 0\nspawn_stagger_ms = 0\n",
		wantInline: 0, wantStagger: 0,
	}, {
		name:       "base explicit positive inline survives a silent overlay",
		base:       "inline_output_bytes = 100\n",
		wantInline: 100, wantStagger: defaultSpawnStaggerMs,
	}, {
		name:       "base explicit positive stagger survives a silent overlay",
		base:       "spawn_stagger_ms = 500\n",
		wantInline: defaultInlineOutputBytes, wantStagger: 500,
	}, {
		// Same out-of-range number on both keys: stagger clamps, inline has no
		// upper bound and passes through. Pins that the clamp did not follow
		// the merge onto the wrong key.
		name:       "the stagger clamp did not follow the merge onto inline",
		base:       "inline_output_bytes = 60000\nspawn_stagger_ms = 60000\n",
		wantInline: 60000, wantStagger: maxSpawnStaggerMs,
	}, {
		name:       "overlay-only explicit inline 0 wins over an absent base",
		overlay:    "inline_output_bytes = 0\n",
		wantInline: 0, wantStagger: defaultSpawnStaggerMs,
	}, {
		name:       "overlay-only explicit stagger 0 wins over an absent base",
		overlay:    "spawn_stagger_ms = 0\n",
		wantInline: defaultInlineOutputBytes, wantStagger: 0,
	}})
}

// TestSubagentPresenceBothLayersSpeak covers the rows where both layers declare
// something. These separate the two halves of the contract that are easiest to
// conflate: absence is non-destructive, but an explicit later key still wins on
// value, and a layer naming one key must leave the other key's presence alone.
func TestSubagentPresenceBothLayersSpeak(t *testing.T) {
	runPresenceCases(t, []presenceCase{{
		// Independence: the overlay declaring stagger must neither re-assert
		// nor erase inline's presence.
		name: "base names only inline, overlay names only stagger",
		base: "inline_output_bytes = 0\n", overlay: "spawn_stagger_ms = 0\n",
		wantInline: 0, wantStagger: 0,
	}, {
		// The mirror of the row above: order must not matter.
		name: "base names only stagger, overlay names only inline",
		base: "spawn_stagger_ms = 0\n", overlay: "inline_output_bytes = 0\n",
		wantInline: 0, wantStagger: 0,
	}, {
		name: "overlay wins on value where both layers name inline",
		base: "inline_output_bytes = 0\n", overlay: "inline_output_bytes = 100\n",
		wantInline: 100, wantStagger: defaultSpawnStaggerMs,
	}, {
		name: "overlay wins on value where both layers name stagger",
		base: "spawn_stagger_ms = 0\n", overlay: "spawn_stagger_ms = 500\n",
		wantInline: defaultInlineOutputBytes, wantStagger: 500,
	}})
}
