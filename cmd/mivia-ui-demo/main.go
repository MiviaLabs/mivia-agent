// Command mivia-ui-demo is an additive, throwaway demo binary for Phase 1
// of the new terminal UI: it replays the recorded testdata/ fixture
// through the plain and JSON renderers, and prints every embedded theme
// at every degradation tier. It drives no harness, needs no API key and
// no network, and is not the real cmd/mivia-ui binary (build-spec step 8,
// out of scope here).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/ui/jsonout"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
)

func main() {
	os.Exit(run())
}

// run is separated from main so testscript's RunMain can register this
// binary's behaviour as an in-process command without calling os.Exit
// from inside a test.
func run() int {
	return dispatch(os.Stdout, os.Stderr, os.Args[1:], os.Environ())
}

// dispatch is the actual subcommand router, factored out of run() so it
// can be exercised by direct in-process unit tests (a bytes.Buffer in,
// exit code and output out) in addition to the black-box testscript
// coverage in testdata/script - testscript execs a subprocess, which
// standard `go test -coverprofile` cannot see, so this split is what
// keeps the routing and error-formatting logic itself under normal
// coverage.
func dispatch(stdout, stderr io.Writer, args []string, env []string) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}

	var err error
	switch args[0] {
	case "stream":
		err = runStream(stdout)
	case "json":
		err = runJSON(stdout)
	case "themes":
		err = runThemes(stdout, args[1:], env)
	default:
		usage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "mivia-ui-demo:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: mivia-ui-demo <stream|json|themes> [flags]")
}

func runStream(w io.Writer) error {
	events, err := stream.DefaultFixture()
	if err != nil {
		return err
	}
	return stream.Render(w, events)
}

func runJSON(w io.Writer) error {
	events, err := stream.DefaultFixture()
	if err != nil {
		return err
	}
	return jsonout.Render(w, events)
}
