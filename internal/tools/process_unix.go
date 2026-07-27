//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tools

import (
	"errors"
	"os/exec"
	"syscall"
)

// prepareCommand puts the command in its own process group so cancellation
// cannot leave shell descendants running after the caller times out.
func prepareCommand(cmd *exec.Cmd) (commandScope, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
