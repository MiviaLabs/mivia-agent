package clichat

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseNonNegativeCountRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"", "abc", "-1", "3.5", " 4"} {
		if _, err := parseNonNegativeCount(raw, "--keep"); err == nil {
			t.Fatalf("parseNonNegativeCount(%q) = nil error, want rejection", raw)
		}
	}
	got, err := parseNonNegativeCount("0", "--keep")
	if err != nil || got != 0 {
		t.Fatalf("parseNonNegativeCount(\"0\") = %d, %v; want 0, nil", got, err)
	}
}

// TestSessionsGCRejectsBadFlagValues keeps the verb fail-closed: a malformed
// bound must abort before any store is opened or any row is deleted.
func TestSessionsGCRejectsBadFlagValues(t *testing.T) {
	for _, args := range [][]string{
		{"gc", "--keep-days", "nope"},
		{"gc", "--keep", "-2"},
	} {
		var stdout, stderr bytes.Buffer
		err := runSessionsWithIO(args, &stdout, &stderr)
		if err == nil {
			t.Fatalf("runSessionsWithIO(%v) = nil error, want rejection", args)
		}
		if !strings.Contains(err.Error(), "sessions gc") {
			t.Fatalf("runSessionsWithIO(%v) error = %v, want it scoped to sessions gc", args, err)
		}
	}
}

// TestSessionsUnknownSubcommandStillRejected guards the dispatch edit: adding
// gc must not turn an unknown verb into a silent success.
func TestSessionsUnknownSubcommandStillRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runSessionsWithIO([]string{"nonsense"}, &stdout, &stderr); err == nil {
		t.Fatal("unknown sessions subcommand was accepted")
	}
}

// TestSessionsNoSubcommandNamesGC keeps the usage line honest.
func TestSessionsNoSubcommandNamesGC(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runSessionsWithIO(nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("empty sessions invocation was accepted")
	}
	if !strings.Contains(err.Error(), "gc") {
		t.Fatalf("usage error = %v, want it to list gc", err)
	}
}
