//go:build !unix

package mcp

import "os"

// isExecutableFile reports whether the stdio server command is a regular
// file that can be executed. Windows has no executable permission bits, so
// any regular file is a valid target; a file that cannot actually be run
// fails cleanly when the server starts.
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
