package agent

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

const shapeTestPrincipal = "session-shape"

func testSpool(t *testing.T) (*remainder.Spool, *remainder.MemoryStore) {
	t.Helper()
	store := remainder.NewMemoryStore()
	return remainder.NewSpool(store), store
}

// body returns n bytes of deterministic, ref-free filler.
func body(n int) string { return strings.Repeat("x", n) }

// untruncatedPart is what a worker produces for a result that fit its own cap.
func untruncatedPart(text string, cap int) resultParts {
	return resultParts{cappedBody: text, totalN: len(text), effectiveCap: cap}
}

// capPart runs the real pass-1 cap so tests compose against production
// truncation rather than a hand-rolled imitation of it.
func capPart(t *testing.T, spool *remainder.Spool, text string, cap int) resultParts {
	t.Helper()
	capped, ref, truncated := remainder.CapWithSpoolRef(spool, shapeTestPrincipal, text, cap)
	return resultParts{
		cappedBody: capped, refA: ref, totalN: len(text),
		effectiveCap: cap, truncated: truncated,
	}
}

func TestShapeBatchLeavesAFittingBatchUntouched(t *testing.T) {
	spool, _ := testSpool(t)
	parts := []resultParts{
		untruncatedPart(body(100), 0),
		untruncatedPart(body(200), 0),
		untruncatedPart(body(300), 0),
	}
	shaped, report := shapeBatch(parts, 4096, newShapeEnv(spool, shapeTestPrincipal))

	for i, p := range parts {
		if shaped[i] != p.cappedBody {
			t.Fatalf("result %d was altered while under budget: len=%d want %d", i, len(shaped[i]), len(p.cappedBody))
		}
	}
	if report.degraded != 0 {
		t.Fatalf("degraded=%d, want 0", report.degraded)
	}
	if report.charged != 600 {
		t.Fatalf("charged=%d, want 600", report.charged)
	}
	if report.remaining != 4096-600 {
		t.Fatalf("remaining=%d, want %d", report.remaining, 4096-600)
	}
	for _, s := range shaped {
		if strings.Contains(s, "batch result budget") {
			t.Fatal("status line emitted for a batch where nothing degraded")
		}
	}
}

// The straddling result is the only one allowed to overshoot, and it must
// overshoot to the floor rather than to the arithmetic remainder: a result cut
// to the last 40 bytes of budget is a notice with a rounding error attached.
func TestShapeBatchStraddlingResultIsRecutToTheFloor(t *testing.T) {
	spool, _ := testSpool(t)
	const budget = 2048
	parts := []resultParts{
		untruncatedPart(body(1024), 0),
		untruncatedPart(body(512<<10), 0),
	}
	shaped, report := shapeBatch(parts, budget, newShapeEnv(spool, shapeTestPrincipal))

	if shaped[0] != parts[0].cappedBody {
		t.Fatal("first result degraded although it fit")
	}
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1", report.degraded)
	}
	if len(shaped[1]) <= budget-1024 {
		t.Fatalf("straddling result kept %d bytes, want the %d-byte floor", len(shaped[1]), BatchDegradeFloorBytes)
	}
	if len(shaped[1]) > BatchDegradeFloorBytes {
		t.Fatalf("straddling result kept %d bytes, over the floor %d", len(shaped[1]), BatchDegradeFloorBytes)
	}
	if !strings.Contains(shaped[1], fmt.Sprintf("of %d bytes", 512<<10)) {
		t.Fatalf("notice does not report the TRUE original total; tail=%q", tailOf(shaped[1]))
	}
	if !strings.Contains(shaped[1], "ref:output:") {
		t.Fatalf("degraded result carries no remainder ref; tail=%q", tailOf(shaped[1]))
	}
	if report.remaining != 0 {
		t.Fatalf("remaining=%d after an over-budget re-cut, want 0", report.remaining)
	}
}

