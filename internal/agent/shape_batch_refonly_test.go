package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// --- Wave 3 ref-only tier: never-inline tools (plan tools/06) ----------------
//
// RefOnlyTools opts a tool out of inlining entirely: when its raw body is at
// least BatchDegradeFloorBytes and the result is not ephemeral, the WHOLE body
// is spooled and replaced by a notice naming the remainder, and only the
// notice's bytes are charged. All other tiers stay byte-identical.

// refOnlyResult is a pass-1 result for a named tool: an untruncated body plus
// the structured parts buildExecResult would produce for it. The tool name is
// the only extra thing the ref-only tier needs, and it lives on the call.
func refOnlyResult(name, text string) toolExecResult {
	return toolExecResult{
		toolCall: toolCall("call_"+name, name, "{}"),
		result:   text,
		parts:    untruncatedPart(text, 0),
	}
}

// refOnlyNotice is the body the ref-only tier must emit when the spool minted
// a ref: the whole result is replaced by this notice, and its bytes are the
// ones charged.
func refOnlyNotice(name string, total int, ref string) string {
	return fmt.Sprintf("[tool result for %s elided to a remainder ref (original ~%s): %s — use read_output to fetch the full body]",
		name, bucketLabel(total), ref)
}

// refOnlyPlainNotice is the INV-AG-10 fallback when no ref could be minted
// (nil spool, empty principal, or store failure): the body is still elided,
// but no remainder is named and read_output is never directed.
func refOnlyPlainNotice(name string, total int) string {
	return fmt.Sprintf("[tool result for %s elided; original ~%s]", name, bucketLabel(total))
}

// bucketLabel re-derives the size label the notice must carry: the raw body
// size rounded UP to the next power of two, rendered as KiB or MiB. The
// production label lives in contextmgr (unexported); the notice has to agree
// with it, so the test applies the same rounding rules locally.
func bucketLabel(n int) string {
	if n <= 0 {
		return "0 KiB"
	}
	bucket := 1
	for bucket < n {
		bucket <<= 1
	}
	const kib = 1024
	if bucket >= kib*kib {
		return fmt.Sprintf("%d MiB", bucket/(kib*kib))
	}
	return fmt.Sprintf("%d KiB", bucket/kib)
}

