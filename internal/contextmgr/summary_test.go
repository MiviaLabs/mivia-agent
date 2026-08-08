package contextmgr

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestValidateSummaryRejectsSensitiveAndOversizedData(t *testing.T) {
	request := summaryTestRequest(t)
	request.RedactionPolicy = contextstate.RedactionPolicy{
		Configured: true,
		Classifier: func(data []byte) error {
			if strings.Contains(string(data), "sensitive-sentinel") {
				return errors.New("classifier rejection")
			}
			return nil
		},
	}
	base := Summary{Version: 1, Objective: "objective", State: "state", SourceRange: request.SourceRange}
	if _, err := ValidateSummary(base, request); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}

	sensitive := base
	sensitive.Evidence = []string{"sensitive-sentinel"}
	if _, err := ValidateSummary(sensitive, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("sensitive summary error = %v, want ErrInvalidDTO", err)
	}

	duplicate := base
	duplicate.Decisions = []string{"same", "same"}
	if _, err := ValidateSummary(duplicate, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("duplicate summary error = %v, want ErrInvalidDTO", err)
	}

	oversized := base
	oversized.Objective = strings.Repeat("x", maxSummaryFieldBytes+1)
	if _, err := ValidateSummary(oversized, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("oversized summary error = %v, want ErrInvalidDTO", err)
	}

	metadata, err := mustValidatedSummary(t, base).Metadata(false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "objective") || !strings.Contains(string(metadata), "content_sha256") {
		t.Fatalf("hash-only metadata leaked summary content: %s", metadata)
	}
	full, err := mustValidatedSummary(t, base).Metadata(true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(full), "objective") {
		t.Fatalf("configured metadata omitted validated summary content: %s", full)
	}
}

func TestValidateSummaryRejectsMismatchedProvenance(t *testing.T) {
	request := summaryTestRequest(t)
	summary := Summary{Version: 2, Objective: "objective", State: "state", SourceRange: request.SourceRange}
	if _, err := ValidateSummary(summary, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("version mismatch error = %v", err)
	}
	request.Input.SourceRange.End.Sequence++
	if _, err := ValidateSummary(Summary{Version: 1, Objective: "objective", State: "state", SourceRange: request.SourceRange}, request); err == nil {
		t.Fatal("request with mismatched sealed/input range was accepted")
	}
}

func summaryTestRequest(t *testing.T) SummaryRequest {
	t.Helper()
	source := contextstate.SourceID{SessionID: "summary-session", Sequence: 1}
	rangeValue, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	envelope, err := NewSummaryEnvelope(1, "objective", "state", nil, nil, nil, nil, nil, rangeValue, digest)
	if err != nil {
		t.Fatal(err)
	}
	return SummaryRequest{
		Input: envelope, Budget: 100, OutputLimit: 128, SourceRange: rangeValue,
		Provider: "provider", Model: "model", EndpointAllowlist: []string{"https://summary.invalid"},
	}
}

func mustValidatedSummary(t *testing.T, summary Summary) UntrustedSummary {
	t.Helper()
	request := summaryTestRequest(t)
	validated, err := ValidateSummary(summary, request)
	if err != nil {
		t.Fatal(err)
	}
	return validated
}

// TestValidateSummaryRejectsC1ControlCharacters confirms that C1 control
// characters (U+0080–U+009F) are rejected by ValidateSummary after the
// control-character check uses unicode.IsControl instead of a C0-only range.
func TestValidateSummaryRejectsC1ControlCharacters(t *testing.T) {
	request := summaryTestRequest(t)

	// C1 low boundary: U+0081 (PAD) in Objective
	c1Low := Summary{Version: 1, Objective: "obj\u0081ective", State: "state", SourceRange: request.SourceRange}
	if _, err := ValidateSummary(c1Low, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("summary with U+0081 in Objective: error = %v, want ErrInvalidDTO", err)
	}

	// C1 high boundary: U+009F (APC) in a Decision item
	c1High := Summary{Version: 1, Objective: "objective", State: "state", SourceRange: request.SourceRange, Decisions: []string{"dec\u009Fision"}}
	if _, err := ValidateSummary(c1High, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("summary with U+009F in Decisions: error = %v, want ErrInvalidDTO", err)
	}

	// Positive control: non-control Unicode U+00E9 (é) passes
	validUnicode := Summary{Version: 1, Objective: "café", State: "state", SourceRange: request.SourceRange}
	if _, err := ValidateSummary(validUnicode, request); err != nil {
		t.Fatalf("summary with U+00E9 é: unexpected error = %v", err)
	}

	// C0 regression: U+0001 (SOH) still rejected
	c0 := Summary{Version: 1, Objective: "obj\x01ective", State: "state", SourceRange: request.SourceRange}
	if _, err := ValidateSummary(c0, request); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("summary with U+0001 SOH: error = %v, want ErrInvalidDTO", err)
	}
}

// TestSummaryMetadataEnvelopeRejection drives the summary envelope's metadata
// bound through Validate: an envelope whose canonical size exceeds the default
// 12 KiB ceiling is refused, and the same envelope is accepted once the
// operator raises the ceiling.
func TestSummaryMetadataEnvelopeRejection(t *testing.T) {
	restore := contextstate.CurrentLimits()
	t.Cleanup(func() { contextstate.SetLimits(restore) })
	contextstate.SetLimits(contextstate.Limits{})

	source := contextstate.SourceID{SessionID: "summary-session", Sequence: 1}
	sourceRange, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	base, err := NewSummaryEnvelope(1, "objective", "state", nil, nil, nil, nil, nil, sourceRange, digest)
	if err != nil {
		t.Fatal(err)
	}

	// A full list of large (but per-field valid and unique) items pushes the
	// canonical envelope past the default summary metadata bound while staying
	// comfortably under the raised operator ceiling below.
	items := make([]string, maxSummaryItems)
	for i := range items {
		items[i] = fmt.Sprintf("%d:%s", i, strings.Repeat("x", 1024))
	}
	oversized := base
	oversized.Decisions = items

	if err := oversized.Validate(); err == nil {
		t.Fatal("summary envelope exceeding the default metadata limit validated")
	} else if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("oversized envelope error = %v, want ErrInvalidDTO", err)
	}

	contextstate.SetLimits(contextstate.Limits{SummaryMetadataBytes: 64 * 1024})
	if err := oversized.Validate(); err != nil {
		t.Fatalf("envelope rejected under raised operator ceiling: %v", err)
	}
}
