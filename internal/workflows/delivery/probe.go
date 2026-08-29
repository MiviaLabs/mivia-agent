package delivery

import (
	"fmt"
	"os/exec"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// prToolProbe runs the provider's PR tool with a harmless offline argument.
// It is a variable so tests can drive the failure paths without a real gh.
var prToolProbe = func(program string) error {
	cmd := exec.Command(program, "--version")
	cmd.WaitDelay = deliveryWaitDelay
	cmd.Env = ghEnv()
	return cmd.Run()
}

// ProbePRTool reports whether the provider's PR tool is usable before a run
// starts.
//
// Delivery is the last step of a long workflow, so a missing tool is only
// discovered after every gate has passed and the whole run is spent. The probe
// moves that discovery to admission.
//
// It is deliberately OFFLINE. `gh --version` touches no network and proves the
// binary exists and executes. An auth probe was considered and rejected: it
// would put a network call in the admission path of every delivery run, and it
// still could not prove the token is valid at delivery time, which may be
// hours later. Delivery keeps its own authoritative checks.
func ProbePRTool(provider string) error {
	program, err := prToolProgram(provider)
	if err != nil {
		return err
	}
	if err := prToolProbe(program); err != nil {
		return fmt.Errorf("workflow requires delivery but %q is not usable: %w", program, err)
	}
	return nil
}

// prToolProgram maps a delivery provider to the executable that publishes for
// it. An unknown provider is refused rather than assumed. The refusal states
// the support boundary instead of a missing tool: only
// definition.ProviderGitHub is supported, and no installed CLI changes that.
func prToolProgram(provider string) (string, error) {
	switch provider {
	case definition.ProviderGitHub:
		return "gh", nil
	default:
		return "", fmt.Errorf("delivery provider %q is not supported (only %q is currently supported)", provider, definition.ProviderGitHub)
	}
}
