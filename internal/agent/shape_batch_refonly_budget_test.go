package agent

import (
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// --- Ref-only elision vs the batch budget: the elision must not spend the
// --- rest of the budget (plan tools/06 + tier-1 contract).
//
// A ref-only elision (refOnlyTier in shapeOne) spools the WHOLE body before
// the budget tiers see the result and charges only its notice's bytes. The
// degraded branch of shapeBatch used to apply the tier-2 rule - "a degrade
// spends the rest of the budget" (remaining = 0) - to it, so every result
// that followed in the same batch degraded to a tier-3 notice even when it
// fit the remaining budget. That violated tier 1 ("fits the remaining budget
// -> emitted unchanged") for an input the operator explicitly opted in to
// elide. The fix charges a ref-only degrade its notice's actual bytes instead
// of zeroing the budget; these tests pin that contract and its edges.
//
// Reachability: [tools] ref_only_tools (opt-in) plus an active batch budget,
// which is the default in tool sessions (BatchResultBudgetBytes derives a
// floor of 256 KiB from the chat MaxContextTokens=1,000,000 default). A model
// that batch-calls a ref-only tool (>= 16 KiB body) with a fitting sibling
// under that budget hits the bug.

// RED gate: a ref-only elision followed by a fitting sibling must leave the
// sibling byte-identical (tier 1), degrade exactly one result, and leave the
// budget positive. Pre-fix, shapeBatch zeroed the budget on the elision, so
// the sibling was replaced by a tier-3 notice, degraded==2, remaining==0, and
// the sibling was spooled (store.Len()==2).
func TestRefOnlyElisionLeavesFittingSiblingUnchanged(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("A", big)
	sibling := strings.Repeat("B", 1<<10)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	refOnly := untruncatedPart(text, 0)
	refOnly.toolName = "read_file"
	sib := untruncatedPart(sibling, 0)
	sib.toolName = "list_dir"

	shaped, report := shapeBatch([]resultParts{refOnly, sib}, 1<<20, env)

	ref := refIn(t, shaped[0])
	if shaped[0] != refOnlyNotice("read_file", big, ref) {
		t.Fatalf("ref-only result is not exactly the notice: %q", tailOf(shaped[0]))
	}
	if strings.Contains(shaped[0], "batch result budget") {
		t.Fatalf("ref-only notice carries a status line: %q", tailOf(shaped[0]))
	}
	if data, err := spool.Load(t.Context(), shapeTestPrincipal, ref); err != nil || string(data) != text {
		t.Fatalf("ref does not page the FULL original body (err=%v, %d bytes)", err, len(data))
	}
	// The bug: the fitting sibling must be emitted unchanged (tier 1).
	if shaped[1] != sibling {
		t.Fatalf("fitting sibling after a ref-only elision degraded to %d bytes: %q",
			len(shaped[1]), tailOf(shaped[1]))
	}
	if strings.Contains(shaped[1], "... truncated: kept ") || strings.Contains(shaped[1], "ref:output:") {
		t.Fatalf("fitting sibling carries a degrade artifact: %q", tailOf(shaped[1]))
	}
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1 (only the ref-only result)", report.degraded)
	}
	if report.remaining <= 0 {
		t.Fatalf("remaining=%d after a ref-only elision, want >0", report.remaining)
	}
	if report.charged != len(shaped[0])+len(shaped[1]) {
		t.Fatalf("charged=%d, want %d", report.charged, len(shaped[0])+len(shaped[1]))
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want 1 (only the ref-only body; the sibling must not be spooled)", store.Len())
	}

	// The same contract through shapeBatchResults (model-visible bodies).
	opts := Options{
		BatchResultBudgetBytes: 1 << 20,
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	results := []toolExecResult{
		refOnlyResult("read_file", text),
		{index: 1, toolCall: toolCall("call_list_dir", "list_dir", "{}"),
			result: sibling, parts: untruncatedPart(sibling, 0)},
	}
	bodies := shapeBatchResults(results, opts)
	if bodies[0] != shaped[0] {
		t.Fatalf("shapeBatchResults ref-only body differs from shapeBatch: %q", tailOf(bodies[0]))
	}
	if bodies[1] != sibling {
		t.Fatalf("shapeBatchResults degraded the fitting sibling: %q", tailOf(bodies[1]))
	}
}

// Order-independence: a fitting sibling BEFORE the ref-only elision is emitted
// unchanged, the elision still elides to the exact notice, and the budget
// survives the elision. The ref-only notice is the last degraded result, and
// composeDegraded's fallback path returns it verbatim - no status line is
// glued onto a ref-only notice.
func TestRefOnlyElisionAsLastKeepsPriorSibling(t *testing.T) {
	spool, _ := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("C", big)
	sibling := strings.Repeat("D", 1<<10)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	sib := untruncatedPart(sibling, 0)
	sib.toolName = "list_dir"
	refOnly := untruncatedPart(text, 0)
	refOnly.toolName = "read_file"

	shaped, report := shapeBatch([]resultParts{sib, refOnly}, 1<<20, env)

	if shaped[0] != sibling {
		t.Fatalf("fitting sibling BEFORE the elision was degraded: %q", tailOf(shaped[0]))
	}
	ref := refIn(t, shaped[1])
	if shaped[1] != refOnlyNotice("read_file", big, ref) {
		t.Fatalf("last ref-only result is not exactly the notice: %q", tailOf(shaped[1]))
	}
	if strings.Contains(shaped[1], "batch result budget") {
		t.Fatalf("ref-only notice carries a status line: %q", tailOf(shaped[1]))
	}
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1", report.degraded)
	}
	if report.remaining <= 0 {
		t.Fatalf("remaining=%d, want >0", report.remaining)
	}
}

