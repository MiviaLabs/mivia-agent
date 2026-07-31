//go:build !unix

package skills

import "io/fs"

// Platforms without Unix inode link counts still reject symlinks and retain
// the opened-file identity check in readRegularSkill.
func hasSingleLink(fs.FileInfo) bool { return true }
