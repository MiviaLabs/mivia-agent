package tools

import "testing"

func TestEffectiveMemoryBackstopDefaultAndOverride(t *testing.T) {
	if got := effectiveMemoryBackstop(DefaultOptions{}); got != defaultMemoryBackstopBytes {
		t.Fatalf("default backstop = %d, want %d", got, defaultMemoryBackstopBytes)
	}
	if got := effectiveMemoryBackstop(DefaultOptions{MemoryBackstopBytes: 0}); got != defaultMemoryBackstopBytes {
		t.Fatalf("0 backstop = %d, want default", got)
	}
	const custom = 64 << 20
	if got := effectiveMemoryBackstop(DefaultOptions{MemoryBackstopBytes: custom}); got != custom {
		t.Fatalf("custom backstop = %d, want %d", got, custom)
	}
}

func TestReadClassBudgetsHonorMemoryBackstop(t *testing.T) {
	const custom = 32 << 20
	readMax, classMax := readClassBudgets(DefaultOptions{MemoryBackstopBytes: custom})
	if readMax != custom || classMax != custom {
		t.Fatalf("readClassBudgets = %d,%d want both %d", readMax, classMax, custom)
	}
	// Explicit MaxReadBytes still wins over backstop.
	readMax, classMax = readClassBudgets(DefaultOptions{MaxReadBytes: 1 << 20, MemoryBackstopBytes: custom})
	if readMax != 1<<20 || classMax != 1<<20 {
		t.Fatalf("explicit MaxReadBytes lost: %d,%d", readMax, classMax)
	}
}

// TestReadClassBudgetsPreClampsWhenMaxReadUncapped pins that MaxToolResultBytes
// still clamps read budgets when MaxReadBytes is 0 (uncapped). Resolving the
// memory backstop first, then clamping, avoids min(0, cap) collapsing to 0 and
// then replacing with the full backstop (ignoring the result cap).
func TestReadClassBudgetsPreClampsWhenMaxReadUncapped(t *testing.T) {
	const capBytes = 4096
	readMax, classMax := readClassBudgets(DefaultOptions{
		MaxReadBytes:       0,
		MaxToolResultBytes: capBytes,
	})
	if classMax != capBytes {
		t.Fatalf("readClassMax = %d, want MaxToolResultBytes %d", classMax, capBytes)
	}
	wantReadMax := capBytes - readResultReserve
	if readMax != wantReadMax {
		t.Fatalf("readMax = %d, want MaxToolResultBytes-readResultReserve %d", readMax, wantReadMax)
	}
}

func TestEditToolGuardHonorsMemoryBackstop(t *testing.T) {
	const custom = 48 << 20
	// registerEditTools is private; exercise via registry construction.
	reg := NewDefaultRegistry(DefaultOptions{MemoryBackstopBytes: custom})
	tool, ok := reg.Get("search_replace")
	if !ok {
		t.Fatal("search_replace missing")
	}
	sr, ok := tool.(*searchReplaceTool)
	if !ok {
		t.Fatalf("type %T", tool)
	}
	if sr.maxFileBytes != custom {
		t.Fatalf("edit maxFileBytes = %d, want backstop %d", sr.maxFileBytes, custom)
	}
}
