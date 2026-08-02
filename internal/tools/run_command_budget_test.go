package tools

import "testing"

// TestRunCommandUncappedDeclaresMemoryBackstop pins the skeptic fix: when
// max_output_bytes is 0, ResultBudgetBytes must not return 0 (which collapses
// the dispatcher ceiling to the floor and destroys honest multi-MB capture).
func TestRunCommandUncappedDeclaresMemoryBackstop(t *testing.T) {
	reg := NewDefaultRegistry(DefaultOptions{
		RunAllowlist:   []string{"echo"},
		MaxOutputBytes: 0,
	})
	tool, ok := reg.Get("run_command")
	if !ok {
		t.Fatal("run_command missing")
	}
	budgeted, ok := tool.(ResultBudgetTool)
	if !ok {
		t.Fatal("run_command must implement ResultBudgetTool")
	}
	got := budgeted.ResultBudgetBytes()
	if got != defaultMemoryBackstopBytes {
		t.Fatalf("uncapped ResultBudgetBytes = %d, want memory backstop %d", got, defaultMemoryBackstopBytes)
	}
}

func TestRunCommandBoundedDeclaresMaxOut(t *testing.T) {
	const bound = 50000
	reg := NewDefaultRegistry(DefaultOptions{
		RunAllowlist:   []string{"echo"},
		MaxOutputBytes: bound,
	})
	tool, ok := reg.Get("run_command")
	if !ok {
		t.Fatal("run_command missing")
	}
	got := tool.(ResultBudgetTool).ResultBudgetBytes()
	if got != bound {
		t.Fatalf("bounded ResultBudgetBytes = %d, want %d", got, bound)
	}
}

func TestRunCommandHonorsCustomMemoryBackstop(t *testing.T) {
	const custom = 64 << 20
	reg := NewDefaultRegistry(DefaultOptions{
		RunAllowlist:        []string{"echo"},
		MaxOutputBytes:      0,
		MemoryBackstopBytes: custom,
	})
	got := mustGet(t, reg, "run_command").(ResultBudgetTool).ResultBudgetBytes()
	if got != custom {
		t.Fatalf("ResultBudgetBytes = %d, want custom backstop %d", got, custom)
	}
}

func mustGet(t *testing.T, reg *Registry, name string) Tool {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tool
}
