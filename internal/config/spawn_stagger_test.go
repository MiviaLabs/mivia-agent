package config

import (
	"testing"
)

func writeSubagentsTOML(t *testing.T, sub string) string {
	t.Helper()
	return writeMinimalConfig(t, "[subagents]\n"+sub+"\n")
}

// TestSpawnStaggerExplicitZeroPreserved pins that an explicit
// spawn_stagger_ms = 0 disables staggering and survives resolution - it must
// not be replaced by the 150ms default.
func TestSpawnStaggerExplicitZeroPreserved(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeSubagentsTOML(t, "spawn_stagger_ms = 0")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.SpawnStaggerMs != 0 {
		t.Fatalf("explicit spawn_stagger_ms = 0 resolved to %d, want 0 (disabled)", res.Subagents.SpawnStaggerMs)
	}
}

// TestSpawnStaggerAbsentFallsBackToDefault is the negative guard: an absent
// key takes the compiled default so anti-thundering-herd protection is on
// without operator action.
func TestSpawnStaggerAbsentFallsBackToDefault(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeSubagentsTOML(t, "schema_retry_max = 2")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.SpawnStaggerMs != defaultSpawnStaggerMs {
		t.Fatalf("absent spawn_stagger_ms resolved to %d, want default %d", res.Subagents.SpawnStaggerMs, defaultSpawnStaggerMs)
	}
}

// TestSpawnStaggerClampedAtMax pins the typo guard: a value above
// maxSpawnStaggerMs cannot serialize every batch behind seconds-long gaps.
func TestSpawnStaggerClampedAtMax(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeSubagentsTOML(t, "spawn_stagger_ms = 60000")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.SpawnStaggerMs != maxSpawnStaggerMs {
		t.Fatalf("spawn_stagger_ms = 60000 resolved to %d, want clamp %d", res.Subagents.SpawnStaggerMs, maxSpawnStaggerMs)
	}
}
