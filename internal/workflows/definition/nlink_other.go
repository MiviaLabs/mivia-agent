//go:build !unix

package definition

import "io/fs"

// fileNlink returns 0 on non-Unix platforms, which means the nlink check is
// skipped. Symlink rejection and opened-file identity checks are still enforced.
func fileNlink(fs.FileInfo) uint64 { return 0 }
