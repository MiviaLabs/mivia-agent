package chat

import (
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// prefixSession builds a session whose model offers reasoning efforts, so the
// W1 tests can drive the /effort dial and prove the identity flips with it.
func prefixSession(t *testing.T) *Session {
	t.Helper()
	return NewSession(&config.Resolved{ProviderName: "zai", Model: reasoningModel, Models: []string{reasoningModel}, ModelProfiles: []config.ModelSpec{{Name: reasoningModel, ContextWindowTokens: 100000, ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High}, Reasoning: reasoning.High, ReasoningDialect: reasoning.DialectThinkingEffort}}}, &requestCaptureCompleter{})
}

// TestPrefixIdentityChangesWithReasoningEffort pins gap B13: an accepted
// /effort set AND an accepted clear both make the cached identity unequal to
// the pre-change identity, because /effort changes the request body via
// reasoningFields in a way BindingFence cannot detect.
func TestPrefixIdentityChangesWithReasoningEffort(t *testing.T) {
	s := prefixSession(t)
	before := s.PrefixIdentity()

	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	afterSet := s.PrefixIdentity()
	if afterSet == before {
		t.Fatalf("accepted /effort set left the identity unchanged: %+v", afterSet)
	}
	if afterSet.ReasoningLevel != string(reasoning.Low) {
		t.Fatalf("identity reasoning level = %q, want %q", afterSet.ReasoningLevel, reasoning.Low)
	}

	if err := s.SetReasoningEffort(""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	afterClear := s.PrefixIdentity()
	if afterClear == afterSet {
		t.Fatalf("accepted /effort clear left the identity unchanged: %+v", afterClear)
	}
	if afterClear.ReasoningLevel != string(reasoning.High) {
		t.Fatalf("identity reasoning level after clear = %q, want the model default %q", afterClear.ReasoningLevel, reasoning.High)
	}
}

// TestPrefixIdentityStableAcrossEqualBindingCaptures pins INV-68-1: two
// sessions built from identical config produce identical identities, and
// repeated accessor reads within one session return the cached value
// unchanged (the cache is read, not recomputed, between trigger events).
func TestPrefixIdentityStableAcrossEqualBindingCaptures(t *testing.T) {
	first := prefixSession(t)
	second := prefixSession(t)

	firstID := first.PrefixIdentity()
	secondID := second.PrefixIdentity()
	if firstID != secondID {
		t.Fatalf("equal configs produced unequal identities:\n%+v\n---\n%+v", firstID, secondID)
	}

	for i := 0; i < 3; i++ {
		if again := first.PrefixIdentity(); again != firstID {
			t.Fatalf("repeated read %d changed the cached identity: %+v", i, again)
		}
	}
}

// TestPrefixIdentityDistinctFromBindingFencePurpose pins AR-1: BindingFence
// and captureBindingFence stay exactly as they were (a four-field async-fence
// type), OperationToken.Binding stays a BindingFence, and PrefixIdentity is a
// distinct ten-field value type with its own observability purpose (field 10,
// ReasoningDialect, was added by audit RC-2: the provider-resolved dialect is
// wire-affecting and invisible to BindingFence, exactly like ReasoningLevel).
func TestPrefixIdentityDistinctFromBindingFencePurpose(t *testing.T) {
	s := prefixSession(t)
	binding := s.CurrentBinding()

	fence := captureBindingFence(binding)
	if fence.ProviderName == "" || fence.Model == "" || fence.ModelGeneration == 0 {
		t.Fatalf("captureBindingFence still returns a populated BindingFence: %+v", fence)
	}

	// OperationToken.Binding stays a BindingFence (compile-time pin).
	token := s.currentSaveToken()
	var _ BindingFence = token.Binding

	// PrefixIdentity is a distinct ten-field value type; BindingFence stays
	// four fields. Distinctness is structural: the two are not assignable.
	if n := reflect.TypeOf(PrefixIdentity{}).NumField(); n != 10 {
		t.Fatalf("PrefixIdentity has %d fields, want 10 (RC-2 added ReasoningDialect)", n)
	}
	if n := reflect.TypeOf(BindingFence{}).NumField(); n != 4 {
		t.Fatalf("BindingFence has %d fields, want 4 (AR-1: unchanged)", n)
	}

	id := s.PrefixIdentity()
	if id.ProviderName != fence.ProviderName || id.Model != fence.Model {
		t.Fatalf("identity provider/model must match the fence's: %+v vs %+v", id, fence)
	}
}

// TestPrefixIdentityNotCapturedOnPerTurnSavePath pins INV-68-8: the capture
// counter proves that neither SaveAfterTurn nor the internal saveAfterTurn
// recaptures the prefix identity; capture is reachable only from the four
// trigger events.
func TestPrefixIdentityNotCapturedOnPerTurnSavePath(t *testing.T) {
	s := prefixSession(t)
	s.SessionDir = t.TempDir()
	before := s.prefixIdentityCaptureCountForTest()
	if before == 0 {
		t.Fatal("NewSession must have captured once")
	}

	s.SaveAfterTurn()
	if got := s.prefixIdentityCaptureCountForTest(); got != before {
		t.Fatalf("SaveAfterTurn recaptured the prefix identity: count %d -> %d (INV-68-8)", before, got)
	}

	if err := s.saveAfterTurn(s.currentSaveToken()); err != nil {
		t.Fatalf("saveAfterTurn: %v", err)
	}
	if got := s.prefixIdentityCaptureCountForTest(); got != before {
		t.Fatalf("saveAfterTurn recaptured the prefix identity: count %d -> %d (INV-68-8)", before, got)
	}
}
