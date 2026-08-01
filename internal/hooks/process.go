package hooks

import "os/exec"

// commandScope owns a hook process's lifetime beyond cmd.Wait. A hook that
// spawns children and is then killed must not leave them running: the shape is
// the one run_command already uses, deliberately reimplemented here rather than
// imported, because internal/hooks must not reach internal/tools.
type commandScope struct {
	cancel  func(*exec.Cmd) error
	cleanup func()
}
