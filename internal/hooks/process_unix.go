//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package hooks

import (
	"errors"
	"os/exec"
	"syscall"
)

// prepareCommand puts a hook in its own process group so a timeout kills the
// whole tree. A PreToolUse gate that timed out while its children kept running
// would report a verdict for work that had not stopped.
func prepareCommand(cmd *exec.Cmd) commandScope {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return commandScope{
		cancel:  cancelCommandTree,
		cleanup: func() { _ = cancelCommandTree(cmd) },
	}
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