// Negative: the fix must not disable the budget tiers. With the budget at the
// floor, an over-budget sibling after a ref-only elision still cannot fit and
// is re-cut to the floor with an honest truncation notice and a remainder ref
// (degraded==2, remaining==0, charged within the F6 bound).
func TestOverBudgetSiblingStillDegradesAfterRefOnlyElision(t *testing.T) {
	spool, _ := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("E", big)
	huge := strings.Repeat("F", 2<<20)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	refOnly := untruncatedPart(text, 0)
	refOnly.toolName = "read_file"
	hugePart := untruncatedPart(huge, 0)
	hugePart.toolName = "list_dir"

	shaped, report := shapeBatch([]resultParts{refOnly, hugePart}, BatchDegradeFloorBytes, env)

	ref := refIn(t, shaped[0])
	if shaped[0] != refOnlyNotice("read_file", big, ref) {
		t.Fatalf("ref-only notice altered under a floor budget: %q", tailOf(shaped[0]))
	}
	if len(shaped[1]) > BatchDegradeFloorBytes {
		t.Fatalf("over-budget sibling kept %d bytes, over the floor %d", len(shaped[1]), BatchDegradeFloorBytes)
	}
	if !strings.Contains(shaped[1], "... truncated: kept ") {
		t.Fatalf("over-budget sibling lost its truncation notice: %q", tailOf(shaped[1]))
	}
	if !strings.Contains(shaped[1], "ref:output:") {
		t.Fatalf("over-budget sibling lost its remainder ref: %q", tailOf(shaped[1]))
	}
	if report.degraded != 2 {
		t.Fatalf("degraded=%d, want 2", report.degraded)
	}
	if report.remaining != 0 {
		t.Fatalf("remaining=%d after an over-budget straddle, want 0", report.remaining)
	}
	// F6: charged stays within budget + floor + framing regardless of batch size.
	framing := (len(remainder.TruncationNotice(math.MaxInt32, math.MaxInt32, "ref:output:"+strings.Repeat("f", 64))) + statusLineMaxBytes) * len(shaped)
	if report.charged > BatchDegradeFloorBytes+BatchDegradeFloorBytes+framing {
		t.Fatalf("charged %d, over the F6 bound %d", report.charged, BatchDegradeFloorBytes+BatchDegradeFloorBytes+framing)
	}
}

