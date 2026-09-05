package agents

import (
	"fmt"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/testenv"
)

func TestMain(m *testing.M) {
	restoreHome, err := testenv.IsolateHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	restoreHome()
	os.Exit(code)
}
