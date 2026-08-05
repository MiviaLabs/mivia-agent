//go:build !unix && !windows

package vcs

import (
	"os"
	"os/exec"
)

func startProcessWithLease(cmd *exec.Cmd, lease *os.File) (func(), error) {
	if lease != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, lease)
	}
	return func() {}, cmd.Start()
}
