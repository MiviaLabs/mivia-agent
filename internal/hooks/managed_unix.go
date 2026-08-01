//go:build unix

package hooks

import (
	"os"
	"syscall"
)

// ManagedConfigPath is the operator-owned hook config: a system path outside
// the user's home, where a non-root account cannot install a hook.
func ManagedConfigPath() string { return "/etc/mivia/managed.toml" }

func fileOwner(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
