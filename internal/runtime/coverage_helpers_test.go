package runtime

import (
	"strings"
	"testing"
)

func TestSessionIDAndDispatcherAllowHelpers(t *testing.T) {
	first, second := NewSessionID(), NewSessionID()
	if first == second || len(first) != 26 || strings.Contains(first, "=") {
		t.Fatalf("session IDs must be distinct unpadded 128-bit base32 values: %q, %q", first, second)
	}
	dispatcher := New(Policy{})
	if dispatcher.Has(Tool, "read_file") {
		t.Fatal("unregistered handler reported as present")
	}
	dispatcher.Allow(Tool, "read_file")
	if !dispatcher.policy.Allow[Tool]["read_file"] {
		t.Fatal("Allow did not create the requested permission")
	}
}
