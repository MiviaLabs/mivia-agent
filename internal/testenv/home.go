// Package testenv isolates a test binary from the developer's own machine
// state. It is a non-test package so any package's TestMain can import it.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// homeEnvVars are every variable workspace.UserHomeDir and the OS home
// resolver consult. HOME wins on every platform mivia targets, but the
// Windows pair is set too so one isolated binary behaves the same everywhere.
var homeEnvVars = []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"}

// HomeDirPrefix names the throwaway directories IsolateHome creates. It is
// exported so a package can assert its own isolation (HomeIsolated) without
// knowing what the developer's real home happens to be.
const HomeDirPrefix = "mivia-test-home-"

// HomeIsolated reports whether the process home currently resolves to a
// throwaway directory installed by IsolateHome.
//
// A package asserts this to keep its results independent of the machine it
// runs on. Ambient state does not only pollute a real home; it also DECIDES
// test outcomes. internal/cliworkflow read the developer's own
// ~/.mivia/mivia.toml through config.Load - which merges the user-level [mcp]
// table regardless of an explicit ConfigPath - and 19 resume tests failed on
// a machine with MCP servers configured while passing everywhere else. A
// suite whose verdict depends on who runs it proves nothing.
func HomeIsolated() bool {
	home, ok := os.LookupEnv("HOME")
	if !ok || home == "" {
		return false
	}
	return strings.HasPrefix(filepath.Base(home), HomeDirPrefix)
}

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
	dir, err := os.MkdirTemp("", HomeDirPrefix)
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
