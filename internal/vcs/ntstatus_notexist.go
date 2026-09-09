package vcs

import (
	"fmt"
	"os"
)

// NTSTATUS values returned by NtCreateFile when a path component does not
// exist. Compared by direct value so the classification is deterministic
// and testable without an ntdll dependency (this file carries no windows
// build tag).
const statusObjectNameNotFound uint32 = 0xC0000034
const statusObjectPathNotFound uint32 = 0xC000003A

// ntObjectNotExist reports whether status is one of the NTSTATUS codes
// NtCreateFile returns for a missing path component.
func ntObjectNotExist(status uint32) bool {
	return status == statusObjectNameNotFound || status == statusObjectPathNotFound
}

// withNotExist wraps err so errors.Is(_, os.ErrNotExist) matches while
// preserving err's own text. windows.NTStatus implements only Errno() and
// Error() - it has neither Is nor Unwrap - so a plain fmt.Errorf("%w", err)
// around an NTStatus never satisfies errors.Is(_, os.ErrNotExist). Chaining
// os.ErrNotExist as a second %w verb (Go 1.20+ multi-%w) gives the sentinel
// match callers need without discarding the original NTSTATUS text.
func withNotExist(err error) error {
	return fmt.Errorf("%w (%w)", os.ErrNotExist, err)
}