// Edge: a budget smaller than the notice clamps remaining to 0 (no negative
// carry, no panic) while the ref-only notice stays exact.
func TestTinyBudgetAfterRefOnlyElisionClampsToZero(t *testing.T) {
	spool, _ := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("G", big)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	refOnly := untruncatedPart(text, 0)
	refOnly.toolName = "read_file"

	shaped, report := shapeBatch([]resultParts{refOnly}, 64, env)

	ref := refIn(t, shaped[0])
	if shaped[0] != refOnlyNotice("read_file", big, ref) {
		t.Fatalf("ref-only notice altered under a tiny budget: %q", tailOf(shaped[0]))
	}
	if report.remaining != 0 {
		t.Fatalf("remaining=%d, want 0 (clamped)", report.remaining)
	}
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1", report.degraded)
	}
	if report.charged != len(shaped[0]) {
		t.Fatalf("charged=%d, want %d (the notice's bytes)", report.charged, len(shaped[0]))
	}
}

// Golden accounting for the single-result case that existing tests already
// pin byte-for-byte: one ref-only result charges exactly its notice, leaves
// budget - len(notice), degrades once, and carries no status line. The
// nil-spool variant (INV-AG-10) keeps the plain notice and never invents a
// ref, with the same accounting shape.
func TestSingleRefOnlyAccountingUnchanged(t *testing.T) {
	spool, _ := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("H", big)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	refOnly := untruncatedPart(text, 0)
	refOnly.toolName = "read_file"

	shaped, report := shapeBatch([]resultParts{refOnly}, 1<<20, env)
	ref := refIn(t, shaped[0])
	want := refOnlyNotice("read_file", big, ref)
	if shaped[0] != want {
		t.Fatalf("single ref-only result is not the exact notice: %q", tailOf(shaped[0]))
	}
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1", report.degraded)
	}
	if report.charged != len(want) {
		t.Fatalf("charged=%d, want %d (the notice's bytes)", report.charged, len(want))
	}
	if report.remaining != (1<<20)-len(want) {
		t.Fatalf("remaining=%d, want %d", report.remaining, (1<<20)-len(want))
	}
	if strings.Contains(shaped[0], "batch result budget") {
		t.Fatalf("ref-only notice carries a status line: %q", tailOf(shaped[0]))
	}

	// INV-AG-10 nil-spool variant: plain notice, no ref invented, same shape.
	nilEnv := newShapeEnv(nil, shapeTestPrincipal)
	nilEnv.refOnlyTools = []string{"read_file"}
	plain, plainReport := shapeBatch([]resultParts{refOnly}, 1<<20, nilEnv)
	wantPlain := refOnlyPlainNotice("read_file", big)
	if plain[0] != wantPlain {
		t.Fatalf("nil-spool single result = %q, want %q", tailOf(plain[0]), wantPlain)
	}
	if strings.Contains(plain[0], "ref:output:") || strings.Contains(plain[0], "read_output") {
		t.Fatal("a ref was invented without a spool")
	}
	if plainReport.degraded != 1 || plainReport.charged != len(wantPlain) {
		t.Fatalf("nil-spool accounting wrong: degraded=%d charged=%d", plainReport.degraded, plainReport.charged)
	}
}

// Empty input: shapeBatch of no results is a zero-cost no-op on every branch
// (unlimited, tiny, and 1 MiB budgets) - no panic, nothing charged.
func TestShapeBatchEmptyPartsNoPanic(t *testing.T) {
	spool, _ := testSpool(t)
	for _, budget := range []int{0, 64, 1 << 20} {
		shaped, report := shapeBatch(nil, budget, newShapeEnv(spool, shapeTestPrincipal))
		if len(shaped) != 0 || report.degraded != 0 || report.charged != 0 {
			t.Fatalf("budget=%d empty batch: shaped=%d degraded=%d charged=%d",
				budget, len(shaped), report.degraded, report.charged)
		}
		if budget <= 0 {
			if report.remaining != 0 {
				t.Fatalf("budget=%d empty batch remaining=%d, want 0", budget, report.remaining)
			}
		} else if report.remaining != budget {
			t.Fatalf("budget=%d empty batch remaining=%d, want %d", budget, report.remaining, budget)
		}
	}
}

