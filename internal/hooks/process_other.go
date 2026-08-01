//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package hooks

import "os/exec"

// prepareCommand falls back to killing the process itself where no process
// group primitive is available. Descendants may outlive the kill; that is a
// platform limit, recorded here rather than hidden.
func prepareCommand(cmd *exec.Cmd) commandScope {
	return commandScope{
		cancel:  cancelCommandTree,
		cleanup: func() { _ = cancelCommandTree(cmd) },
	}
}

func cancelCommandTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
