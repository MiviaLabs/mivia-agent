package cli

import (
	"fmt"
	"os"
	"testing"
)

func TestDumpLogoVisual(t *testing.T) {
	if os.Getenv("DUMP_LOGO") == "" {
		t.Skip("set DUMP_LOGO=1")
	}
	for _, i := range []int{0, 6, 12, 18} {
		fmt.Fprintf(os.Stderr, "--- frame %d ---\n%s\n", i, renderLogoFrame(i, 0))
	}
	fmt.Fprintf(os.Stderr, "\n--- wordmark braille frames ---\n")
	for _, i := range []int{0, 5, 12, 23} {
		fmt.Fprintf(os.Stderr, "--- frame %d ---\n%s\n", i, renderWordmarkBraille(i, 60))
	}
}
