package tools

import "os/exec"

type commandScope struct {
	attach  func(*exec.Cmd) error
	cancel  func(*exec.Cmd) error
	cleanup func()
}