// Everything after the straddler degrades to a notice, so batch size cannot
// buy the model more bytes (F6). This is the invariant that makes the bound
// hold with MaxToolCallsPerBatch unset.
func TestShapeBatchTailDegradesToNoticeOnlyAndStaysBounded(t *testing.T) {
	spool, _ := testSpool(t)
	const budget = 64 << 10
	parts := make([]resultParts, 200)
	for i := range parts {
		parts[i] = untruncatedPart(body(128<<10), 0)
	}
	shaped, report := shapeBatch(parts, budget, newShapeEnv(spool, shapeTestPrincipal))

	if report.degraded != len(parts) {
		t.Fatalf("degraded=%d, want %d", report.degraded, len(parts))
	}
	framing := (len(remainder.TruncationNotice(math.MaxInt32, math.MaxInt32, "ref:output:"+strings.Repeat("f", 64))) + statusLineMaxBytes) * len(parts)
	bound := budget + BatchDegradeFloorBytes + framing
	if report.charged > bound {
		t.Fatalf("charged %d bytes, over the finite bound %d", report.charged, bound)
	}
	// The real point: the tail is notices, not bodies.
	for i := 1; i < len(shaped); i++ {
		if len(shaped[i]) > 256 {
			t.Fatalf("tail result %d kept %d bytes, want a notice-only degrade", i, len(shaped[i]))
		}
		if !strings.Contains(shaped[i], "ref:output:") {
			t.Fatalf("tail result %d was destroyed instead of referenced: %q", i, shaped[i])
		}
	}
	if got := strings.Count(joinAll(shaped), "batch result budget"); got != 1 {
		t.Fatalf("status line appears %d times, want exactly 1 (last degraded result)", got)
	}
	if !strings.Contains(shaped[len(shaped)-1], "batch result budget") {
		t.Fatal("status line is not on the last degraded result")
	}
}

// F3: shaping may only ever shrink a result. A tool that declared a 4 KiB
// result budget must not come back holding the 16 KiB floor.
func TestShapeBatchNeverInflatesPastThePerCallCap(t *testing.T) {
	spool, _ := testSpool(t)
	const smallCap = 4 << 10
	capped := capPart(t, spool, body(256<<10), smallCap)
	parts := []resultParts{
		untruncatedPart(body(1<<10), 0),
		capped,
	}
	shaped, _ := shapeBatch(parts, 1<<10+16, newShapeEnv(spool, shapeTestPrincipal))
	if len(shaped[1]) > smallCap {
		t.Fatalf("re-cut produced %d bytes for a %d-byte-cap tool", len(shaped[1]), smallCap)
	}
}

// D9/C1: a result pass 1 already truncated is re-cut from the ORIGINAL bytes
// loaded back through the spool, and emits exactly ONE notice. Cutting the
// pass-1 artifact instead would clip the ref embedded in its own notice.
func TestShapeBatchRecutsTheOriginalNotThePassOneArtifact(t *testing.T) {
	spool, _ := testSpool(t)
	original := strings.Repeat("A", 200<<10)
	capped := capPart(t, spool, original, 128<<10)
	if !capped.truncated || capped.refA == "" {
		t.Fatalf("precondition: pass 1 did not truncate+spool (truncated=%v ref=%q)", capped.truncated, capped.refA)
	}
	parts := []resultParts{untruncatedPart(body(1<<10), 0), capped}

	shaped, _ := shapeBatch(parts, (1<<10)+1, newShapeEnv(spool, shapeTestPrincipal))
	got := shaped[1]

	if n := strings.Count(got, "... truncated: kept "); n != 1 {
		t.Fatalf("shaped result carries %d truncation notices, want exactly 1: tail=%q", n, tailOf(got))
	}
	if !strings.Contains(got, fmt.Sprintf("of %d bytes", len(original))) {
		t.Fatalf("notice lost the true original total; tail=%q", tailOf(got))
	}
	if !strings.Contains(got, capped.refA) {
		t.Fatalf("shaped result names a different ref than pass 1 minted; tail=%q", tailOf(got))
	}
	// The kept content must be a prefix of the ORIGINAL, and must not contain
	// the pass-1 notice text anywhere in the middle.
	if !strings.HasPrefix(original, keptContent(got)) {
		t.Fatal("kept content is not a prefix of the original body")
	}
	assertNoPartialRef(t, got)
}

// C1's other half: no shaped body may contain a clipped "ref:output:" token.
// A partial ref reads as a real one and sends the model to read_output with a
// key that cannot resolve.
func TestShapeBatchNeverEmitsAPartialRef(t *testing.T) {
	spool, _ := testSpool(t)
	for _, budget := range []int{1, 64, 200, 1 << 10, 16 << 10, 100 << 10} {
		parts := []resultParts{
			capPart(t, spool, strings.Repeat("B", 300<<10), 64<<10),
			untruncatedPart(strings.Repeat("C", 90<<10), 0),
			capPart(t, spool, strings.Repeat("D", 120<<10), 32<<10),
		}
		shaped, _ := shapeBatch(parts, budget, newShapeEnv(spool, shapeTestPrincipal))
		for i, s := range shaped {
			assertNoPartialRef(t, s)
			if len(s) == 0 {
				t.Fatalf("budget=%d result %d was emptied", budget, i)
			}
		}
	}
}

