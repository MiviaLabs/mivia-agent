//go:build !unix

package agent

import (
	"fmt"
	"os"
)

// openAuditDumpFile opens the operator-named dump file for append on
// platforms without O_NOFOLLOW. The Unix flag's threat - a turn's whole
// prompt and response appended through a symlink another user planted at
// the dump path, which the 0600 mode cannot protect because the target
// already exists - is refused best-effort with a pre-open Lstat. A
// symlink swapped in after that check is a residual race no portable
// flag covers.
func openAuditDumpFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("audit dump path is a symlink; refusing to append")
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}