// Duplicate input: two IDENTICAL ref-only results in one batch are both
// elided to the exact notice. The spool is content-addressed, so the identical
// body dedups to one ref and one stored entry (INV-AG-10 keeps the notice
// naming the same remainder, never a second spool).
func TestTwoIdenticalRefOnlyResultsBothElided(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("I", big)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	one := untruncatedPart(text, 0)
	one.toolName = "read_file"
	two := untruncatedPart(text, 0)
	two.toolName = "read_file"

	shaped, report := shapeBatch([]resultParts{one, two}, 1<<20, env)

	ref := refIn(t, shaped[0])
	want := refOnlyNotice("read_file", big, ref)
	if shaped[0] != want || shaped[1] != want {
		t.Fatalf("identical ref-only results not both elided to the exact notice:\n0 %q\n1 %q",
			tailOf(shaped[0]), tailOf(shaped[1]))
	}
	if report.degraded != 2 {
		t.Fatalf("degraded=%d, want 2", report.degraded)
	}
	if report.remaining != (1<<20)-2*len(want) {
		t.Fatalf("remaining=%d, want %d", report.remaining, (1<<20)-2*len(want))
	}
	if report.charged != 2*len(want) {
		t.Fatalf("charged=%d, want %d", report.charged, 2*len(want))
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want 1 (identical content dedups to one ref)", store.Len())
	}
	if data, err := spool.Load(t.Context(), shapeTestPrincipal, ref); err != nil || string(data) != text {
		t.Fatalf("ref does not page the full body (err=%v)", err)
	}
}

// D10 interaction: a ref-only elision followed by an ephemeral sibling. The
// ref-only notice carries its ref; the ephemeral re-cut carries NO ref and
// never the 'elided' wording - only the honest truncation notice. Only the
// ref-only body is ever stored.
func TestEphemeralSiblingAfterRefOnlyElision(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("J", big)
	ephemeralText := strings.Repeat("K", 2<<20)

	env := newShapeEnv(spool, shapeTestPrincipal)
	env.refOnlyTools = []string{"read_file"}
	refOnly := untruncatedPart(text, 0)
	refOnly.toolName = "read_file"
	ephemeral := untruncatedPart(ephemeralText, 0)
	ephemeral.toolName = "list_dir"
	ephemeral.ephemeral = true

	shaped, report := shapeBatch([]resultParts{refOnly, ephemeral}, 1<<20, env)

	ref := refIn(t, shaped[0])
	if shaped[0] != refOnlyNotice("read_file", big, ref) {
		t.Fatalf("ref-only notice altered: %q", tailOf(shaped[0]))
	}
	if strings.Contains(shaped[1], "ref:output:") {
		t.Fatalf("ephemeral sibling was put behind a ref: %q", tailOf(shaped[1]))
	}
	if strings.Contains(shaped[1], "elided") {
		t.Fatalf("ephemeral sibling got ref-only wording: %q", tailOf(shaped[1]))
	}
	if !strings.Contains(shaped[1], "... truncated: kept ") {
		t.Fatalf("ephemeral sibling lost its honest truncation notice: %q", tailOf(shaped[1]))
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want 1 (only the ref-only body; an ephemeral body is never stored)", store.Len())
	}
	if report.degraded != 2 {
		t.Fatalf("degraded=%d, want 2", report.degraded)
	}
	if report.remaining != 0 {
		t.Fatalf("remaining=%d after the ephemeral straddle, want 0", report.remaining)
	}
}