// F1/F2 fallback: when the remainder cannot be read back, the pass-1 body is
// kept whole. Bounded by that result's own per-call cap, and never
// string-surgeried.
func TestShapeBatchKeepsPassOneBodyWhenRemainderLoadFails(t *testing.T) {
	spool, store := testSpool(t)
	capped := capPart(t, spool, strings.Repeat("E", 300<<10), 32<<10)
	if capped.refA == "" {
		t.Fatal("precondition: pass 1 minted no ref")
	}
	store.Delete(capped.refA)

	parts := []resultParts{untruncatedPart(body(512), 0), capped}
	shaped, _ := shapeBatch(parts, 600, newShapeEnv(spool, shapeTestPrincipal))

	want := capped.cappedBody
	if !strings.HasPrefix(shaped[1], want) {
		t.Fatalf("pass-1 body was cut after a failed load: len=%d want prefix len=%d", len(shaped[1]), len(want))
	}
	assertNoPartialRef(t, shaped[1])
}

// D10: an ephemeral result is charged like any other body but never spooled.
// A ref would let the model page back, via read_output, exactly the bytes
// ScrubEphemeralToolMessages exists to remove from history.
func TestShapeBatchNeverPutsAnEphemeralBodyBehindARef(t *testing.T) {
	spool, store := testSpool(t)
	ephemeral := untruncatedPart(strings.Repeat("F", 200<<10), 0)
	ephemeral.ephemeral = true
	parts := []resultParts{
		untruncatedPart(body(1<<10), 0),
		ephemeral,
		func() resultParts { p := untruncatedPart(strings.Repeat("G", 90<<10), 0); p.ephemeral = true; return p }(),
	}
	shaped, report := shapeBatch(parts, 2<<10, newShapeEnv(spool, shapeTestPrincipal))

	if report.degraded != 2 {
		t.Fatalf("degraded=%d, want 2", report.degraded)
	}
	for _, i := range []int{1, 2} {
		if strings.Contains(shaped[i], "ref:output:") {
			t.Fatalf("ephemeral result %d was put behind a ref: %q", i, tailOf(shaped[i]))
		}
		if !strings.Contains(shaped[i], "... truncated: kept ") {
			t.Fatalf("ephemeral result %d lost its honest notice: %q", i, tailOf(shaped[i]))
		}
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("%d bodies were spooled for an all-ephemeral degrade, want 0", got)
	}
}

func TestShapeBatchIsDeterministic(t *testing.T) {
	spool, _ := testSpool(t)
	build := func() []resultParts {
		return []resultParts{
			untruncatedPart(strings.Repeat("H", 40<<10), 0),
			capPart(t, spool, strings.Repeat("I", 300<<10), 64<<10),
			untruncatedPart(strings.Repeat("J", 90<<10), 0),
			func() resultParts { p := untruncatedPart(strings.Repeat("K", 10<<10), 0); p.ephemeral = true; return p }(),
		}
	}
	env := newShapeEnv(spool, shapeTestPrincipal)
	first, firstReport := shapeBatch(build(), 48<<10, env)
	second, secondReport := shapeBatch(build(), 48<<10, env)
	if firstReport != secondReport {
		t.Fatalf("reports differ: %+v vs %+v", firstReport, secondReport)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("result %d differs between identical runs", i)
		}
	}
}

func TestShapeBatchStatusLineIsBounded(t *testing.T) {
	for _, args := range [][4]int{
		{0, 0, 0, 0},
		{math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt},
		{math.MinInt, math.MinInt, math.MinInt, math.MinInt},
	} {
		if got := len(statusLine(args[0], args[1], args[2], args[3])); got > statusLineMaxBytes {
			t.Fatalf("status line is %d bytes for %v, over the declared bound %d", got, args, statusLineMaxBytes)
		}
	}
}

