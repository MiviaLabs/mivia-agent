package main

import (
	"bytes"
	"os"
	"strings"
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

func TestDispatchStream(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(&out, &errOut, []string{"stream"}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Add retry with exponential backoff") {
		t.Errorf("stream output missing fixture content: %s", out.String())
	}
}

func TestDispatchJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(&out, &errOut, []string{"json"}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"kind":"turn.start"`) {
		t.Errorf("json output missing expected event: %s", out.String())
	}
}

func TestDispatchThemes(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(&out, &errOut, []string{"themes", "--theme", "mivia-dark", "--tier", "ascii"}, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Mivia Dark") {
		t.Errorf("themes output missing theme label: %s", out.String())
	}
}

func TestDispatchThemesBadTier(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(&out, &errOut, []string{"themes", "--tier", "bogus"}, nil)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr = %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "unknown --tier") {
		t.Errorf("stderr missing tier error: %s", errOut.String())
	}
}

func TestDispatchNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(&out, &errOut, nil, nil)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("stderr missing usage: %s", errOut.String())
	}
}

func TestDispatchUnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := dispatch(&out, &errOut, []string{"bogus"}, nil)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("stderr missing usage: %s", errOut.String())
	}
}
