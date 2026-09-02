package cliorchestrate

import (
	"context"
	"os"
	"testing"
)

// TestMain fences every doctor test off from the developer's machine. Doctor
// resolves the sync endpoint and the login state, and with a real login the
// real reachability probe would run - a live GET to production from a unit
// test, and a 3s hang per test offline. Neither may depend on who is logged
// in on the machine running the suite, so HOME is a fresh empty directory
// for the whole package and the probe seam is inert unless a test installs
// its own.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "cliorchestrate-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("MIVIA_API_BASE_URL", "")
	syncProbe = func(context.Context, string) (bool, string) {
		return false, "probe stubbed by TestMain"
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