// FuzzShapeBatchBudgetAccounting pins the accounting invariants of shapeBatch
// under arbitrary (budget, result mix) combinations: shaped length, non-empty
// bodies, remaining >= 0, degraded bounds, charged == sum of bodies,
// passthrough on budget <= 0, the F6 charged bound, degraded == count of
// changed bodies, no partial refs, and that every ref-only-eligible result is
// elided. shapeBatch is pure over ints/strings with an injectable in-memory
// spool, so this is deterministic and cheap; the host runs it for a bounded
// 30s window as part of the code_validate gate.
func FuzzShapeBatchBudgetAccounting(f *testing.F) {
	seedShapeBatchBudgetCorpus(f)

	const safeAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 \n"

	f.Fuzz(func(t *testing.T, budget int, flags int, seedA, seedB int64, payload []byte) {
		// payload only seeds the PRNG: content is drawn from safeAlphabet so
		// no generated body can contain a "ref:output:"-shaped false positive
		// for assertNoPartialRef.
		rng := rand.New(rand.NewSource(seedA ^ (seedB << 1) ^ int64(flags) ^ int64(len(payload))))
		count := rng.Intn(9) // 0..8 results
		refOnly := flags&1 != 0

		spool, _ := testSpool(t)
		env := newShapeEnv(spool, shapeTestPrincipal)
		if refOnly {
			env.refOnlyTools = []string{"read_file"}
		}

		parts, refOnlyEligible := fuzzShapeParts(t, spool, rng, count, refOnly, budget, safeAlphabet)
		shaped, report := shapeBatch(parts, budget, env)

		checkShapeBatchFuzzInvariants(t, parts, shaped, report, budget, refOnlyEligible)
	})
}

// seedShapeBatchBudgetCorpus seeds the fuzzer with one of each interesting
// shape: fitting batches, unlimited and negative budgets, a ref-only elision
// with a fitting sibling, a floor-budget straddle after a ref-only elision, a
// tiny budget, a max-int budget, a ref-only + ephemeral pair, and an empty
// payload.
func seedShapeBatchBudgetCorpus(f *testing.F) {
	f.Add(1<<20, 0, int64(42), int64(7), []byte("fitting batch"))
	f.Add(0, 0, int64(42), int64(7), []byte("unlimited passthrough"))
	f.Add(-1, 0, int64(42), int64(7), []byte("negative passthrough"))
	f.Add(1<<20, 1, int64(42), int64(7), []byte("ref-only + fitting sibling"))
	f.Add(BatchDegradeFloorBytes, 1, int64(42), int64(7), []byte("floor budget straddle after ref-only"))
	f.Add(64, 1, int64(42), int64(7), []byte("tiny budget ref-only elision"))
	f.Add(math.MaxInt, 0, int64(42), int64(7), []byte("max-int budget"))
	f.Add(1<<20, 3, int64(42), int64(7), []byte("ref-only + ephemeral sibling"))
	f.Add(1<<20, 1, int64(42), int64(7), []byte(""))
}

// fuzzShapeParts draws (count, size, ephemeral, truncation, tool) mixes from a
// deterministic rng and builds the pass-1 results for them. Sizes come from a
// boundary set around 0, the degrade floor, and the budget, clamped to 2 MiB
// so one iteration never allocates beyond the intended max body; every fourth
// draw uses a random size up to 256 KiB. Returns the parts and a per-index
// flag marking the results the ref-only tier must elide.
func fuzzShapeParts(t *testing.T, spool *remainder.Spool, rng *rand.Rand, count int, refOnly bool, budget int, safeAlphabet string) ([]resultParts, []bool) {
	boundarySizes := []int{1, 2, BatchDegradeFloorBytes - 1, BatchDegradeFloorBytes,
		BatchDegradeFloorBytes + 1, 1 << 12, 1 << 16, 1 << 20, 1 << 21}
	if budget > 0 {
		if budget < math.MaxInt {
			boundarySizes = append(boundarySizes, budget-1, budget, budget+1)
		} else {
			boundarySizes = append(boundarySizes, budget-1, budget)
		}
	}

	fill := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = safeAlphabet[rng.Intn(len(safeAlphabet))]
		}
		return string(b)
	}

	parts := make([]resultParts, count)
	refOnlyEligible := make([]bool, count)
	for i := 0; i < count; i++ {
		size := boundarySizes[rng.Intn(len(boundarySizes))]
		if size < 1 {
			size = 1
		}
		if size > 1<<21 {
			// A fuzzer-mutated budget can put absurd boundary sizes in
			// the set; clamp so one iteration never allocates beyond the
			// intended max body (2 MiB).
			size = 1 << 21
		}
		if rng.Intn(4) == 0 {
			size = 1 + rng.Intn(1<<18)
		}
		text := fill(size)
		ephemeral := rng.Intn(8) == 0
		truncated := rng.Intn(8) == 0 && size > 1 && size <= 1<<16
		name := "list_dir"
		if refOnly && rng.Intn(3) != 0 {
			name = "read_file"
		}
		var p resultParts
		if truncated {
			p = capPart(t, spool, text, size/2)
		} else {
			cap := 0
			if rng.Intn(4) == 0 {
				cap = 1 << (8 + rng.Intn(12))
			}
			p = untruncatedPart(text, cap)
		}
		p.toolName = name
		p.ephemeral = ephemeral
		parts[i] = p
		refOnlyEligible[i] = refOnly && name == "read_file" &&
			p.totalN >= BatchDegradeFloorBytes && !ephemeral
	}
	return parts, refOnlyEligible
}

