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
