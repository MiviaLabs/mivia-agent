// Package gittest holds test-process configuration for packages whose tests
// spawn real git processes against temporary fixture repositories.
package gittest

import (
	"os"
	"strconv"
)

// DisableDetachedMaintenance turns off git's background maintenance for
// every git process this test process spawns - fixture helpers and
// production git runners alike, since both inherit the process environment.
//
// Without it, a `git push` into a fixture bare origin can trigger a
// DETACHED auto-gc in origin.git that is still writing objects while
// t.TempDir cleanup removes the tree, failing the test with
// "TempDir RemoveAll cleanup: unlinkat .../objects: directory not empty"
// (the verify-main CI flake). GIT_CONFIG_* environment pairs reach every
// repo the spawned processes touch - including the RECEIVING side of a
// push (receive.autogc), which per-invocation -c flags on the pushing side
// cannot reach. Call it from TestMain before m.Run; it composes with any
// GIT_CONFIG_COUNT already present.
func DisableDetachedMaintenance() {
	pairs := [][2]string{
		{"gc.auto", "0"},
		{"gc.autoDetach", "false"},
		{"receive.autogc", "false"},
		{"maintenance.auto", "false"},
	}
	base := 0
	if n, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && n > 0 {
		base = n
	}
	for i, kv := range pairs {
		idx := strconv.Itoa(base + i)
		os.Setenv("GIT_CONFIG_KEY_"+idx, kv[0])
		os.Setenv("GIT_CONFIG_VALUE_"+idx, kv[1])
	}
	os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(base+len(pairs)))
}
