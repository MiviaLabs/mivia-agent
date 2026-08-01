package runtime

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"
)

// MaxHookContextBytes bounds hook-supplied advisory text for one invocation.
//
// It is a fixed constant rather than configuration: the bound exists so hook
// output can never be the reason a model-visible payload is unbounded, and a
// bound an operator can raise is not that. Over-budget context is TRUNCATED
// with a notice, not refused - unlike tool output, which the dispatcher
// destroys because an undeclared result cannot be bounded, hook context is
// advisory, and losing a tool's result because its formatter was chatty is the
// worse failure.
const MaxHookContextBytes = 8 << 10

// statusBlocked is a policy block: the tool did not run because a PreToolUse
// hook denied it. It is deliberately distinct from "failed", which means the
// tool ran and broke. Collapsing them would make a working gate and a broken
// tool indistinguishable in the audit sink, the TUI, and the CLI's status
// classification.
const statusBlocked = "blocked"

// blockedError is the error a blocked invocation carries.
//
// It is FIXED text and never embeds the hook's reason. internal/cli classifies
// a task by substring-matching the error message, so a hook reason containing
// "canceled" or "deadline exceeded" - which a hook author may write for
// entirely innocent reasons - would report a policy block as a cancellation.
// The verbatim reason still reaches the model in the result payload and the
// operator in the audit preview, which is where each of them reads it.
var blockedError = errors.New("blocked by a PreToolUse lifecycle hook")

// HookVerdict is a PreToolUse gate's answer.
type HookVerdict struct {
	// Denied blocks the invocation. The handler is never reached.
	Denied bool
	// Reason is why. It reaches the model - that is the point of a block.
	Reason string
	// Context is advisory text the gate produced even when it allowed. It
	// reaches the model merged with the reactive event's context.
	Context string
	// Runs is what executed, for the operator's view. A denial carries its own
	// run: the tool did not happen, and the reason came from a script.
	Runs []HookRun
}

type hookScopeKey struct{}

// withinHook marks a context as running inside hook execution.
//
// The guard is context-scoped, not process-wide, and that distinction is the
// whole point: a process-wide flag would let one invocation's hook suppress a
// concurrently running invocation's gate, which fails OPEN. Only work descended
// from this hook's own context skips the nested gate.
func withinHook(ctx context.Context) context.Context {
	return context.WithValue(ctx, hookScopeKey{}, true)
}

// insideHook reports whether this call descends from hook execution. If a hook
// ever reaches Invoke - through a future handler type, or a bug - the nested
// gate is skipped rather than recursing. MaxDepth would not catch it, because
// hook execution carries no depth.
func insideHook(ctx context.Context) bool {
	value, _ := ctx.Value(hookScopeKey{}).(bool)
	return value
}

// preInvoke runs the PreToolUse gate. It fires only for Kind == Tool: an event
// named PreToolUse that also fired on subagent dispatch would be a lie in a
// security-relevant name, and a matcher regex written against tool names would
// match subagent names by coincidence.
func (d *Dispatcher) preInvoke(ctx context.Context, req Request) HookVerdict {
	hook := d.policy.PreInvokeHook
	if hook == nil || req.Kind != Tool || insideHook(ctx) {
		return HookVerdict{}
	}
	return hook(withinHook(ctx), req)
}

// postInvoke runs the reactive PostToolUse hooks and returns their bounded
// context.
//
// The context passed is the dispatcher's INCOMING ctx, never execute's callCtx:
// by this point that one's deferred cancel has fired, and a hook run on it
// would silently never execute.
func (d *Dispatcher) postInvoke(ctx context.Context, req Request, result Result) HookResult {
	hook := d.policy.PostInvokeHook
	if hook == nil || req.Kind != Tool || insideHook(ctx) {
		return HookResult{}
	}
	return hook(withinHook(ctx), req, result)
}

// boundHookContext truncates on a rune boundary and announces the cut. Policy
// hook funcs are supplied by the caller, so the dispatcher enforces its own
// bound rather than trusting one to have been applied upstream.
//
// Bounding is all it does. The text is returned verbatim otherwise, because the
// dispatcher has no model-facing surface of its own: structural framing - and
// neutralizing tags the hook wrote to forge that framing - belongs to whoever
// assembles the model's view, which is internal/agent's appendHookContext.
// Editing the bytes here would leave HookContext describing neither what the
// hook wrote nor what the model read.
func boundHookContext(text string) string {
	if len(text) <= MaxHookContextBytes {
		return text
	}
	cut := text[:MaxHookContextBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + fmt.Sprintf("\n... hook context truncated at %d bytes", MaxHookContextBytes)
}

// neutralizedHookTag replaces a tag-shaped string that hook-authored content
// tried to write. It is deliberately shorter than the shortest string it can
// replace, so neutralization can only shrink the payload.
const neutralizedHookTag = "[escaped-hook-tag]"

// hookTagPattern matches anything a model could read as a lifecycle-hook-output
// tag. The {0,512} bound covers any realistic attribute list while capping
// the collateral — an unbounded [^>]* would swallow every line down to the
// next > anywhere below it.
//
// This pattern is defined here (not in internal/agent) so that deliverTerminal
// can neutralize hook-authored text before it enters the JSON envelope. The
// same pattern is compiled independently in internal/agent/hook_context.go
// for the framed-block path; both must agree on the shape.
var hookTagPattern = regexp.MustCompile(`(?i)<\s*/?\s*lifecycle-hook-output\b[^>]{0,512}>`)

// neutralizeHookTags removes any lifecycle-hook-output tags from text that
// originated in hook-authored content. Hook scripts are third-party code, so
// their output — including block reasons — is untrusted and must be sanitized
// for tag-shaped text before reaching the model.
//
// This is applied in deliverTerminal before the reason enters the JSON
// envelope, so json.Marshal's escaping of < and > is irrelevant: the tags are
// already gone by the time the bytes are serialized.
func neutralizeHookTags(text string) string {
	return hookTagPattern.ReplaceAllLiteralString(text, neutralizedHookTag)
}
