//go:build windows

package vcs

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestNtObjectNotExistMatchesWindowsConstants guards against x/sys constant
// drift: ntObjectNotExist's hardcoded NTSTATUS values must stay equal to the
// package's own STATUS_OBJECT_NAME_NOT_FOUND / STATUS_OBJECT_PATH_NOT_FOUND.
func TestNtObjectNotExistMatchesWindowsConstants(t *testing.T) {
	if !ntObjectNotExist(uint32(windows.STATUS_OBJECT_NAME_NOT_FOUND)) {
		t.Fatalf("ntObjectNotExist(STATUS_OBJECT_NAME_NOT_FOUND) = false, want true")
	}
	if !ntObjectNotExist(uint32(windows.STATUS_OBJECT_PATH_NOT_FOUND)) {
		t.Fatalf("ntObjectNotExist(STATUS_OBJECT_PATH_NOT_FOUND) = false, want true")
	}
}
