package vcs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestNtObjectNotExist(t *testing.T) {
	cases := []struct {
		name   string
		status uint32
		want   bool
	}{
		{"object name not found", statusObjectNameNotFound, true},
		{"object path not found", statusObjectPathNotFound, true},
		{"access denied is not a not-exist code", 0xC0000005, false},
		{"zero is not a not-exist code", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ntObjectNotExist(tc.status); got != tc.want {
				t.Fatalf("ntObjectNotExist(%#x) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// fakeNTStatus mirrors windows.NTStatus's method set as it actually stands:
// Error() string only. It deliberately has no Is or Unwrap method, matching
// `GOOS=windows go doc golang.org/x/sys/windows NTStatus` output.
type fakeNTStatus uint32

func (s fakeNTStatus) Error() string { return fmt.Sprintf("ntstatus %#x", uint32(s)) }

// TestOldRawPathNeverMatchesNotExist pins the defect's root cause: a plain
// fmt.Errorf("%w", err) wrap around an NTStatus-shaped error (no Is/Unwrap)
// never satisfies errors.Is(_, os.ErrNotExist), which is exactly why the
// marker-exclude retry gate in worktree_marker.go never fired on Windows
// before this fix.
func TestOldRawPathNeverMatchesNotExist(t *testing.T) {
	var raw error = fakeNTStatus(statusObjectNameNotFound)
	oldWrapped := fmt.Errorf("open: %w", raw)
	if errors.Is(oldWrapped, os.ErrNotExist) {
		t.Fatalf("errors.Is(oldWrapped, os.ErrNotExist) = true, want false (defect pin failed: NTStatus should not satisfy errors.Is via a plain %%w wrap)")
	}
}

func TestWithNotExistMatchesAndPreservesText(t *testing.T) {
	var raw error = fakeNTStatus(statusObjectPathNotFound)
	original := fmt.Errorf("open: %w", raw)
	wrapped := withNotExist(original)
	if !errors.Is(wrapped, os.ErrNotExist) {
		t.Fatalf("errors.Is(wrapped, os.ErrNotExist) = false, want true")
	}
	if !strings.Contains(wrapped.Error(), raw.Error()) {
		t.Fatalf("wrapped error %q does not contain original text %q", wrapped.Error(), raw.Error())
	}
}
