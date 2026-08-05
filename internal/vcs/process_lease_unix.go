//go:build unix

package vcs

import (
	"os"
	"os/exec"
)

func inheritProcessLease(cmd *exec.Cmd, lease *os.File) (func(), error) {
	if lease != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, lease)
	}
	return func() {}, nil
}
