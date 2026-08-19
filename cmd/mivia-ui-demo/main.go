// Command mivia-ui-demo is an additive, throwaway demo binary for Phase 1
// of the new terminal UI: it replays the recorded testdata/ fixture
// through the plain and JSON renderers, and prints every embedded theme
// at every degradation tier. It drives no harness, needs no API key and
// no network, and is not the real cmd/mivia-ui binary (build-spec step 8,
// out of scope here).
package main

import (
	"fmt"
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
	if len(os.Args) < 2 {
		usage()
		return 2
	}

	var err error
	switch os.Args[1] {
	case "stream":
		err = runStream(os.Stdout)
	case "json":
		err = runJSON(os.Stdout)
	case "themes":
		err = runThemes(os.Stdout, os.Args[2:], os.Environ())
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mivia-ui-demo:", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mivia-ui-demo <stream|json|themes> [flags]")
}

func runStream(w *os.File) error {
	events, err := stream.DefaultFixture()
	if err != nil {
		return err
	}
	return stream.Render(w, events)
}

func runJSON(w *os.File) error {
	events, err := stream.DefaultFixture()
	if err != nil {
		return err
	}
	return jsonout.Render(w, events)
}
