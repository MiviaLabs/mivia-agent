package runtime

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// outputCeilingFloor is the historical dispatcher output backstop. A derived
// ceiling never goes below it, so tools without a declared result budget keep
// exactly the bound they always had.
const outputCeilingFloor = 256 << 10

// outputCeilingSlack is headroom added on top of a tool-declared result budget
// when deriving an output backstop. Budgets bound tool CONTENT; the wire output
// also carries fixed-size tool framing — read_file's "… lines X–Y" window
// header and "... truncated at N bytes" notice (~111 bytes worst case),
// run_command's cwd/exit-status lines. 4 KiB covers any such framing by a wide
// margin while staying negligible next to the budgets themselves.
// Input-derived framing (run_command's argv echo) is covered separately by the
// policy's MaxInputBytes allowance.
const outputCeilingSlack = 4096

// defaultInputAllowance mirrors the Policy default for MaxInputBytes. A
// ceiling derivation handed a non-positive input cap uses it, so the derived
// bound matches what New would actually enforce.
const defaultInputAllowance = 64 << 10

// toolOutputCeiling returns the output backstop for ONE tool, derived from that
// tool's OWN declared result budget (tools.ResultBudgetTool):
//
//	max(declaredBudget, outputCeilingFloor) + maxInputBytes + outputCeilingSlack
//
// A tool that declares nothing gets the floor-derived value, which is exactly
// what the pre-per-tool global backstop gave it on the default config — the
// derivation only ever adds the framing terms, never removes them.
//
// The input allowance is added because tool results may echo request input
// verbatim (run_command's argv header), so an honest result can exceed its
// content budget by up to the input size.
func toolOutputCeiling(t tools.Tool, maxInputBytes int) int {
	if maxInputBytes <= 0 {
		maxInputBytes = defaultInputAllowance
	}
	budget := outputCeilingFloor
	if budgeted, ok := t.(tools.ResultBudgetTool); ok {
		if declared := budgeted.ResultBudgetBytes(); declared > budget {
			budget = declared
		}
	}
	return budget + maxInputBytes + outputCeilingSlack
}

// DeriveOutputCeiling returns the runaway-tool output backstop for a
// dispatcher serving the given registry: the largest tool-declared result
// budget (tools.ResultBudgetTool) plus an input allowance plus slack, floored
// at 256KiB. maxInputBytes is the dispatcher's input cap; <= 0 means the
// Policy default (64KiB). It is added because tool results may echo request
// input verbatim (run_command's argv header), so an honest result can exceed
// its content budget by up to the input size.
//
// This is the GLOBAL value: the dispatcher's Policy.MaxOutputBytes, which is a
// hard cap that no single tool's ceiling may exceed. The bound actually
// enforced on a given call is the per-tool ceiling from toolOutputCeiling,
// min'd against this — see Dispatcher.OutputCeiling.
//
// The dispatcher hard-fails (never truncates) any output above its effective
// ceiling. That is intentional for runaway tools, but the audit of commit
// 0f6e524 showed the fixed 256KiB default colliding with the default
// max_read_bytes (also 262144): a config-compliant read_file window — content
// at budget plus header — was destroyed whole. Deriving the ceiling from the
// budgets the registry actually granted guarantees the backstop can never bind
// below an honest tool result, whatever the config.
func DeriveOutputCeiling(r *tools.Registry, maxInputBytes int) int {
	if maxInputBytes <= 0 {
		maxInputBytes = defaultInputAllowance
	}
	ceiling := outputCeilingFloor
	if r == nil {
		return ceiling
	}
	for _, t := range r.List() {
		budgeted, ok := t.(tools.ResultBudgetTool)
		if !ok {
			continue
		}
		budget := budgeted.ResultBudgetBytes()
		if budget <= 0 {
			continue
		}
		if derived := budget + maxInputBytes + outputCeilingSlack; derived > ceiling {
			ceiling = derived
		}
	}
	return ceiling
}

// overCeilingError describes a result the dispatcher destroyed for exceeding
// its ceiling. It names the capability, the result size and the bound broken:
// this is the dispatcher's most confusing failure — the tool did nothing wrong
// from its own point of view — and a bare "output budget exceeded" left no way
// to tell a runaway tool from a ceiling set too low. Sizes and the name only;
// no result content reaches the message (rule 10). The wording deliberately
// avoids "canceled" and "deadline exceeded", which internal/cli/dispatch.go's
// statusFromErr substring-matches to classify a task outcome.
func overCeilingError(req Request, size, ceiling int) error {
	return fmt.Errorf("output budget exceeded: %s %q produced %d bytes, over its %d-byte ceiling",
		req.Kind, req.Name, size, ceiling)
}

// OutputCeiling returns the output bound the dispatcher actually enforces for
// one registered capability: the per-tool ceiling recorded at RegisterTool
// time, capped by Policy.MaxOutputBytes. Kinds with no declarable budget
// (Skill, Subagent) and handlers installed through the bare Register path get
// the policy value.
func (d *Dispatcher) OutputCeiling(k Kind, name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.effectiveCeilingLocked(k, name)
}

// effectiveCeilingLocked is the single place the enforced bound is computed.
// The caller must hold d.mu. reserve() calls it inside its existing critical
// section, next to the handler lookup, so a handler and its ceiling are always
// read together and no extra lock lands on the hot path.
//
// The min is the safety argument: a per-tool ceiling may only ever TIGHTEN the
// policy bound, never raise it, so an explicit Policy.MaxOutputBytes remains a
// hard global cap.
func (d *Dispatcher) effectiveCeilingLocked(k Kind, name string) int {
	if k != Tool {
		return d.policy.MaxOutputBytes
	}
	perTool, ok := d.toolCeilings[name]
	if !ok || perTool > d.policy.MaxOutputBytes {
		return d.policy.MaxOutputBytes
	}
	return perTool
}
