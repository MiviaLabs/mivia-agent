package cli

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// hookGate is how this session is allowed to run hooks.
//
// The rule is fail-closed and it is the correct default even though it means a
// CI job expecting hooks gets none until someone passes the flag. The
// alternative - headless implies trusted - makes a cloned repo's hooks execute
// on any build machine that ever runs mivia non-interactively.
type hookGate struct {
	// headless means there is nobody to answer a confirmation prompt.
	headless bool
	// bypass is --bypass-hook-trust. It relaxes TRUST and nothing else.
	bypass bool
}

// hookGateFor decides the gate for an invocation.
//
// A one-shot -p run is headless even with a terminal attached: there is no
// prompt in it at which a confirmation could be answered.
func hookGateFor(invocation chatInvocation, stdinIsTerminal bool) hookGate {
	return hookGate{
		headless: invocation.prompt != "" || !stdinIsTerminal,
		bypass:   invocation.bypassHookTrust,
	}
}

// applyGate records the gate on the session and returns the startup messages it
// owes the operator.
//
// Two things must be said out loud. A headless run that silently executes no
// hooks is the failure mode that produces "hooks are broken" bug reports, so it
// names what did not run and the flag that would change that. A bypass that
// leaves no record is indistinguishable from having no gate at all, so it names
// every hook it ran without confirmation.
func (h *hookSession) applyGate(gate hookGate) []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gate = gate
	if gate.bypass {
		return h.bypassRecord()
	}
	if !gate.headless {
		return nil
	}
	var suppressed []string
	for _, decision := range h.decisions {
		if decision.Tier != hooks.TierManaged {
			suppressed = append(suppressed, hookGateLabel(decision.Group))
		}
	}
	if len(suppressed) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"lifecycle hooks are not running in this non-interactive session: %s. "+
			"There is no terminal to confirm them at, and a headless run deliberately does not "+
			"inherit an interactive confirmation. Pass --bypass-hook-trust to run them anyway.",
		strings.Join(suppressed, "; "))}
}

func (h *hookSession) bypassRecord() []string {
	var ran []string
	for _, decision := range h.decisions {
		// A managed hook needed no bypass to run, so recording it as one would
		// overstate what the flag did.
		if decision.Tier != hooks.TierManaged && decision.Status != hooks.StatusActive {
			ran = append(ran, hookGateLabel(decision.Group))
		}
	}
	if len(ran) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"--bypass-hook-trust: running %d unconfirmed lifecycle hook(s) without review: %s",
		len(ran), strings.Join(ran, "; "))}
}

// gateNotice explains why the statuses above are not the whole story. Without
// it a bypassed session lists hooks as "pending" while running every one of
// them, which describes a session that does not exist.
func (h *hookSession) gateNotice() string {
	switch {
	case h.gate.bypass:
		return "--bypass-hook-trust is active: every hook above runs regardless of its status."
	case h.gate.headless:
		return "this session is non-interactive: only managed hooks run, regardless of the " +
			"statuses above. Pass --bypass-hook-trust to run the rest."
	default:
		return ""
	}
}

func hookGateLabel(group hooks.Group) string {
	return fmt.Sprintf("%s %s", group.Event, hookArgvLabel(group))
}

// gatedRunnable applies the gate to the resolved decisions.
//
// The gate decides only WHICH groups run. Everything else the parser resolved -
// argv, timeout, on_timeout, the matcher - travels with the group untouched, so
// bypassing trust never bypasses argv-only execution or a gate's failure mode.
func (h *hookSession) gatedRunnable() []hooks.Group {
	var groups []hooks.Group
	for _, decision := range h.decisions {
		if h.mayRun(decision) {
			groups = append(groups, decision.Group)
		}
	}
	return groups
}

func (h *hookSession) mayRun(decision hooks.Decision) bool {
	if decision.Tier == hooks.TierManaged {
		return true
	}
	if h.gate.bypass {
		return true
	}
	if h.gate.headless {
		return false
	}
	return decision.Status == hooks.StatusActive
}
