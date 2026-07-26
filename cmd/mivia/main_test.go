package main

import "testing"

func TestRunVersion(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
}

func TestRunUnknown(t *testing.T) {
	if err := run([]string{"not-a-command"}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}