// The status line rides INSIDE the envelope that was budgeted (D8/F3): a line
// appended after the fact would push a capped result past its own cap.
func TestShapeBatchStatusLineStaysInsideTheCappedEnvelope(t *testing.T) {
	spool, _ := testSpool(t)
	// The cap must exceed the floor, or the re-cut target lands on the cap and
	// reproduces pass 1 exactly - a no-op degrade, with nothing to carry the
	// status line.
	const cap = 20 << 10
	parts := []resultParts{capPart(t, spool, strings.Repeat("L", 256<<10), cap)}
	shaped, _ := shapeBatch(parts, 512, newShapeEnv(spool, shapeTestPrincipal))
	if !strings.Contains(shaped[0], "batch result budget") {
		t.Fatalf("only degraded result carries no status line: %q", tailOf(shaped[0]))
	}
	if len(shaped[0]) > BatchDegradeFloorBytes {
		t.Fatalf("status line pushed the result to %d bytes, past the %d envelope it was budgeted",
			len(shaped[0]), BatchDegradeFloorBytes)
	}
	assertNoPartialRef(t, shaped[0])
}

// A re-cut that cannot shrink a result is not performed: repeating pass 1's
// own output with a second notice attached costs bytes and buys nothing.
func TestShapeBatchDoesNotRecutWhenItCannotShrinkTheResult(t *testing.T) {
	spool, _ := testSpool(t)
	const cap = 2 << 10 // below the floor: the re-cut target clamps to it
	part := capPart(t, spool, strings.Repeat("N", 64<<10), cap)
	shaped, report := shapeBatch([]resultParts{part}, 512, newShapeEnv(spool, shapeTestPrincipal))
	if shaped[0] != part.cappedBody {
		t.Fatalf("no-op re-cut rewrote the result: %d bytes vs %d", len(shaped[0]), len(part.cappedBody))
	}
	if report.degraded != 0 {
		t.Fatalf("degraded=%d for a re-cut that changed nothing, want 0", report.degraded)
	}
	if n := strings.Count(shaped[0], "... truncated: kept "); n != 1 {
		t.Fatalf("result carries %d notices, want 1", n)
	}
}

// A short body - the shape every synthesized error takes - is charged whole
// rather than replaced by a notice that would be no smaller and would drop the
// only explanation the model gets (C7).
func TestShapeBatchKeepsShortBodiesInsteadOfPointingAtThem(t *testing.T) {
	spool, store := testSpool(t)
	const errText = "error: read_file: open nope.txt: no such file or directory"
	parts := []resultParts{
		untruncatedPart(strings.Repeat("O", 100<<10), 0),
		untruncatedPart(errText, 0),
	}
	shaped, report := shapeBatch(parts, 1<<10, newShapeEnv(spool, shapeTestPrincipal))
	if shaped[1] != errText {
		t.Fatalf("short error body was degraded to %q", shaped[1])
	}
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1 (only the large result)", report.degraded)
	}
	if store.Len() != 1 {
		t.Fatalf("%d bodies spooled, want 1 - a body kept whole must not be spooled", store.Len())
	}
}

func TestShapeBatchWithNoSpoolStillDegradesHonestly(t *testing.T) {
	parts := []resultParts{
		untruncatedPart(body(1<<10), 0),
		untruncatedPart(strings.Repeat("M", 100<<10), 0),
	}
	shaped, report := shapeBatch(parts, 1<<10, newShapeEnv(nil, shapeTestPrincipal))
	if report.degraded != 1 {
		t.Fatalf("degraded=%d, want 1", report.degraded)
	}
	if strings.Contains(shaped[1], "ref:output:") {
		t.Fatal("a ref was invented without a spool")
	}
	if !strings.Contains(shaped[1], fmt.Sprintf("of %d bytes", 100<<10)) {
		t.Fatalf("notice lost the true total: %q", tailOf(shaped[1]))
	}
}

func TestShapeBatchZeroBudgetIsAPassthrough(t *testing.T) {
	parts := []resultParts{untruncatedPart(body(1<<20), 0)}
	shaped, report := shapeBatch(parts, 0, shapeEnv{})
	if shaped[0] != parts[0].cappedBody {
		t.Fatal("zero budget altered a result")
	}
	if report.degraded != 0 {
		t.Fatalf("degraded=%d, want 0", report.degraded)
	}
}

// The load path refuses to touch the store without both a spool and a
// principal: a load attempted under an empty principal is a cross-principal
// read waiting to happen, and there is nothing to gain by trying.
func TestShapeEnvLoadRefusesWithoutSpoolOrPrincipal(t *testing.T) {
	spool, _ := testSpool(t)
	ref := spool.Spool(t.Context(), shapeTestPrincipal, []byte("body"))
	if ref == "" {
		t.Fatal("precondition: nothing spooled")
	}
	cases := map[string]shapeEnv{
		"no store":     {principal: shapeTestPrincipal},
		"no principal": {store: spool},
	}
	for name, env := range cases {
		if got, ok := env.load(ref); ok || got != "" {
			t.Fatalf("%s: load returned (%q, %v), want (\"\", false)", name, got, ok)
		}
	}
	if got, ok := newShapeEnv(spool, shapeTestPrincipal).load(""); ok || got != "" {
		t.Fatalf("empty ref loaded (%q, %v)", got, ok)
	}
}

