//go:build windows

package vcs

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func startProcessWithLease(cmd *exec.Cmd, lease *os.File) (func(), error) {
	if lease == nil {
		return func() {}, cmd.Start()
	}
	handle := windows.Handle(lease.Fd())
	if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, syscall.Handle(handle))
	cleanup := func() {
		_ = windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
