package chat

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// nameCap is the enforced total-name ceiling on the admitted set: the
// well-behaved path admits at most MaxAdmissionPublications batches of up to
// MaxAdmissionNamesPerCall names, and the deferred path must not accumulate
// more into one publication than that.
func nameCap() int {
	return tools.MaxAdmissionPublications * tools.MaxAdmissionNamesPerCall
}

// distinctNames returns n distinct names under one prefix, so several batches
// can never collide with each other.
func distinctNames(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%s-%d", prefix, i))
	}
	return out
}

// TestStageAdmissionNameCapRefusesOverLimit: one call staging 513 distinct
// names in a single widening must be refused outright, leaving no trace of an
// admission behind.
func TestStageAdmissionNameCapRefusesOverLimit(t *testing.T) {
	sess := newAdmissionSession(t)
	names := distinctNames("tool", nameCap()+1)
	_, err := sess.StageToolAdmission(names, 0)
	if err == nil {
		t.Fatalf("one call staging %d names was allowed past the %d-name cap", len(names), nameCap())
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want it to mention the limit", err)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want nothing admitted after a refused call", got)
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a refused call must not leave a pending stage")
	}
}

// TestPerpetualDeferralAccumulationCapped is the headline hazard: a sibling
// turn keeps activeTurns != 1, so every boundary defers and the pending stage
// folds in names from later turns. Without the total-name cap that grows one
// publication past 512; with it, the fold stops exactly at the ceiling.
func TestPerpetualDeferralAccumulationCapped(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	sess.mu.Lock()
	sess.activeTurns = 2 // a sibling turn keeps every publication deferred
	sess.mu.Unlock()

	for k := 0; k < tools.MaxAdmissionPublications; k++ {
		if _, err := sess.StageToolAdmission(distinctNames(fmt.Sprintf("b%d", k), tools.MaxAdmissionNamesPerCall), 0); err != nil {
			t.Fatalf("deferred stage %d: %v", k, err)
		}
		sess.PublishPendingAdmission() // sibling turn active -> deferred, never publishes
	}
	if widener.count() != 0 {
		t.Fatal("a publication happened while a sibling turn was active")
	}
	stage, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("no pending stage after the deferrals")
	}
	if len(stage.Names) != nameCap() {
		t.Fatalf("pending names = %d, want the %d-name ceiling accumulated across deferred batches", len(stage.Names), nameCap())
	}

	// The 9th batch would be the 576th name: refused, and the pending stage
	// stays exactly at the ceiling.
	_, err := sess.StageToolAdmission(distinctNames("b9", tools.MaxAdmissionNamesPerCall), 0)
	if err == nil {
		t.Fatal("a batch past the accumulated ceiling was admitted")
	}
	stage, _ = sess.PendingAdmission()
	if len(stage.Names) != nameCap() {
		t.Fatalf("pending names after refusal = %d, want the ceiling of %d", len(stage.Names), nameCap())
	}
}

// TestStageAdmissionExactlyAtCapAllowed: the ceiling is strict - exactly
// MaxAdmissionPublications x MaxAdmissionNamesPerCall (512) names in one call
// is the maximum and must stay allowed (admission_note_test.go's worst-case
// fixture stages exactly this).
func TestStageAdmissionExactlyAtCapAllowed(t *testing.T) {
	sess := newAdmissionSession(t)
	names := distinctNames("tool", nameCap())
	if _, err := sess.StageToolAdmission(names, 0); err != nil {
		t.Fatalf("staging exactly %d names must be allowed: %v", len(names), err)
	}
	stage, ok := sess.PendingAdmission()
	if !ok || len(stage.Names) != len(names) {
		t.Fatalf("pending = %+v, want all %d names staged", stage, len(names))
	}
}

// TestNameCapExcludesAlreadyStagedReRequests: a re-request of an already
// staged name stages nothing new, so it consumes no cap budget and is free
// even when the pending stage already sits at the ceiling.
func TestNameCapExcludesAlreadyStagedReRequests(t *testing.T) {
	sess := newAdmissionSession(t)
	names := distinctNames("tool", nameCap())
	if _, err := sess.StageToolAdmission(names, 1); err != nil {
		t.Fatalf("stage to the cap: %v", err)
	}
	result, err := sess.StageToolAdmission([]string{"tool-0"}, 2)
	if err != nil {
		t.Fatalf("re-request at the cap must be free: %v", err)
	}
	if !slices.Equal(result.AlreadyStaged, []string{"tool-0"}) {
		t.Fatalf("already staged = %v, want the re-requested name", result.AlreadyStaged)
	}
	if len(result.Staged) != 0 {
		t.Fatalf("staged = %v, want nothing new", result.Staged)
	}
}

// TestNameCapCountsTotalAcrossPublishedAndPending: the cap counts ADMITTED
// plus PENDING names together, not just the size of one stage. One published
// batch of 64 plus one deferred batch of 448 fills the 512 ceiling, so a
// single further name is refused.
func TestNameCapCountsTotalAcrossPublishedAndPending(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)

	// One batch that publishes at its turn boundary: 64 names admitted.
	if _, err := sess.StageToolAdmission(distinctNames("pub", tools.MaxAdmissionNamesPerCall), 0); err != nil {
		t.Fatalf("stage published batch: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if got := sess.AdmittedTools(); len(got) != tools.MaxAdmissionNamesPerCall {
		t.Fatalf("admitted = %d names, want %d published", len(got), tools.MaxAdmissionNamesPerCall)
	}

	// One deferred batch that brings admitted+pending to exactly 512.
	deferred := nameCap() - tools.MaxAdmissionNamesPerCall
	if _, err := sess.StageToolAdmission(distinctNames("def", deferred), 0); err != nil {
		t.Fatalf("stage deferred batch: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 2
	sess.mu.Unlock()
	sess.PublishPendingAdmission() // sibling turn active -> stays pending
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the second batch must stay pending, not publish")
	}

	// 64 admitted + 448 pending + 1 more = 513 > 512: refused.
	_, err := sess.StageToolAdmission([]string{"one-too-many"}, 0)
	if err == nil {
		t.Fatal("a name past the admitted+pending total was accepted")
	}
	if !strings.Contains(err.Error(), "names already admitted or staged") {
		t.Fatalf("error = %v, want the total-name cap", err)
	}
}

// TestNameCapAllowsFreshStageAfterReset: an /agent switch (ResetAdmissions)
// clears the counters, so a fresh binding can stage again after hitting the
// ceiling on the old one.
func TestNameCapAllowsFreshStageAfterReset(t *testing.T) {
	sess := newAdmissionSession(t)
	names := distinctNames("tool", nameCap())
	if _, err := sess.StageToolAdmission(names, 0); err != nil {
		t.Fatalf("stage to the cap: %v", err)
	}
	if _, err := sess.StageToolAdmission([]string{"one-too-many"}, 0); err == nil {
		t.Fatal("a name past the ceiling was accepted")
	}
	sess.ResetAdmissions()
	if _, err := sess.StageToolAdmission(distinctNames("fresh", tools.MaxAdmissionNamesPerCall), 0); err != nil {
		t.Fatalf("a fresh binding could not stage after reset: %v", err)
	}
}