// The golden ref-only case: the tool is named in RefOnlyTools, the body is
// well above the floor, and the spool works. The whole body - never a
// truncation artifact - is spooled, exactly one remainder exists, and the
// result content is the notice, not the body, so the bytes charged are the
// notice's alone.
func TestRefOnlySpoolsWholeBody(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("A", big)
	opts := Options{
		BatchResultBudgetBytes: 1 << 20, // generous: only the ref-only tier may shape
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file", text)}, opts)

	got := bodies[0]
	if !strings.Contains(got, "elided to a remainder ref (original ~512 KiB)") {
		t.Fatalf("notice lost the elision wording or the size label: %q", tailOf(got))
	}
	if strings.Contains(got, "elided; original") {
		t.Fatalf("ref-only notice fell back to the plain form despite a working spool: %q", tailOf(got))
	}
	if !strings.Contains(got, "use read_output to fetch the full body") {
		t.Fatalf("notice no longer directs the model to read_output: %q", tailOf(got))
	}
	ref := refIn(t, got)
	if !strings.HasPrefix(ref, "ref:output:") {
		t.Fatalf("notice names no remainder ref: %q", tailOf(got))
	}
	want := refOnlyNotice("read_file", big, ref)
	if !strings.HasPrefix(got, want) {
		t.Fatalf("result content is not exactly the ref-only notice:\n got      %q\nwant-pref %q", tailOf(got), want)
	}
	if len(got) > len(want)+statusLineMaxBytes {
		t.Fatalf("result carries %d bytes, want the notice alone (%d, plus status-line slack)", len(got), len(want))
	}
	if strings.Contains(got, strings.Repeat("A", 1<<10)) {
		t.Fatal("the raw body leaked into the shaped result")
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want exactly 1 (the ref-only body)", store.Len())
	}
	data, err := spool.Load(t.Context(), shapeTestPrincipal, ref)
	if err != nil {
		t.Fatalf("spooled ref %s does not resolve: %v", ref, err)
	}
	if string(data) != text {
		t.Fatalf("spool pages %d bytes, want the FULL %d-byte untruncated body", len(data), len(text))
	}
	assertNoPartialRef(t, got)
}

// Below the floor the ref-only tier is inert: the body is inlined whole, no
// notice is emitted, and nothing is spooled. The floor is the same
// BatchDegradeFloorBytes the budget tiers use.
func TestRefOnlyBelowFloorStaysInline(t *testing.T) {
	spool, store := testSpool(t)
	const small = 4 << 10 // < BatchDegradeFloorBytes
	text := strings.Repeat("B", small)
	opts := Options{
		BatchResultBudgetBytes: 1 << 20,
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file", text)}, opts)

	if bodies[0] != text {
		t.Fatalf("sub-floor body was not inlined whole: %q", trunc(bodies[0], 120))
	}
	if strings.Contains(bodies[0], "elided") {
		t.Fatalf("sub-floor body carries a ref-only notice: %q", trunc(bodies[0], 120))
	}
	if store.Len() != 0 {
		t.Fatalf("%d bodies spooled for a sub-floor result, want 0", store.Len())
	}
}

// Membership is STRICT: a name that merely shares a prefix with a ref-only
// tool keeps the existing budget tiers byte-for-byte and never sees a ref-only
// notice. Here read_file_x must not match RefOnlyTools["read_file"].
func TestRefOnlyNotInSetUnchanged(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("C", big)
	opts := Options{
		BatchResultBudgetBytes: 1 << 10, // tight: forces the existing budget tiers
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file_x", text)}, opts)

	got := bodies[0]
	if !strings.Contains(got, "... truncated: kept ") {
		t.Fatalf("non-ref-only result did not take the existing budget tier: %q", tailOf(got))
	}
	if !strings.Contains(got, "ref:output:") {
		t.Fatalf("existing tier lost its remainder ref: %q", tailOf(got))
	}
	if strings.Contains(got, "elided") {
		t.Fatalf("a tool outside RefOnlyTools got a ref-only notice: %q", tailOf(got))
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want 1 (the existing tier spooled the original)", store.Len())
	}
	assertNoPartialRef(t, got)
}

// D10 outranks ref-only: an ephemeral result is charged like any other body
// but is never spooled and never gets a ref-only notice - a ref would let the
// model page back, via read_output, exactly the bytes the scrub exists to
// remove. It degrades through the existing tiers with a plain, ref-free
// notice.
func TestRefOnlySkipsEphemeral(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("D", big)
	r := refOnlyResult("ephemeral_resource", text)
	r.parts.ephemeral = true
	opts := Options{
		BatchResultBudgetBytes: 1 << 10, // tight: forces a degrade
		RefOnlyTools:           []string{"ephemeral_resource"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{r}, opts)

	got := bodies[0]
	if strings.Contains(got, "elided") {
		t.Fatalf("ephemeral ref-only result got a ref-only notice: %q", tailOf(got))
	}
	if strings.Contains(got, "ref:output:") {
		t.Fatalf("ephemeral result was put behind a ref: %q", tailOf(got))
	}
	if !strings.Contains(got, "... truncated: kept ") {
		t.Fatalf("ephemeral result was not degraded honestly: %q", tailOf(got))
	}
	if store.Len() != 0 {
		t.Fatalf("%d bodies spooled for an ephemeral result, want 0", store.Len())
	}
}

// INV-AG-10 with no spool at all: the body is still elided (the operator opted
// the tool in), but the notice names no remainder and never directs
// read_output - a ref must never be invented where one cannot be minted.
func TestRefOnlyNilSpoolFallsBackToPlainNotice(t *testing.T) {
	const big = 300 << 10
	text := strings.Repeat("E", big)
	opts := Options{
		BatchResultBudgetBytes: 1 << 20,
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		// RemainderSpool left nil: no ref can be minted.
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file", text)}, opts)

	want := refOnlyPlainNotice("read_file", big)
	if bodies[0] != want {
		t.Fatalf("nil-spool fallback = %q, want %q", tailOf(bodies[0]), want)
	}
	if strings.Contains(bodies[0], "ref:output:") || strings.Contains(bodies[0], "read_output") {
		t.Fatal("a ref was invented without a spool")
	}
}

// INV-AG-10 with a failing store: the spool returns no ref, the fallback plain
// notice is emitted, and the tool call is never failed by the store.
func TestRefOnlyStoreFailureFallsBackToPlainNotice(t *testing.T) {
	spool := remainder.NewSpool(remainder.FailingStore{})
	const big = 300 << 10
	text := strings.Repeat("F", big)
	opts := Options{
		BatchResultBudgetBytes: 1 << 20,
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file", text)}, opts)

	want := refOnlyPlainNotice("read_file", big)
	if bodies[0] != want {
		t.Fatalf("store-failure fallback = %q, want %q", tailOf(bodies[0]), want)
	}
	if strings.Contains(bodies[0], "ref:output:") || strings.Contains(bodies[0], "read_output") {
		t.Fatal("a ref was invented despite a failed store")
	}
}

// INV-AG-10's other edge: a working spool with an EMPTY principal cannot mint
// a ref (visibility is principal-scoped), so the fallback plain notice is
// emitted and nothing is stored.
func TestRefOnlyEmptyPrincipalFallsBack(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("G", big)
	opts := Options{
		BatchResultBudgetBytes: 1 << 20,
		RefOnlyTools:           []string{"read_file"},
		SessionID:              "", // empty principal: no ref, no store
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file", text)}, opts)

	want := refOnlyPlainNotice("read_file", big)
	if bodies[0] != want {
		t.Fatalf("empty-principal fallback = %q, want %q", tailOf(bodies[0]), want)
	}
	if strings.Contains(bodies[0], "ref:output:") || strings.Contains(bodies[0], "read_output") {
		t.Fatal("a ref was invented for an empty principal")
	}
	if store.Len() != 0 {
		t.Fatalf("%d bodies stored under an empty principal, want 0", store.Len())
	}
}

// The ref-only tier outranks the budget tiers: even a budget so small the
// body would be re-cut to the floor cannot stop the ref-only notice, because
// the whole body was already spooled before the budget pass ever saw the
// result. What reaches the model is notice-sized, never the 16 KiB floor the
// budget tier would have cut to.
func TestRefOnlyBudgetExhaustedStillRefOnly(t *testing.T) {
	spool, store := testSpool(t)
	const big = 300 << 10
	text := strings.Repeat("H", big)
	opts := Options{
		BatchResultBudgetBytes: 64, // far below the floor: budget tiers would fire
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	bodies := shapeResultsForTest([]toolExecResult{refOnlyResult("read_file", text)}, opts)

	got := bodies[0]
	if !strings.Contains(got, "elided to a remainder ref (original ~512 KiB)") {
		t.Fatalf("ref-only notice lost under an exhausted budget: %q", tailOf(got))
	}
	if strings.Contains(got, "... truncated: kept ") {
		t.Fatalf("budget truncation leaked into a ref-only result: %q", tailOf(got))
	}
	ref := refIn(t, got)
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want 1", store.Len())
	}
	if data, err := spool.Load(t.Context(), shapeTestPrincipal, ref); err != nil || string(data) != text {
		t.Fatalf("ref-only ref does not page the full body (err=%v)", err)
	}
	if len(got) > 1<<10 {
		t.Fatalf("ref-only result kept %d bytes, want a notice (the budget tier would have cut to %d)",
			len(got), BatchDegradeFloorBytes)
	}
	assertNoPartialRef(t, got)
}

// A pass-1-TRUNCATED ref-only result must page the ORIGINAL, not the
// truncation artifact: buildExecResult already spooled the original under
// parts.refA via CapWithSpoolRef, so the notice names THAT ref and never
// spools again. Pre-fix, refOnlyTier spooled p.cappedBody (the artifact) and
// named a NEW ref - the model's read_output then paged the 4 KiB artifact
// while the notice called it 'original ~512 KiB', the real body needed an
// undisclosed second hop, and the store held a redundant second entry.
func TestRefOnlyNamesPassOneRefForTruncatedResult(t *testing.T) {
	spool, store := testSpool(t)
	const cap = 4 << 10
	original := strings.Repeat("A", 300<<10)
	capped := capPart(t, spool, original, cap)
	if !capped.truncated || capped.refA == "" {
		t.Fatalf("precondition: pass 1 did not truncate+spool (truncated=%v ref=%q)", capped.truncated, capped.refA)
	}
	opts := Options{
		BatchResultBudgetBytes: 1 << 20, // generous: only the ref-only tier may shape
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	r := toolExecResult{
		toolCall: toolCall("call_read_file", "read_file", "{}"),
		result:   capped.cappedBody,
		parts:    capped,
	}
	bodies := shapeResultsForTest([]toolExecResult{r}, opts)

	got := bodies[0]
	if !strings.Contains(got, "elided to a remainder ref (original ~512 KiB)") {
		t.Fatalf("notice lost the elision wording or the size label: %q", tailOf(got))
	}
	ref := refIn(t, got)
	if ref != capped.refA {
		t.Fatalf("notice names %q, want the pass-1 original ref %q (a NEW ref would page the artifact)", ref, capped.refA)
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want exactly 1 (the original; the artifact must not be re-spooled)", store.Len())
	}
	data, err := spool.Load(t.Context(), shapeTestPrincipal, ref)
	if err != nil {
		t.Fatalf("named ref %s does not resolve: %v", ref, err)
	}
	if string(data) != original {
		t.Fatalf("named ref pages %d bytes, want the FULL %d-byte original", len(data), len(original))
	}
	if strings.Contains(string(data), "... truncated: kept ") {
		t.Fatal("named ref pages a truncation artifact, not the original")
	}
	assertNoPartialRef(t, got)
}

// INV-AG-10 on the truncated branch: when pass 1 truncated the result but
// minted no ref (store failure), the ref-only tier must NOT spool the pass-1
// artifact - that would repeat the exact defect by naming the artifact as the
// 'original' - and must not invent a ref. It falls back to the existing plain
// notice, and the tool call is never failed by the store.
func TestRefOnlyTruncatedNoPassOneRefFallsBackToPlainNotice(t *testing.T) {
	spool := remainder.NewSpool(remainder.FailingStore{})
	const big = 300 << 10
	original := strings.Repeat("A", big)
	capped := capPart(t, spool, original, 4<<10)
	if !capped.truncated || capped.refA != "" {
		t.Fatalf("precondition: pass 1 did not truncate without a ref (truncated=%v ref=%q)", capped.truncated, capped.refA)
	}
	opts := Options{
		BatchResultBudgetBytes: 1 << 20,
		RefOnlyTools:           []string{"read_file"},
		SessionID:              shapeTestPrincipal,
		RemainderSpool:         spool,
	}
	r := toolExecResult{
		toolCall: toolCall("call_read_file", "read_file", "{}"),
		result:   capped.cappedBody,
		parts:    capped,
	}
	bodies := shapeResultsForTest([]toolExecResult{r}, opts)

	want := refOnlyPlainNotice("read_file", big)
	if bodies[0] != want {
		t.Fatalf("truncated-no-ref fallback = %q, want %q", tailOf(bodies[0]), want)
	}
	if strings.Contains(bodies[0], "ref:output:") || strings.Contains(bodies[0], "read_output") {
		t.Fatal("a ref was invented for a truncated result whose pass-1 spool failed")
	}
	if strings.HasPrefix(bodies[0], "error:") {
		t.Fatalf("truncated-no-ref result was failed by the store: %q", tailOf(bodies[0]))
	}
}
