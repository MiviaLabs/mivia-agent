//go:build unix

package agent

import (
	"os"
	"syscall"
)

// openAuditDumpFile opens the operator-named dump file for append.
// O_NOFOLLOW refuses to write through a symlink planted at the dump
// path. Without it, an operator who points the variable at a shared
// directory can have a turn's whole prompt and response appended to a
// file another user chose - and the 0600 argument does not apply,
// because the target already exists. Repo rule 10 forbids following a
// symlink on a write path unconditionally.
func openAuditDumpFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}
