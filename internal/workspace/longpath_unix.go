//go:build !windows

package workspace

// longPath is the identity on platforms without 8.3 short names.
func longPath(path string) string { return path }