// The batch accounting event is silent when the budget changed nothing: a row
// on every step would bury the tool rows it sits among.
func TestEmitBatchShapingIsSilentWhenNothingDegraded(t *testing.T) {
	var events []Event
	opts := Options{OnEvent: func(e Event) { events = append(events, e) }}

	emitBatchShaping(opts, shapeReport{budget: 1024, results: 3, charged: 90})
	if len(events) != 0 {
		t.Fatalf("emitted %d events for an untouched batch: %+v", len(events), events)
	}

	emitBatchShaping(opts, shapeReport{budget: 1024, results: 3, charged: 900, degraded: 2})
	if len(events) != 1 {
		t.Fatalf("emitted %d events for a degraded batch, want 1", len(events))
	}
	if !strings.Contains(events[0].Detail, "2 of 3 results degraded") {
		t.Fatalf("event detail %q does not report the counts", events[0].Detail)
	}
	if events[0].Content != "" || events[0].Output != "" {
		t.Fatal("batch accounting event carries result content; it must be content-free")
	}
}

func TestEffectiveBatchBudget(t *testing.T) {
	cases := []struct {
		name   string
		opts   Options
		expect int
	}{
		{"unset is unlimited", Options{}, 0},
		{"positive is literal", Options{BatchResultBudgetBytes: 4096}, 4096},
		{"derived without a prompt budget is inert",
			Options{BatchResultBudgetBytes: batchBudgetDerived}, 0},
		{"derived below the floor takes the floor",
			Options{BatchResultBudgetBytes: batchBudgetDerived, MaxContextTokens: 1000},
			derivedBatchBudgetFloorBytes},
		{"derived is a quarter of the prompt bytes",
			Options{BatchResultBudgetBytes: batchBudgetDerived, MaxContextTokens: 1 << 20},
			(1 << 20) * bytesPerToken / derivedBudgetShare},
		{"derivation cannot overflow",
			Options{BatchResultBudgetBytes: batchBudgetDerived, MaxContextTokens: math.MaxInt},
			maxDerivableTokens * bytesPerToken / derivedBudgetShare},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveBatchBudget(tc.opts); got != tc.expect {
				t.Fatalf("effectiveBatchBudget=%d, want %d", got, tc.expect)
			}
		})
	}
}

// BenchmarkShapeBatchResultsUnbudgeted pins the default path's cost: with no
// budget configured, appending a batch must not allocate per result beyond the
// one slice of bodies. Run with -benchmem; a regression here means shaping
// leaked onto the path every session takes.
func BenchmarkShapeBatchResultsUnbudgeted(b *testing.B) {
	results := make([]toolExecResult, 8)
	for i := range results {
		text := strings.Repeat("x", 64<<10)
		results[i] = toolExecResult{index: i, result: text,
			parts: resultParts{cappedBody: text, totalN: len(text)}}
	}
	opts := Options{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := shapeBatchResults(results, opts); len(got) != len(results) {
			b.Fatal("wrong body count")
		}
	}
}

// partialRefPattern matches a ref token that is not a complete reference:
// "ref:output:" followed by fewer than the full digest characters, at end of
// string or before a non-hex character.
var partialRefPattern = regexp.MustCompile(`ref:output:[0-9a-f]{0,63}(?:[^0-9a-f]|$)`)

func assertNoPartialRef(t *testing.T, s string) {
	t.Helper()
	if m := partialRefPattern.FindString(s); m != "" {
		t.Fatalf("shaped body contains a partial content reference %q in tail=%q", m, tailOf(s))
	}
}

// keptContent strips the framing a shaped body carries - the status line and
// the truncation notice - leaving only the tool bytes.
func keptContent(s string) string {
	for _, marker := range []string{"\n[batch result budget:", "\n... truncated: kept "} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i]
		}
	}
	return s
}

func tailOf(s string) string {
	if len(s) <= 220 {
		return s
	}
	return "…" + s[len(s)-220:]
}

func joinAll(parts []string) string { return strings.Join(parts, "\x00") }
