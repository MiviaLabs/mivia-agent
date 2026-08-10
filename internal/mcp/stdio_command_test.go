package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// stdioFixtureCommand returns the path of a real command file that passes
// ValidateServerConfig on every platform. Validation requires an absolute
// path to a regular file (executable on Unix, any regular file on Windows),
// so a POSIX-only fixture such as /bin/echo fails on Windows CI. The tests
// that use this helper never execute the command (they inject a fake
// Connect or only validate), so the stub contents are irrelevant: on
// Windows a .cmd batch file is a regular file, and on Unix a small
// executable shell script satisfies the executable-bit check.
func stdioFixtureCommand(t *testing.T) string {
	t.Helper()
	name := "mcp-stub"
	content := []byte("#!/bin/sh\nexit 0\n")
	if runtime.GOOS == "windows" {
		name += ".cmd"
		content = []byte("@echo off\r\nexit /b 0\r\n")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatalf("write stdio fixture command: %v", err)
	}
	if runtime.GOOS != "windows" {
		// os.WriteFile's mode is subject to the umask; make the executable
		// bit explicit so isExecutableFile accepts the fixture on Unix.
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatalf("chmod stdio fixture command: %v", err)
		}
	}
	return path
}
