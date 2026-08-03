package agent

import (
	"context"
	"fmt"
	"math"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// Aggregate per-batch tool-result budget (plan tools/06).
//
// N parallel calls, each honestly under its own per-call cap, still blow the
// context when they land together. The per-call cap cannot see them: it is
// applied inside a worker that knows nothing about its siblings. This file is
// the layer that does - one pure function over the issue-ordered results of a
// single tool batch, charging body bytes against a budget and degrading the
// results that do not fit into references to their own full bodies.
//
// It degrades, it never fails: the model already paid for these calls and the
// side effects already happened. Nothing here can turn a completed call into
// an error, and nothing here destroys bytes that are not recoverable through
// read_output (with the deliberate exception of ephemeral results, whose
// bodies are scrubbed from history anyway - see D10 below).

const (
	// batchBudgetDerived is the sentinel Options.BatchResultBudgetBytes value
	// meaning "derive the budget from the prompt budget".
	batchBudgetDerived = -1

	// derivedBatchBudgetFloorBytes is the smallest derived budget. A tiny
	// model context would otherwise derive a budget under which every batch
	// degrades, which trades a context overflow for a useless conversation.
	derivedBatchBudgetFloorBytes = 256 << 10

	// BatchDegradeFloorBytes is the smallest re-cut a straddling result gets.
	// Exported because the operator-facing minimum for the budget knob
	// (config.MinBatchResultBudgetBytes) is this number: a budget under the
	// floor cannot be honoured, and the two must not drift apart silently.
	// Cutting to exactly the remaining bytes is correct arithmetic and useless
	// output: a 40-byte remainder buys the model nothing but a notice. One
	// result per batch may overshoot the budget by up to this much - see the
	// bound in shapeReport's doc and §5 of the plan.
	BatchDegradeFloorBytes = 16 << 10

	// statusLineMaxBytes bounds the D8 status line. It is a bound, not a
	// format: statusLine composes four ints into a fixed template, and
	// TestShapeBatchStatusLineIsBounded pins that even int-max arguments stay
	// under this. The framing bound in §4.3 is stated in terms of it.
	statusLineMaxBytes = 160

	// Derived budget = MaxContextTokens x bytesPerToken x (1/derivedBudgetShare).
	// Deliberately NOT calibrated: the calibration ratio corrects a token
	// ESTIMATE against provider-reported usage, and applying it here would let
	// a drifting estimator move a hard byte bound.
	bytesPerToken        = 4
	derivedBudgetShare   = 4 // 25%
	maxDerivableTokens   = math.MaxInt / bytesPerToken
	unlimitedBatchBudget = 0
)

// resultParts is one tool result in structured form, as the worker produced it
// and before anything is appended to history (D9).
//
// The hybrid retention rule is the whole point of the split. An untruncated
// result carries its original body in cappedBody - the same string that would
// have gone into history anyway, so retaining it through shaping costs zero
// extra bytes. A truncated result carries only (refA, totalN): pass 1 already
// spooled its original, so a second pass loads those bytes back rather than
// holding a batch-scaled pile of originals alive for a slow path that usually
// does not run.
type resultParts struct {
	// cappedBody is the model-visible body pass 1 produced: the original when
	// truncated is false, a truncation artifact (content + notice) when true.
	cappedBody string
	// refA is the remainder ref pass 1 minted, empty when the body was not
	// truncated or the store failed.
	refA string
	// totalN is the true size of the ORIGINAL body in bytes, before any cap.
	totalN int
	// effectiveCap is the per-call cap pass 1 applied (0 = uncapped). Shaping
	// clamps its re-cut target to it: a tool that declares a 4 KiB result
	// budget must not come back holding 16 KiB because the batch had room
	// (F3).
	effectiveCap int
	// hookContext is the raw PostToolUse advisory text. It is re-appended
	// after shaping, framed, and never charged against the budget: paying for
	// a formatter's advice out of the tool's own bytes would destroy real
	// result content to make room for commentary about it (C4/F5).
	hookContext string
	// truncated reports whether pass 1 cut the body.
	truncated bool
	// ephemeral marks a result whose body ScrubEphemeralToolMessages removes
	// from history after the final step. Shaping charges it like any other
	// body but never puts it behind a ref (D10): a ref would let the model
	// resurrect, via read_output, exactly the bytes the scrub exists to
	// remove.
	ephemeral bool
}

// shapeStore is the spool capability shapeBatch needs, narrowed to keep the
// enforcement core injectable and testable without a ledger.
type shapeStore interface {
	Spool(ctx context.Context, principal string, data []byte) string
	Load(ctx context.Context, principal, ref string) ([]byte, error)
}

// shapeEnv carries the only I/O shapeBatch performs.
type shapeEnv struct {
	store     shapeStore
	principal string
}

// newShapeEnv builds the environment from loop options. A nil spool or empty
// principal yields an environment that mints no refs - degraded results then
// carry plain notices, exactly as truncation does today.
func newShapeEnv(spool *remainder.Spool, principal string) shapeEnv {
	if spool == nil {
		return shapeEnv{principal: principal}
	}
	return shapeEnv{store: spool, principal: principal}
}

func (e shapeEnv) spool(body string) string {
	if e.store == nil || e.principal == "" || body == "" {
		return ""
	}
	return e.store.Spool(context.Background(), e.principal, []byte(body))
}

func (e shapeEnv) load(ref string) (string, bool) {
	if e.store == nil || e.principal == "" || ref == "" {
		return "", false
	}
	data, err := e.store.Load(context.Background(), e.principal, ref)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// shapeReport is the content-free accounting for one shaped batch.
type shapeReport struct {
	budget    int // the budget the batch was charged against
	charged   int // body bytes emitted into history (excludes hook context)
	degraded  int // results whose body was re-cut or replaced by a notice
	remaining int // unspent budget after the batch
	results   int // batch size
}

// shapeBatchResults applies the aggregate per-batch budget to the ordered
// results and returns the body each one contributes to history.
//
// Charging happens HERE, after the sort, and not in the workers: workers
// finish in scheduling order, which is not deterministic, so a budget spent
// concurrently would hand identical batches different results. The ordered
// append loop is the only place where "first N bytes of this batch" means the
// same thing on every run (D1).
func shapeBatchResults(results []toolExecResult, opts Options) []string {
	bodies := make([]string, len(results))
	budget := effectiveBatchBudget(opts)
	if budget <= 0 || len(results) == 0 {
		// The default. No shaping, no allocation beyond this slice, and the
		// bytes are the ones pass 1 already built.
		for i, r := range results {
			bodies[i] = r.result
		}
		return bodies
	}
	parts := make([]resultParts, len(results))
	for i, r := range results {
		parts[i] = r.parts
	}
	shaped, report := shapeBatch(parts, budget, newShapeEnv(opts.RemainderSpool, opts.SessionID))
	for i := range shaped {
		// Hook context is re-attached AFTER shaping and outside the budget
		// check: it is framing, bounded by runtime.MaxHookContextBytes, and
		// cutting a formatter's advice to make room for it in a tool's own
		// byte budget was never the trade this layer is here to make (F5).
		bodies[i] = appendHookContext(shaped[i], parts[i].hookContext)
	}
	emitBatchShaping(opts, report)
	return bodies
}

// emitBatchShaping reports what the budget did. Content-free by construction:
// counts and byte totals only, never a fragment of a result.
//
// Silent when nothing degraded - a per-batch row on every step would bury the
// tool rows it sits among, and "the budget did nothing" is not news.
func emitBatchShaping(opts Options, report shapeReport) {
	if report.degraded == 0 {
		return
	}
	emit(opts, Event{
		Kind: EventHeartbeat,
		Detail: fmt.Sprintf("tool batch budget: %d of %d results degraded · %d/%d bytes charged",
			report.degraded, report.results, report.charged, report.budget),
	})
}

// shapeBatch charges a batch's results against budget in issue order and
// returns the shaped bodies (D6: pure but for spool I/O through env).
//
// Three tiers, in order of preference:
//
//  1. the result fits the remaining budget - emitted unchanged;
//  2. it does not, and budget remains - the ORIGINAL body is re-cut to
//     min(effectiveCap, max(remaining, floor)) with one coherent notice
//     naming the remainder and reporting the true total;
//  3. it does not, and the budget is spent - the body is replaced by that
//     notice alone (~100 bytes).
//
// Because a tier-2 re-cut always charges at least the remaining budget, at
// most ONE result per batch straddles the boundary and pays the floor; every
// result after it takes tier 3. Bytes entering history are therefore bounded
// by budget + floor + framing regardless of how many calls the batch held
// (F6) - the bound does not depend on MaxToolCallsPerBatch being set.
func shapeBatch(parts []resultParts, budget int, env shapeEnv) ([]string, shapeReport) {
	shaped := make([]string, len(parts))
	report := shapeReport{budget: budget, results: len(parts)}
	if budget <= 0 {
		// Defensive: callers gate on the budget, so this path costs the hot
		// path nothing. Charging an unlimited budget must still be a no-op.
		for i, p := range parts {
			shaped[i] = p.cappedBody
			report.charged += len(p.cappedBody)
		}
		report.remaining = 0
		return shaped, report
	}

	remaining := budget
	lastDegraded := -1
	var lastState degradeState
	for i, p := range parts {
		body, state, degraded := shapeOne(p, remaining, env)
		shaped[i] = body
		if !degraded {
			remaining -= len(body)
			if remaining < 0 {
				remaining = 0
			}
			continue
		}
		// A degrade always spends the rest of the budget, even when the built
		// envelope came in a few bytes under its target: the target was
		// already >= remaining, so what is left is notice-sized slack, not
		// budget. Carrying that slack forward would let the NEXT result claim
		// the floor as well, and "at most one straddling result pays the
		// floor" (F6) is the whole reason the bound is finite.
		remaining = 0
		report.degraded++
		lastDegraded, lastState = i, state
	}

	// D8: the status line is composed INTO the last degraded result's
	// envelope, never inserted into an already-built body. Recomposition is
	// pure - resolveDegrade did the I/O - so this costs no second spool round
	// trip and cannot change any other result's charge: everything after the
	// first degrade is already tier 3.
	if lastDegraded >= 0 {
		shaped[lastDegraded] = composeDegraded(lastState,
			statusLine(report.degraded, len(parts), remaining, budget))
	}
	report.charged = 0
	for _, body := range shaped {
		report.charged += len(body)
	}
	report.remaining = remaining
	return shaped, report
}

// shapeOne decides one result's fate against the budget left. It reports the
// body to emit, the resolved degrade behind it (so the caller can recompose
// that body with the status line without repeating the I/O), and whether the
// result was actually degraded.
//
// A degrade must buy more than it costs: replacing a body with a pointer to
// itself is only worth doing when the pointer is substantially smaller than
// what it replaces. Below that the batch saves nothing worth measuring and the
// model loses a whole result - and short bodies are exactly where the failure
// explanations live ("error: no such file"). Requiring the saving to cover the
// notice itself is the threshold.
func shapeOne(p resultParts, remaining int, env shapeEnv) (string, degradeState, bool) {
	if len(p.cappedBody) <= remaining {
		return p.cappedBody, degradeState{}, false
	}
	// The pre-check skips the spool round trip for bodies that cannot possibly
	// clear the threshold.
	minNotice := degradeMinSize(p)
	if len(p.cappedBody) <= 2*minNotice {
		return p.cappedBody, degradeState{}, false
	}
	target := 0
	if remaining > 0 {
		target = recutTarget(p, remaining)
	}
	state := resolveDegrade(p, target, env)
	body := composeDegraded(state, "")
	if len(p.cappedBody)-len(body) < minNotice {
		// Same threshold, decided against the body that was actually built: a
		// re-cut whose target lands on the per-call cap reproduces pass 1 byte
		// for byte, and paying a second notice for that is pure loss.
		return p.cappedBody, degradeState{}, false
	}
	return body, state, true
}

// refPlaceholder sizes a notice for a body that has not been spooled yet.
// Minted through contentref rather than assembled from a literal - references
// have exactly one minter, and every reference is the same length, so a real
// one over throwaway bytes measures the notice exactly.
var refPlaceholder = contentref.Reference(contentref.KindOutput, []byte("size probe"))

// degradeMinSize is the smallest body a degrade of p could produce: the
// truncation notice alone, sized for the ref that degrade would name.
func degradeMinSize(p resultParts) int {
	ref := p.refA
	switch {
	case p.ephemeral:
		ref = "" // D10: an ephemeral degrade never names a ref
	case ref == "":
		ref = refPlaceholder // a tier-2/3 degrade would spool and name one
	}
	return len(remainder.TruncationNotice(p.totalN, p.totalN, ref))
}

// recutTarget is the tier-2 envelope for one straddling result: at least the
// floor so the re-cut is worth reading, never more than the per-call cap the
// tool contracted for (F3).
func recutTarget(p resultParts, remaining int) int {
	target := remaining
	if target < BatchDegradeFloorBytes {
		target = BatchDegradeFloorBytes
	}
	if p.effectiveCap > 0 && target > p.effectiveCap {
		target = p.effectiveCap
	}
	return target
}

// degradeState is a resolved degrade: everything needed to build the body,
// with all I/O already done. Recomposing from it is pure, which is what lets
// the status line be folded into the last degraded result without a second
// load and without disturbing determinism.
type degradeState struct {
	// original is the full pre-cap body when it is available; empty means the
	// result degrades to a notice alone.
	original string
	total    int // true original size in bytes
	ref      string
	target   int // re-cut envelope; <= 0 means notice-only
	// fallback, when set, is the pass-1 body to keep whole because the
	// original could not be recovered. Cutting a pass-1 artifact further is
	// the C1 failure class - it would clip the ref embedded in its own notice
	// - so the artifact is kept intact and charged as-is instead.
	fallback string
}

func resolveDegrade(p resultParts, target int, env shapeEnv) degradeState {
	state := degradeState{target: target, total: p.totalN, ref: p.refA}
	if p.ephemeral {
		state.ref = "" // D10: never a ref, on any tier
	}
	if target <= 0 {
		// Tier 3 needs no bytes, so it never loads. An untruncated body is
		// spooled here so the notice can name it: replacing a body with a
		// notice that points nowhere would be the one thing this layer must
		// not do - destroy a result the model paid for.
		if !p.truncated && !p.ephemeral {
			state.ref = env.spool(p.cappedBody)
		}
		return state
	}
	if !p.truncated {
		state.original = p.cappedBody
		if !p.ephemeral {
			state.ref = env.spool(state.original)
		}
		return state
	}
	original, ok := env.load(p.refA)
	if !ok {
		// F1/F2: the remainder is unreadable (no spool, store fault, expired
		// grant). Keep pass 1's body whole and charge it - bounded by that
		// result's own per-call cap, and only ever one result per batch takes
		// this path.
		state.fallback = p.cappedBody
		return state
	}
	state.original = original
	return state
}

// composeDegraded builds one degraded body, charging trailer inside the same
// envelope as the content and the notice.
func composeDegraded(state degradeState, trailer string) string {
	if state.fallback != "" {
		// The remainder is unreadable, so pass 1's body is the most truthful
		// answer available. It saves nothing, so shapeOne's shrink guard turns
		// this into a passthrough and no trailer is ever composed onto it -
		// which is also why cutting it further (the C1 failure class) never
		// arises here.
		return state.fallback
	}
	if state.target <= 0 || state.original == "" {
		return trailer + remainder.TruncationNotice(0, state.total, state.ref)
	}
	return remainder.Fit(state.original, state.total, state.target, state.ref, trailer)
}

// statusLine reports what the batch budget did, in one bounded line the model
// can act on: it is the only signal that paging a ref would cost it nothing it
// has not already been charged for.
func statusLine(degraded, results, remaining, budget int) string {
	return fmt.Sprintf(
		"\n[batch result budget: %d of %d results degraded; %d of %d bytes remaining]",
		degraded, results, remaining, budget)
}

// effectiveBatchBudget resolves the operator knob for one batch: positive is
// literal, negative selects the derived budget, zero disables the mechanism.
func effectiveBatchBudget(opts Options) int {
	switch {
	case opts.BatchResultBudgetBytes > 0:
		return opts.BatchResultBudgetBytes
	case opts.BatchResultBudgetBytes < 0:
		return derivedBatchBudget(opts.MaxContextTokens)
	default:
		return unlimitedBatchBudget
	}
}

// derivedBatchBudget turns a prompt budget into a batch byte budget.
//
// With no prompt budget the derivation is inert rather than floored (C5):
// MaxContextTokens <= 0 already means "no pruning", and inventing a 256 KiB
// batch bound for a loop that opted out of context management would enforce a
// limit nobody configured.
func derivedBatchBudget(maxContextTokens int) int {
	if maxContextTokens <= 0 {
		return unlimitedBatchBudget
	}
	if maxContextTokens > maxDerivableTokens {
		maxContextTokens = maxDerivableTokens
	}
	derived := maxContextTokens * bytesPerToken / derivedBudgetShare
	if derived < derivedBatchBudgetFloorBytes {
		derived = derivedBatchBudgetFloorBytes
	}
	return derived
}
