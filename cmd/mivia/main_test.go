package main

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

func TestVersion(t *testing.T) {
	if err := cli.Execute([]string{"version"}); err != nil {
		t.Fatal(err)
	}
}

func TestHelp(t *testing.T) {
	if err := cli.Execute([]string{"help"}); err != nil {
		t.Fatal(err)
	}
}

func TestUnknown(t *testing.T) {
	if err := cli.Execute([]string{"not-a-command"}); err == nil {
		t.Fatal("expected error")
	}
}
