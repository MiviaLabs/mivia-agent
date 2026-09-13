// skill_registry_error_test.go pins skillRegistrySource's error-reporting
// path: a LoadSessionSkills failure must not come back as a silent nil
// registry. Before this fix, the error was discarded outright and every
// subsequent StepSkill dispatch failed with the opaque "no skill registry
// available", with no trace of the real cause anywhere.
package cliautomations

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// captureStderr redirects the real os.Stderr to a pipe for the duration of
// the call and returns a func that restores it and returns everything
// written. skillRegistrySource's error path writes through
// cliagents.WarnSkillLoad, which prints straight to os.Stderr rather than
// this package's own stderrWriter indirection, so the stdoutWriter/
// stderrWriter capture (captureOutput in run_cmd_test.go) cannot see it.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = write
	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		captured <- string(data)
	}()
	var once bool
	var result string
	return func() string {
		if !once {
			once = true
			os.Stderr = original
			_ = write.Close()
			result = <-captured
			_ = read.Close()
		}
		return result
	}
}

// TestSkillRegistrySourceReportsLoadError forces loadSessionSkillsFunc to
// fail and asserts the error text reaches stderr via the package's
// existing warn path (WarnSkillLoad), not just a silently returned nil.
func TestSkillRegistrySourceReportsLoadError(t *testing.T) {
	prev := loadSessionSkillsFunc
	wantErr := errors.New("boom: skills unreadable")
	loadSessionSkillsFunc = func(root string, allowProject bool) (*skills.Registry, []string, error) {
		return nil, nil, wantErr
	}
	t.Cleanup(func() { loadSessionSkillsFunc = prev })

	restore := captureStderr(t)
	reg := skillRegistrySource(t.TempDir())()
	stderr := restore()

	if reg != nil {
		t.Fatalf("skillRegistrySource returned a non-nil registry on load error: %+v", reg)
	}
	if !strings.Contains(stderr, "boom: skills unreadable") {
		t.Fatalf("stderr = %q, want it to contain the discarded error text", stderr)
	}
}
