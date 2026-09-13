// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (slashallow.go) is D15's closed taxonomy for StepSlash:
// which slash commands are safe to run with no TUI attached, enforced
// at TOML validation time so an unusable automation is refused when it
// is written rather than failing at 2am. See
// docs/design/automations.md's D15 for the full rationale of every
// bucket below.
package automation

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// slashClass names why a builtin slash command is allowed or rejected
// for headless execution, so the refusal error can name the class
// rather than just saying "not allowed".
type slashClass int

const (
	// slashAllowedBareOK is a builtin that is safe to run with no
	// argument at all (only /compact today).
	slashAllowedBareOK slashClass = iota
	// slashAllowedNeedsArg is a builtin that is safe headless ONLY with
	// a non-empty argument, because a bare invocation opens a picker
	// that cannot render (/model, /effort, /budget, /steps).
	slashAllowedNeedsArg
	// slashRejectedInteractiveSurface needs a picker, tab UI, or
	// selection the executor does not have.
	slashRejectedInteractiveSurface
	// slashRejectedSessionLifecycle would corrupt or abandon the very
	// session the executor is driving.
	slashRejectedSessionLifecycle
	// slashRejectedOutboundUnattended performs a real outbound side
	// effect that must not happen unattended.
	slashRejectedOutboundUnattended
	// slashRejectedInformationalNoOp renders output nobody reads
	// headless and changes no state.
	slashRejectedInformationalNoOp
)

// builtinSlashClass classifies every builtin slash command by its
// canonical Name (never an alias - FindSlashCommand resolves aliases to
// their canonical command before this table is consulted, so an alias
// like /h inherits /help's classification automatically). A builtin
// missing from this table is a bug: classifySlashRef's caller
// (validateSlashExhaustive, in the test file) asserts every entry of
// clichat's builtInSlashCommands() has an entry here.
var builtinSlashClass = map[string]slashClass{
	"/compact": slashAllowedBareOK,
	"/model":   slashAllowedNeedsArg,
	"/effort":  slashAllowedNeedsArg,
	"/budget":  slashAllowedNeedsArg,
	"/steps":   slashAllowedNeedsArg,

	"/worktrees": slashRejectedInteractiveSurface,
	"/sessions":  slashRejectedInteractiveSurface,
	"/workflows": slashRejectedInteractiveSurface,
	"/queue":     slashRejectedInteractiveSurface,
	"/agent":     slashRejectedInteractiveSurface,
	"/title":     slashRejectedInteractiveSurface,
	"/new":       slashRejectedInteractiveSurface,
	"/select":    slashRejectedInteractiveSurface,

	"/clear":  slashRejectedSessionLifecycle,
	"/save":   slashRejectedSessionLifecycle,
	"/load":   slashRejectedSessionLifecycle,
	"/delete": slashRejectedSessionLifecycle,
	"/resume": slashRejectedSessionLifecycle,
	"/exit":   slashRejectedSessionLifecycle,

	"/search": slashRejectedOutboundUnattended,

	"/help":      slashRejectedInformationalNoOp,
	"/status":    slashRejectedInformationalNoOp,
	"/list":      slashRejectedInformationalNoOp,
	"/session":   slashRejectedInformationalNoOp,
	"/tools":     slashRejectedInformationalNoOp,
	"/hooks":     slashRejectedInformationalNoOp,
	"/agents":    slashRejectedInformationalNoOp,
	"/plain":     slashRejectedInformationalNoOp,
	"/provider":  slashRejectedInformationalNoOp,
	"/workspace": slashRejectedInformationalNoOp,
}

// classLabel names a slashClass for an error message.
func classLabel(c slashClass) string {
	switch c {
	case slashRejectedInteractiveSurface:
		return "needs an interactive surface"
	case slashRejectedSessionLifecycle:
		return "session-lifecycle mutation"
	case slashRejectedOutboundUnattended:
		return "outbound side effect, unattended"
	case slashRejectedInformationalNoOp:
		return "informational no-op"
	default:
		return "unknown"
	}
}

// validateStepSlash implements D15: Ref must resolve, via
// clichat.FindSlashCommand(ref, clichat.SlashSurfaceTUI, registry) - the
// surface choice is load-bearing (D15: SlashSurfaceTUI is the only
// surface that resolves SlashKindSkill entries at all, and the only
// exported surface constant) - to either a SlashKindSkill command
// (always allowed) or a builtin classified slashAllowedBareOK /
// slashAllowedNeedsArg in builtinSlashClass above. Every other
// resolution (an unresolvable ref, or a builtin classified as
// rejected) is refused, naming the class in the error.
func validateStepSlash(automationID string, stepIndex int, ref string, registry *skills.Registry) error {
	// FindSlashCommand matches its token argument exactly against a
	// command's Name/aliases (slash_catalog.go's FindSlashCommand does
	// an == comparison after trim+lowercase, never a prefix match), so
	// a ref carrying an argument - "/model gpt-x" - must have that
	// argument split off before resolution, or the whole string never
	// matches "/model" and every argument-carrying invocation of an
	// otherwise-allowed command would be misclassified as unrecognized.
	fields := strings.Fields(ref)
	if len(fields) == 0 {
		return fmt.Errorf("automation %q: step %d: slash command ref is empty", automationID, stepIndex)
	}
	cmdToken := fields[0]
	hasArg := len(fields) > 1

	cmd, ok := clichat.FindSlashCommand(cmdToken, clichat.SlashSurfaceTUI, registry)
	if !ok {
		return fmt.Errorf("automation %q: step %d: slash command %q is not recognized", automationID, stepIndex, ref)
	}
	if cmd.Kind == clichat.SlashKindSkill {
		return nil
	}
	class, known := builtinSlashClass[cmd.Name]
	if !known {
		return fmt.Errorf("automation %q: step %d: slash command %q has no headless classification (add one to builtinSlashClass)", automationID, stepIndex, ref)
	}
	switch class {
	case slashAllowedBareOK:
		return nil
	case slashAllowedNeedsArg:
		if hasArg {
			return nil
		}
		return fmt.Errorf("automation %q: step %d: slash command %q requires a non-empty argument headless (opens a picker when bare)", automationID, stepIndex, ref)
	default:
		return fmt.Errorf("automation %q: step %d: slash command %q is rejected for headless execution (%s)", automationID, stepIndex, ref, classLabel(class))
	}
}
