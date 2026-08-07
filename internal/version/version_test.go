package version

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBinaryName(t *testing.T) {
	if Binary != "mivia" {
		t.Fatalf("Binary = %q, want mivia", Binary)
	}
	if Product != "mivia" {
		t.Fatalf("Product = %q, want mivia", Product)
	}
	if Version == "" {
		t.Fatal("Version must be non-empty")
	}
}

// TestVersionStringIncludesCommit asserts that when provenance is injected at
// link time (Commit + Dirty set via -ldflags), the rendered --version line
// carries the commit and the working-tree state.
func TestVersionStringIncludesCommit(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })

	Commit = "abc1234"
	Dirty = "dirty"
	if got, want := String(), "mivia 0.0.0-dev (commit abc1234, dirty)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	Dirty = "clean"
	if got, want := String(), "mivia 0.0.0-dev (commit abc1234, clean)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	// A real commit with no dirty marker injected should still degrade.
	Dirty = ""
	if got, want := String(), "mivia 0.0.0-dev (commit abc1234)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// TestVersionStringFallback asserts the plain `go build` path (no ldflags):
// Commit is still "unknown" and the line degrades to "mivia <version>".
func TestVersionStringFallback(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })

	Commit = "unknown"
	Dirty = ""
	if got, want := String(), "mivia 0.0.0-dev"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	Commit = ""
	Dirty = ""
	if got, want := String(), "mivia 0.0.0-dev"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// TestJSONStringFullProvenance: commit + dirty both present.
func TestJSONStringFullProvenance(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })

	Commit = "abc1234"
	Dirty = "dirty"
	got := JSONString()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("JSONString() not valid JSON: %v: %q", err, got)
	}
	if m["binary"] != "mivia" {
		t.Fatalf("binary = %v, want mivia", m["binary"])
	}
	if m["version"] != Version {
		t.Fatalf("version = %v, want %q", m["version"], Version)
	}
	if m["commit"] != "abc1234" {
		t.Fatalf("commit = %v, want abc1234", m["commit"])
	}
	if m["dirty"] != "dirty" {
		t.Fatalf("dirty = %v, want dirty", m["dirty"])
	}
}

// TestJSONStringCleanProvenance: commit present, dirty empty.
func TestJSONStringCleanProvenance(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })

	Commit = "abc1234"
	Dirty = ""
	got := JSONString()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("JSONString() not valid JSON: %v: %q", err, got)
	}
	if _, ok := m["commit"]; !ok {
		t.Fatal("commit key missing")
	}
	if _, ok := m["dirty"]; ok {
		t.Fatal("dirty key present but should be omitted")
	}
}

// TestJSONStringCommitUnknown: commit "unknown" → both commit and dirty omitted.
func TestJSONStringCommitUnknown(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })

	Commit = "unknown"
	Dirty = ""
	got := JSONString()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("JSONString() not valid JSON: %v: %q", err, got)
	}
	if _, ok := m["commit"]; ok {
		t.Fatal("commit key present but should be omitted for unknown")
	}
	if _, ok := m["dirty"]; ok {
		t.Fatal("dirty key present but should be omitted for unknown")
	}
}

// TestJSONStringCommitEmpty: commit "" → both commit and dirty omitted.
func TestJSONStringCommitEmpty(t *testing.T) {
	oldCommit, oldDirty := Commit, Dirty
	t.Cleanup(func() { Commit, Dirty = oldCommit, oldDirty })

	Commit = ""
	Dirty = ""
	got := JSONString()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("JSONString() not valid JSON: %v: %q", err, got)
	}
	if _, ok := m["commit"]; ok {
		t.Fatal("commit key present but should be omitted for empty")
	}
	if _, ok := m["dirty"]; ok {
		t.Fatal("dirty key present but should be omitted for empty")
	}
}

// TestJSONStringIsCompactNoNewline: JSONString output has no trailing newline.
func TestJSONStringIsCompactNoNewline(t *testing.T) {
	got := JSONString()
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("JSONString() ends with newline: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("JSONString() contains newline: %q", got)
	}
}
