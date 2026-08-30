package localengine

import (
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/gittest"
)

// TestMain disables git's detached background maintenance for every git
// process this package's tests spawn (fixture pushes into temp bare origins
// otherwise race t.TempDir cleanup - see internal/gittest).
func TestMain(m *testing.M) {
	gittest.DisableDetachedMaintenance()
	os.Exit(m.Run())
}
