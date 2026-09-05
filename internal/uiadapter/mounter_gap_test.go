package uiadapter

import "testing"

func TestCommandRunnerMountWithoutAPoolErrors(t *testing.T) {
	var nilRunner *CommandRunner
	if _, err := nilRunner.Mount("id"); err == nil {
		t.Fatal("Mount on a nil CommandRunner did not error")
	}

	r := &CommandRunner{}
	if _, err := r.Mount("id"); err == nil {
		t.Fatal("Mount with no session pool did not error")
	}
}
