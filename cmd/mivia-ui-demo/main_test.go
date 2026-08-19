package main

import (
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain registers this binary's own behaviour as an in-process
// testscript command, so `exec mivia-ui-demo ...` in testdata/script/*.txtar
// runs the real CLI without a separate go-build step.
func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"mivia-ui-demo": run,
	}))
}

// TestScripts is the testscript .txtar coverage for the non-TTY (plain
// stream) and --output json paths, per the phase's definition of done.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
	})
}
