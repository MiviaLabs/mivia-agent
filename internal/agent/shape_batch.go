package agent

import (
	"context"
	"math"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
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

	// readOutputToolName is the model-facing reader for remainder refs
	// (internal/clichat.readOutputTool). It is already a bounded, paginated
	// reader over content the batch/turn shaper itself spooled (32 KiB page /
	// 256 KiB result cap - see internal/clichat/read_output.go), so it is
	// exempt from the degrade tiers below: re-truncating it into ANOTHER
	// remainder ref would send the model chasing a ref through its own
	// recovery tool, which is the "ref-to-ref" failure this constant exists
	// to prevent. agent cannot import clichat (clichat depends on agent), so
	// the name is duplicated here rather than shared.
	readOutputToolName = "read_output"

	// zeroBudgetPreviewBytes is the small per-result preview a tail result
	// gets once the batch/turn budget itself is fully spent (remaining <= 0).
	// Without it, EVERY result after the one straddling result degrades to a
	// bare "kept 0" notice - even a result whose whole body was a few KB -
	// forcing a read_output round trip for content that would have fit
	// trivially. Funded from tailPreviewReserveBytes, not from the primary
	// budget, so this never changes the primary budget's own bound.
	// Sized to match the common case observed in practice: a long-running
	// agent's batches/turns commonly carry a dozen-plus small results (greps,
	// small file reads, inspect_repository/read_output pages in the few-KB
	// range) after the one straddler pays the floor. 512 B was too thin to
	// be worth reading on its own; 2 KiB mirrors the preview size other
	// coding-agent harnesses use for an over-budget tool result.
	zeroBudgetPreviewBytes = 2 << 10

	// tailPreviewReserveBytes bounds the TOTAL bytes zeroBudgetPreviewBytes
	// may spend across one entire batch/turn, independent of how many results
	// degrade to zero. Without this cap, a batch of many tiny results would
	// each claim a preview and the aggregate bound (F6) would stop being a
	// small constant - it would scale with batch size. Once the reserve is
	// spent, later tail results return to a bare notice exactly as before.
	// 256 KiB at the 2 KiB preview above covers ~128 post-exhaustion results
	// before falling back. Raised from the original 32 KiB (~16 results):
	// a long exploration turn (dozens of grep/read_file calls past the
	// primary budget) was observed exhausting the 16-call cushion partway
	// through, after which every remaining call in the turn - however small -
	// degraded to a bare "kept 0" notice and paid a read_output round trip,
	// even though each individual result was well within a single preview.
	// Still a small fraction of the 256 KiB budget floor it rides alongside.
	tailPreviewReserveBytes = 256 << 10
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
	// toolName is the tool that produced this result, stamped from the call
	// by shapeBatchResults. Pass 1 does not build it: the ref-only tier is
	// the only reader, and it decides per batch whether a result is inlined
	// or elided, so the name rides on the structured parts rather than being
	// glued into the body.
	toolName string
}

// shapeStore is the spool capability shapeBatch needs, narrowed to keep the
// enforcement core injectable and testable without a ledger.
type shapeStore interface {
	Spool(ctx context.Context, principal string, data []byte) string
	Load(ctx context.Context, principal, ref string) ([]byte, error)
}

// shapeEnv carries the only I/O shapeBatch performs, plus the ref-only tool
// set that decides whether a result is inlined at all (plan tools/06). The
// set is configuration rather than I/O, but shapeOne receives the env - and
// shapeBatch's signature is pinned by tests - so the set rides here, empty
// meaning the tier is off.
type shapeEnv struct {
	store     shapeStore
	principal string
	// refOnlyTools lists tool names whose results are never inlined: when a
	// result's raw body clears the floor and the result is not ephemeral,
	// the WHOLE body is spooled and replaced by a notice naming the
	// remainder. Empty means every result keeps the budget tiers.
	refOnlyTools []string
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
//
// previewReserve is the reserve pool left for the zero-budget preview tier
// (see tailPreviewReserveBytes); the fourth return value is how much of it
// this call spent, which is always 0 unless that tier fired.
func shapeOne(p resultParts, remaining, previewReserve int, env shapeEnv) (string, degradeState, bool, int) {
	// Ref-only tier (plan tools/06): a tool opted out of inlining is elided
	// before the budget tiers see it. The notice is carried in fallback so
	// the D8 status-line recomposition returns it verbatim, and refOnly marks
	// the elision so shapeBatch charges its notice's bytes instead of
	// spending the rest of the budget on it.
	if notice, ok := refOnlyTier(env, p, p.toolName); ok {
		return notice, degradeState{fallback: notice, refOnly: true}, true, 0
	}
	// read_output is the model's own recovery path for a degraded result
	// (see readOutputToolName). Degrading ITS result would hand the model a
	// ref to a ref instead of the page it asked for, so it is charged like
	// any tier-1 result that fits and never re-cut.
	if p.toolName == readOutputToolName {
		return p.cappedBody, degradeState{}, false, 0
	}
	if len(p.cappedBody) <= remaining {
		return p.cappedBody, degradeState{}, false, 0
	}
	// The pre-check skips the spool round trip for bodies that cannot possibly
	// clear the threshold.
	minNotice := degradeMinSize(p)
	if len(p.cappedBody) <= 2*minNotice {
		return p.cappedBody, degradeState{}, false, 0
	}
	target := 0
	previewUsed := 0
	switch {
	case remaining > 0:
		target = recutTarget(p, remaining)
	case previewReserve > 0:
		// The primary budget is spent, but a small reserve remains: keep a
		// short preview instead of collapsing straight to a bare notice.
		target = zeroBudgetPreviewBytes
		if p.effectiveCap > 0 && target > p.effectiveCap {
			target = p.effectiveCap
		}
		if target > previewReserve {
			target = previewReserve
		}
		previewUsed = target
	}
	state := resolveDegrade(p, target, env)
	body := composeDegraded(state, "")
	if len(p.cappedBody)-len(body) < minNotice {
		// Same threshold, decided against the body that was actually built: a
		// re-cut whose target lands on the per-call cap reproduces pass 1 byte
		// for byte, and paying a second notice for that is pure loss.
		return p.cappedBody, degradeState{}, false, 0
	}
	return body, state, true, previewUsed
}

// refPlaceholder sizes a notice for a body that has not been spooled yet.
// Minted through sdkadapter rather than assembled from a literal - references
// have exactly one minter, and every reference is the same length, so a real
// one over throwaway bytes measures the notice exactly.
var refPlaceholder = sdkadapter.Mint(sdkadapter.KindOutput, []byte("size probe"))

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
	// refOnly marks a degrade that came from the ref-only tier (plan
	// tools/06): the whole body was already spooled before the budget tiers
	// saw it, and the notice's bytes are the only charge. It is not a
	// budget-tier straddle, so it does not spend the rest of the budget:
	// results that fit after it are still emitted unchanged (tier 1).
	refOnly bool
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
