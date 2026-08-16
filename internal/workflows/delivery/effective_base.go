package delivery

import "github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"

// EffectiveBase returns the delivery base a fresh admission must guard: a
// valid pr_base input overrides the workflow's declared delivery base (the
// same override delivery honors at publish time, resolveStackingInputs), so
// the admission origin-containment check keys off the branch the run will
// actually deliver to. An absent, empty, or invalid pr_base never fails
// admission — the declared base is used instead. inputSnapshot may be nil;
// a nil or non-delivery workflow yields the declared base or "".
func EffectiveBase(wf *compiler.CompiledWorkflow, inputSnapshot map[string]string) string {
	base := ""
	if policy, ok := FromCompiled(wf); ok {
		base = policy.Base
	}
	if prBase, ok := inputSnapshot[InputPRBase]; ok && prBase != "" {
		if ValidatePRBase(prBase) == nil {
			return prBase
		}
	}
	return base
}
