//go:build unix

package mcp

import "os"

// isExecutableFile reports whether the stdio server command is a regular
// file with an executable bit set. Unix distinguishes executable bits;
// Windows does not (see executable_other.go).
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