// checkShapeBatchFuzzInvariants pins the accounting contract of shapeBatch
// over one fuzz iteration: shaped length, non-empty bodies, remaining >= 0,
// degraded bounds, charged == sum of bodies, passthrough on budget <= 0, the
// F6 charged bound, degraded == count of changed bodies, no partial refs, and
// that every ref-only-eligible result is elided.
func checkShapeBatchFuzzInvariants(t *testing.T, parts []resultParts, shaped []string, report shapeReport, budget int, refOnlyEligible []bool) {
	t.Helper()
	if len(shaped) != len(parts) {
		t.Fatalf("shaped %d bodies for %d parts", len(shaped), len(parts))
	}
	for i, s := range shaped {
		if s == "" {
			t.Fatalf("result %d emitted an empty body", i)
		}
		assertNoPartialRef(t, s)
	}
	if report.remaining < 0 {
		t.Fatalf("remaining=%d < 0", report.remaining)
	}
	if report.degraded < 0 || report.degraded > report.results {
		t.Fatalf("degraded=%d out of range for %d results", report.degraded, report.results)
	}
	sum := 0
	for _, s := range shaped {
		sum += len(s)
	}
	if report.charged != sum {
		t.Fatalf("charged=%d, want sum of bodies %d", report.charged, sum)
	}
	if budget <= 0 {
		if report.degraded != 0 {
			t.Fatalf("budget=%d degraded %d results, want passthrough", budget, report.degraded)
		}
		for i := range parts {
			if shaped[i] != parts[i].cappedBody {
				t.Fatalf("budget=%d altered result %d", budget, i)
			}
		}
		return
	}
	// F6: with the budget armed, charged stays within budget + floor +
	// framing no matter how many (or how few) results the batch held.
	framing := (len(remainder.TruncationNotice(math.MaxInt32, math.MaxInt32, "ref:output:"+strings.Repeat("f", 64))) + statusLineMaxBytes) * len(parts)
	// Saturate the F6 bound: at a budget within floor+framing of int64
	// max the literal sum budget+floor+framing overflows to a NEGATIVE
	// number, and a negative bound fails every batch (charged is always
	// positive). The mathematical bound is unreachable there - charged is
	// bounded by the per-body limits, and every body fits tier 1 - so
	// clamping to MaxInt keeps the check exact where it can fail and
	// vacuous where it cannot.
	f6 := budget
	if BatchDegradeFloorBytes > math.MaxInt-f6 {
		f6 = math.MaxInt
	} else {
		f6 += BatchDegradeFloorBytes
	}
	if framing > math.MaxInt-f6 {
		f6 = math.MaxInt
	} else {
		f6 += framing
	}
	if report.charged > f6 {
		t.Fatalf("charged %d over the F6 bound %d (budget %d)", report.charged, f6, budget)
	}
	deg := 0
	for i := range parts {
		if shaped[i] != parts[i].cappedBody {
			deg++
		}
	}
	if report.degraded != deg {
		t.Fatalf("degraded=%d but %d bodies differ from pass 1", report.degraded, deg)
	}
	for i, p := range parts {
		if refOnlyEligible[i] && !strings.Contains(shaped[i], "elided") {
			t.Fatalf("ref-only-eligible result %d (%s, %d bytes) not elided: %q",
				i, p.toolName, p.totalN, tailOf(shaped[i]))
		}
	}
}
