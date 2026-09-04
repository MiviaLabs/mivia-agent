package chat

// The deferred-call admission decision: what happens when a turn calls a
// tool that is advertised but not yet admitted. serveUnadmittedTool
// (session_turn_surface.go) owns the wiring - the advertised-name guard, the
// scoped-turn refusal, the lock read - and hands the call here. The decision
// lives in its own file so neither half grows into the other.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// admitDeferredCall resolves the call against the FULL authorized set, then
// decides, then spends. Every step below used to run before the one above
// it, so a call that was about to be refused had already charged an admission
// attempt, staged a publication, and installed a handler on the session
// dispatcher.
func (s *Session) admitDeferredCall(ctx context.Context, turnID uint64, dispatcher *runtime.Dispatcher, resolver func() *tools.Registry, denylist []string, name string, args json.RawMessage, emitPending func(toolCallID, name, detail, input string)) agent.UnadmittedToolResult {
	base, tool, lookup := lookupDeferredTool(dispatcher, resolver, denylist, name)
	if lookup == deferredNoSuchTool {
		// No publication can ever resolve this name, so nothing is staged for
		// it and the model is not told a load was queued: that message names a
		// retry, and the retry cannot resolve either. A missing DISPATCHER is
		// the other case and still degrades to the staged retry below, because
		// there the tool does exist.
		return agent.UnadmittedToolResult{
			Handled: true,
			Content: fmt.Sprintf("tool %q is advertised for this agent but is not present in its tool set, so it cannot be loaded or called; do not retry it", name),
		}
	}

	// Approval BEFORE the budget, and only for a call that can actually run.
	// A refusal is the operator's decision, not the model's request, and
	// charging the loading budget for it lets a deny policy exhaust the
	// session's own admission attempts. Asking on the no-wiring degrade would
	// be worse still: it raises a prompt for a call that was never going to
	// execute.
	if lookup == deferredFound {
		if decision := s.decideDeferredApproval(ctx, tool, name, args, emitPending); !decision.Approved {
			return agent.UnadmittedToolResult{
				Handled: true, Ran: true, Failed: true,
				Content: "tool call denied by user: " + decision.Reason,
			}
		}
	}

	// An already-staged name is hot-served without a second charge: the call
	// that staged it (load_tools, or the fresh deferred call) already spent
	// the admission attempt, and a re-stage would be a no-op. Approval above
	// still ran, so this branch is not a way past a prompt; and it is
	// reachable only for a call that can actually run (deferredFound) - the
	// no-wiring degrade below keeps its queued-for-publication notice, which
	// stays true for a staged name too.
	if lookup == deferredFound && s.hotServeEligible(name) {
		return admitForExecution(dispatcher, base, tool, name)
	}

	if err := s.spendAdmissionFor(name, turnID); err != nil {
		return agent.UnadmittedToolResult{Handled: true, Content: err.Error()}
	}

	if lookup == deferredFound {
		return admitForExecution(dispatcher, base, tool, name)
	}
	// Only the no-wiring degrade reaches here, and it dispatched nothing - so
	// there are no hook runs or hook context to report, which is what the
	// existing degrade tests already assert.
	return agent.UnadmittedToolResult{
		Handled: true,
		Content: fmt.Sprintf("tool %q is authorized but was not yet loaded. It has been queued to load automatically; publication happens at the next step boundary and can be deferred - retry the call on your next step", name),
	}
}

// lookupDeferredTool finds the tool in the FULL authorized set WITHOUT
// installing anything. found=false on the benign reasons the caller degrades
// on: no dispatcher, no resolver wired, the resolver returns nil, the name
// absent even from the full set.
//
// Lookup is separate from registration on purpose. Registration is a durable
// grant on the session dispatcher - Dispatcher.register sets
// policy.Allow[Tool][name] and there is no removal API - and it used to happen
// before the call was approved, budgeted or staged, so a refused call left the
// handler installed. The caller now resolves first to DECIDE, and registers
// only once the call is actually going to run.
func lookupDeferredTool(dispatcher *runtime.Dispatcher, resolver func() *tools.Registry, denylist []string, name string) (*tools.Registry, tools.Tool, deferredLookup) {
	if dispatcher == nil || resolver == nil {
		return nil, nil, deferredNoWiring
	}
	base := resolver()
	if base == nil {
		return nil, nil, deferredNoWiring
	}
	// The operator's denial outranks everything below, and is checked before
	// the name is even looked up. A denied name reports as absent, not as a
	// degrade: no publication may ever resolve it, so nothing is staged and
	// the model is not told to retry.
	if tools.MandatoryDenylistSet(denylist...)[name] {
		return nil, nil, deferredNoSuchTool
	}
	tool, found := base.Get(name)
	if !found {
		return nil, nil, deferredNoSuchTool
	}
	return base, tool, deferredFound
}

// deferredLookup separates the two ways a lookup can fail, because the caller
// must answer them differently and they were collapsed into one bool.
//
// deferredNoWiring is a degrade: no dispatcher or no resolver, so THIS call
// cannot be served synchronously - but the tool exists and a step-boundary
// publication can still make it callable, so staging it and telling the model
// to retry is true. deferredNoSuchTool is not a degrade: the name is absent
// from the full authorized set, so no publication can ever resolve it and both
// the stage and the retry would be a lie.
type deferredLookup int

const (
	deferredFound deferredLookup = iota
	deferredNoWiring
	deferredNoSuchTool
)

// registerDeferredTool installs the handler so the dispatcher can execute this
// call. It runs only after the call is approved and budgeted.
//
// RegisterTool's "duplicate handler" error is treated as success (Has confirms
// it), not failure: a sibling call for the same deferred tool in the same step
// may have already won the race.
func registerDeferredTool(dispatcher *runtime.Dispatcher, base *tools.Registry, tool tools.Tool) bool {
	if err := dispatcher.RegisterTool(base, tool); err != nil && !dispatcher.Has(runtime.Tool, tool.Name()) {
		return false
	}
	return true
}

// admitForExecution installs the handler and hands the tool to the loop.
//
// The loop runs it through the same shim an admitted call uses, which is what
// stops this path being a second implementation of tool execution - the
// defect that produced nine separate bugs here (DC-35). Registration stays on
// this side because the dispatcher must know the handler before the shim
// invokes it, and it happens only now: after the call was approved and paid
// for, so a refused call leaves nothing behind.
func admitForExecution(dispatcher *runtime.Dispatcher, base *tools.Registry, tool tools.Tool, name string) agent.UnadmittedToolResult {
	if !registerDeferredTool(dispatcher, base, tool) {
		return agent.UnadmittedToolResult{
			Handled: true,
			Content: fmt.Sprintf("tool %q could not be installed for this call; retry it on your next step", name),
		}
	}
	return agent.UnadmittedToolResult{Handled: true, Execute: tool}
}
