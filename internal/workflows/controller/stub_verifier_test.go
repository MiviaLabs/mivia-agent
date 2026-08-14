package controller

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// stubVerifierProfile is a test double for evidence_gate steps. It returns
// the fixed checks and derives the result status from them. Tests use it in
// place of workspace-declared profiles so no real command runs.
type stubVerifierProfile struct {
	name   string
	checks []verifier.Check
}

func (p stubVerifierProfile) Name() string { return p.name }

func (p stubVerifierProfile) Verify(context.Context, verifier.Request) (verifier.Result, error) {
	status := "passed"
	for _, check := range p.checks {
		if check.Status == "failed" {
			status = "failed"
		}
	}
	return verifier.Result{Status: status, Checks: p.checks}, nil
}
