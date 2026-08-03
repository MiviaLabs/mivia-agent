package version

import "testing"

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
