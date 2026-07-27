//go:build windows

package tools

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareCommand(cmd *exec.Cmd) (commandScope, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return commandScope{}, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return commandScope{}, err
	}

	attached := false
	attach := func(command *exec.Cmd) error {
		if command.Process == nil {
			return nil
		}
		process, err := windows.OpenProcess(
			windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
			false,
			uint32(command.Process.Pid),
		)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(process)
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			return err
		}
		attached = true
		return nil
	}
	cancel := func(command *exec.Cmd) error {
		if attached {
			return windows.TerminateJobObject(job, 1)
		}
		if command.Process == nil {
			return nil
		}
		return command.Process.Kill()
	}
	cleanup := func() {
		if attached {
			_ = windows.TerminateJobObject(job, 0)
		}
		_ = windows.CloseHandle(job)
	}
	return commandScope{attach: attach, cancel: cancel, cleanup: cleanup}, nil
}
