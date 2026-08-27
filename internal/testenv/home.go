// Package testenv isolates a test binary from the developer's own machine
// state. It is a non-test package so any package's TestMain can import it.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
)

// homeEnvVars are every variable workspace.UserHomeDir and the OS home
// resolver consult. HOME wins on every platform mivia targets, but the
// Windows pair is set too so one isolated binary behaves the same everywhere.
var homeEnvVars = []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"}

// IsolateHome points the process at a throwaway home directory and returns a
// cleanup function. Call it from TestMain, before m.Run.
//
// Without it, any test that reaches workspace.GlobalContextStorePath writes to
// the developer's real ~/.mivia/context.db - which is how one real store
// accumulated 57,077 distinct workspace ids and 70,841 worktree instance rows
// against a handful of genuine worktrees. Test rows in a user's durable store
// are not only noise: they are indistinguishable from real sessions, so no
// retention sweep can safely tell them apart afterwards.
//
// The returned cleanup restores the previous environment and removes the
// temporary directory. It is safe to call more than once.
func IsolateHome() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "mivia-test-home-")
	if err != nil {
		return nil, fmt.Errorf("create isolated home: %w", err)
	}
	saved := make(map[string]*string, len(homeEnvVars))
	for _, key := range homeEnvVars {
		if prev, ok := os.LookupEnv(key); ok {
			value := prev
			saved[key] = &value
		} else {
			saved[key] = nil
		}
	}
	restore := func() {
		for key, prev := range saved {
			if prev == nil {
				_ = os.Unsetenv(key)
				continue
			}
			_ = os.Setenv(key, *prev)
		}
		_ = os.RemoveAll(dir)
	}
	if err := os.Setenv("HOME", dir); err != nil {
		restore()
		return nil, fmt.Errorf("set isolated HOME: %w", err)
	}
	if err := os.Setenv("USERPROFILE", dir); err != nil {
		restore()
		return nil, fmt.Errorf("set isolated USERPROFILE: %w", err)
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if err := os.Setenv(key, filepath.Join(dir, filepath.Base(key))); err != nil {
			restore()
			return nil, fmt.Errorf("set isolated %s: %w", key, err)
		}
	}
	return restore, nil
}
