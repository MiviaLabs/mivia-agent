//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package tools

import "os/exec"

func prepareCommand(cmd *exec.Cmd) (commandScope, error) {
	return commandScope{
		attach:  func(*exec.Cmd) error { return nil },
		cancel:  cancelCommandTree,
		cleanup: func() { _ = cancelCommandTree(cmd) },
	}, nil
}

func cancelCommandTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
