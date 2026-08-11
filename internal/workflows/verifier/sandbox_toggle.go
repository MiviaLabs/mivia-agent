package verifier

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

// sandboxDisabled is process-wide (mirrors redact.SetPolicy /
// tools.SetRedactToolArgs). The zero value (false) means the sandbox is
// enabled by default, so a process that never calls SetSandboxEnabled -
// every test, `mivia version`, any tool constructed directly - keeps the
// safe, isolated behavior.
var sandboxDisabled atomic.Bool

// SetSandboxEnabled installs the process-wide sandbox toggle, resolved from
// [harness] sandbox after config load. It is idempotent and safe to call
// more than once with the same value - newWorkflowController calls it on
// every controller build within a process, not strictly once at startup -
// but every call in one process is expected to carry the same resolved
// config value; it is not a per-run or per-caller override.
func SetSandboxEnabled(enabled bool) { sandboxDisabled.Store(!enabled) }

// SandboxEnabled reports the current process-wide sandbox toggle.
func SandboxEnabled() bool { return !sandboxDisabled.Load() }

// runVerifierCommand dispatches a verifier command through the sandboxed or
// direct-exec path per the process-wide toggle. This is a harness-level
// decision, not a per-verifier or per-run one: every evidence-gate command
// this process runs, for any project, follows the same toggle.
func runVerifierCommand(ctx context.Context, workDir string, baseline *GoModuleBaseline, policy secretpath.Policy, program string, args ...string) error {
	if !SandboxEnabled() {
		return runDirectCommand(ctx, workDir, baseline, program, args...)
	}
	return runSandboxedCommand(ctx, workDir, baseline, policy, program, args...)
}

// runDirectCommand runs one host check directly on the host, with no
// filesystem copy, no module-cache provisioning, and no environment
// scrubbing - the host's own PATH, GOCACHE, network, and credentials all
// apply exactly as they would for a command a developer typed by hand. This
// is the explicit-opt-out escape hatch for a host that cannot run
// bubblewrap; it trades the sandbox's isolation for that.
func runDirectCommand(ctx context.Context, workDir string, baseline *GoModuleBaseline, program string, args ...string) error {
	goMode := program == "go"
	if err := validateVerifierProgram(goMode, baseline, program); err != nil {
		return hostFailure(err)
	}
	exePath, _, _, err := resolveSandboxExecutable(goMode, baseline, program)
	if err != nil {
		return hostFailure(err)
	}
	command := exec.CommandContext(ctx, exePath, args...)
	command.Dir = workDir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return hostFailure(ctx.Err())
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return hostFailure(err)
		}
		return sourceCommandFailure(stdout.String()+stderr.String(), err)
	}
	return nil
}
