package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestUpdateGeneralConfig_SyncPointerWritesOnlyNonNil_AndPreservesSiblings
// pins the load-bearing contract for the three [sync] opt-out switches:
// UpdateGeneralConfig must ONLY write the keys whose *bool is non-nil in
// the request, and must leave every sibling under [sync] (enabled, api_url,
// poll_wait_seconds, max_unflushed) byte-identical. Without this, a
// general theme edit would materialise include_thinking = true into a file
// that was previously left at the documented "absent == ON" state, silently
// converting a default install into an explicit setting.
func TestUpdateGeneralConfig_SyncPointerWritesOnlyNonNil_AndPreservesSiblings(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "mivia.toml")
	initial := `[sync]
enabled = true
api_url = "https://api.example.com"
poll_wait_seconds = 25
max_unflushed = 5000

[tui]
theme = "mivia-dark"
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	view := GeneralSettings{
		Theme: "mivia-dark",
		// Only SyncIncludeThinking is touched.
		SyncIncludeThinking: ptrBool(false),
	}
	if err := UpdateGeneralConfig(path, view); err != nil {
		t.Fatalf("UpdateGeneralConfig: %v", err)
	}

	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal written config: %v", err)
	}
	syncMap, ok := raw["sync"].(map[string]any)
	if !ok {
		t.Fatalf("missing [sync] table in written file:\n%s", string(data))
	}

	if v, ok := syncMap["include_thinking"]; !ok || v != false {
		t.Errorf("include_thinking = %v (present=%v), want false present", v, ok)
	}
	if _, ok := syncMap["include_tool_io"]; ok {
		t.Errorf("include_tool_io unexpectedly materialised (must only be written when pointer non-nil):\n%s", string(data))
	}
	if _, ok := syncMap["stream_assistant"]; ok {
		t.Errorf("stream_assistant unexpectedly materialised:\n%s", string(data))
	}
	if v, ok := syncMap["enabled"]; !ok || v != true {
		t.Errorf("enabled sibling changed: %v (present=%v), want true present", v, ok)
	}
	if v, ok := syncMap["api_url"]; !ok || v != "https://api.example.com" {
		t.Errorf("api_url sibling changed: %v (present=%v), want https://api.example.com", v, ok)
	}
	if v, ok := syncMap["poll_wait_seconds"]; !ok || v != int64(25) {
		t.Errorf("poll_wait_seconds sibling changed: %v (present=%v), want 25", v, ok)
	}
	if v, ok := syncMap["max_unflushed"]; !ok || v != int64(5000) {
		t.Errorf("max_unflushed sibling changed: %v (present=%v), want 5000", v, ok)
	}
}

// TestUpdateGeneralConfig_SyncAbsentThenTrueRoundtripsAsTrue: a file
// without include_thinking, written with SyncIncludeThinking = ptr(true),
// must materialise `include_thinking = true` AND the resolved config must
// agree. Deleting the key (the absence path) would also read as ON under
// sync.go's three-state rule, but the operator explicitly asked for ON,
// so the file must reflect that with a real key — never an absent one —
// otherwise a later editor that strips blanks could erase the operator's
// intent without their consent.
func TestUpdateGeneralConfig_SyncAbsentThenTrueRoundtripsAsTrue(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(path, []byte("[tui]\ntheme = \"mivia-dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	view := GeneralSettings{
		Theme: "mivia-dark",
		// Three pointers explicitly ON.
		SyncIncludeThinking: ptrBool(true),
		SyncIncludeToolIO:   ptrBool(true),
		SyncStreamAssistant: ptrBool(true),
	}
	if err := UpdateGeneralConfig(path, view); err != nil {
		t.Fatalf("UpdateGeneralConfig: %v", err)
	}

	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	syncMap, ok := raw["sync"].(map[string]any)
	if !ok {
		t.Fatalf("missing [sync] table:\n%s", string(data))
	}
	for _, key := range []string{"include_thinking", "include_tool_io", "stream_assistant"} {
		v, present := syncMap[key]
		if !present {
			t.Errorf("%s absent from file; expected explicit true:\n%s", key, string(data))
		}
		if v != true {
			t.Errorf("%s = %v, want true (absent must round-trip to explicit true)", key, v)
		}
	}

	// Reload through the real resolver: ResolvedSync.Include* must agree.
	resolved := ResolveSyncConfig(SyncConfig{
		IncludeThinking: ptrBoolFromAny(syncMap["include_thinking"]),
		IncludeToolIO:   ptrBoolFromAny(syncMap["include_tool_io"]),
		StreamAssistant: ptrBoolFromAny(syncMap["stream_assistant"]),
	})
	if !resolved.IncludeThinking {
		t.Errorf("ResolvedSync.IncludeThinking = false, want true after explicit true write")
	}
	if !resolved.IncludeToolIO {
		t.Errorf("ResolvedSync.IncludeToolIO = false, want true after explicit true write")
	}
	if !resolved.StreamAssistant {
		t.Errorf("ResolvedSync.StreamAssistant = false, want true after explicit true write")
	}
}

// TestUpdateGeneralConfig_SyncToggleOffThenOnWritesTrue: an operator who
// toggles a sync key off, then back on, must end with the key PRESENT and
// set to true — not absent. The "absent == ON" rule at the file layer
// means the round-trip would be correct either way at resolve time, but
// the explicit-true form is the only one that faithfully represents the
// operator's last action and survives a cosmetic file rewriter.
func TestUpdateGeneralConfig_SyncToggleOffThenOnWritesTrue(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	// First write: off.
	if err := UpdateGeneralConfig(path, GeneralSettings{SyncIncludeThinking: ptrBool(false)}); err != nil {
		t.Fatalf("UpdateGeneralConfig (off): %v", err)
	}
	// Second write: on.
	if err := UpdateGeneralConfig(path, GeneralSettings{SyncIncludeThinking: ptrBool(true)}); err != nil {
		t.Fatalf("UpdateGeneralConfig (on): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	syncMap, ok := raw["sync"].(map[string]any)
	if !ok {
		t.Fatalf("missing [sync] table:\n%s", string(data))
	}
	v, present := syncMap["include_thinking"]
	if !present {
		t.Fatalf("include_thinking absent after toggle-back-on; want present=true:\n%s", string(data))
	}
	if v != true {
		t.Errorf("include_thinking = %v after toggle-back-on, want true", v)
	}
}

// TestUpdateGeneralConfig_SyncAllNilLeavesFileUnchanged: when no sync key
// is touched (every *bool nil), UpdateGeneralConfig must NOT create a
// [sync] table in a file that had none. The TUI theme edit must never
// materialise a sync stanza.
func TestUpdateGeneralConfig_SyncAllNilLeavesFileUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(path, []byte("[tui]\ntheme = \"mivia-dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	view := GeneralSettings{Theme: "solarized"}
	if err := UpdateGeneralConfig(path, view); err != nil {
		t.Fatalf("UpdateGeneralConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["sync"]; ok {
		t.Errorf("[sync] table materialised by a non-sync edit:\n%s", string(data))
	}
}

// TestUpdateGeneralConfig_SyncConcurrentWritersSerialise drives N goroutines
// hammering UpdateGeneralConfig with differing sync keys against the same
// path. Under -race, every key in the final file must be present with a
// deterministic value AND the file must remain parseable TOML.
//
// The locking is owned by updateConfigFile/persistFileLocks; this test is
// the contract that the lock holds across the new sync branch too.
func TestUpdateGeneralConfig_SyncConcurrentWritersSerialise(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			view := GeneralSettings{
				SyncIncludeThinking: ptrBool(i%2 == 0),
				SyncIncludeToolIO:   ptrBool(i%2 == 1),
				SyncStreamAssistant: ptrBool(i%3 == 0),
			}
			if err := UpdateGeneralConfig(path, view); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("final file failed to parse under contention:\n%s\nerr: %v", string(data), err)
	}
	syncMap, ok := raw["sync"].(map[string]any)
	if !ok {
		t.Fatalf("missing [sync] after concurrent writes:\n%s", string(data))
	}
	for _, key := range []string{"include_thinking", "include_tool_io", "stream_assistant"} {
		if _, ok := syncMap[key]; !ok {
			t.Errorf("key %q missing after concurrent writes; locked write did not serialise:\n%s", key, string(data))
		}
	}
}

// ptrBool is a tiny helper: *bool literal is awkward in struct literals
// because Go's type-inference does not infer the pointer target type from
// a value context.
func ptrBool(b bool) *bool { return &b }

// ptrBoolFromAny mirrors SyncConfig's pointer semantics for an arbitrary
// decoded value (go-toml decodes booleans as plain bool, never as a
// pointer). nil for non-boolean values, otherwise a pointer to the
// underlying bool.
func ptrBoolFromAny(v any) *bool {
	if v == nil {
		return nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
