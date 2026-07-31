//go:build !unix

package config

import "io/fs"

// Platforms without Unix inode link counts still reject symlinks and retain
// the opened-file identity check in readRegularAgent.
func hasSingleLink(fs.FileInfo) bool { return true }
