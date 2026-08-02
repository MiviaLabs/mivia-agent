package contextstate

import (
	"bytes"
	"strings"
	"testing"
)

// TestEffectiveMetadataLimitsDefaultToCompiledFallback pins the no-behavior-
// change-by-default constraint: with no operator ceilings installed, the
// effective summary and checkpoint metadata bounds stay at the compiled-in
// defaults (12 KiB / 16 KiB), not zero.
func TestEffectiveMetadataLimitsDefaultToCompiledFallback(t *testing.T) {
	restore := CurrentLimits()
	t.Cleanup(func() { SetLimits(restore) })
	SetLimits(Limits{})

	if got := EffectiveSummaryMetadataLimit(); got != DefaultMaxSummaryMetadata {
		t.Fatalf("EffectiveSummaryMetadataLimit() = %d, want compiled default %d", got, DefaultMaxSummaryMetadata)
	}
	if got := EffectiveCheckpointMetadataLimit(); got != DefaultMaxCheckpointMetadata {
		t.Fatalf("EffectiveCheckpointMetadataLimit() = %d, want compiled default %d", got, DefaultMaxCheckpointMetadata)
	}
}

// TestEffectiveMetadataLimitsHonorOperatorCeiling verifies an installed
// operator bound wins over the compiled-in default.
func TestEffectiveMetadataLimitsHonorOperatorCeiling(t *testing.T) {
	restore := CurrentLimits()
	t.Cleanup(func() { SetLimits(restore) })
	SetLimits(Limits{SummaryMetadataBytes: 64 * 1024, CheckpointMetadataBytes: 96 * 1024})

	if got := EffectiveSummaryMetadataLimit(); got != 64*1024 {
		t.Fatalf("EffectiveSummaryMetadataLimit() = %d, want 64 KiB operator ceiling", got)
	}
	if got := EffectiveCheckpointMetadataLimit(); got != 96*1024 {
		t.Fatalf("EffectiveCheckpointMetadataLimit() = %d, want 96 KiB operator ceiling", got)
	}
}

// TestCheckpointMetadataRejectionThroughValidate drives the summary_metadata
// bound through CheckpointRecord.Validate: an over-default record is refused,
// and the same record is accepted once the operator raises the ceiling.
func TestCheckpointMetadataRejectionThroughValidate(t *testing.T) {
	restore := CurrentLimits()
	t.Cleanup(func() { SetLimits(restore) })
	SetLimits(Limits{})

	_, _, _, checkpoint := validContractFixture(t)
	checkpoint.SummaryMetadata = bytes.Repeat([]byte("m"), DefaultMaxCheckpointMetadata+1)

	err := checkpoint.Validate()
	if err == nil {
		t.Fatal("checkpoint with oversized summary_metadata validated")
	}
	if !strings.Contains(err.Error(), "summary_metadata") {
		t.Fatalf("metadata rejection error = %v, want it to mention summary_metadata", err)
	}

	SetLimits(Limits{CheckpointMetadataBytes: 32 * 1024})
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("checkpoint rejected after operator ceiling raised: %v", err)
	}
}

// TestCheckpointMetadataUncappedAllowsAnySize pins the compiled-in fallback
// boundary: with no operator ceiling installed the summary_metadata bound is
// DefaultMaxCheckpointMetadata (exactly at it validates, one byte over does
// not), while the rest of the record stays uncapped.
func TestCheckpointMetadataUncappedAllowsAnySize(t *testing.T) {
	restore := CurrentLimits()
	t.Cleanup(func() { SetLimits(restore) })
	SetLimits(Limits{})

	_, _, _, atLimit := validContractFixture(t)
	atLimit.SummaryMetadata = bytes.Repeat([]byte("m"), DefaultMaxCheckpointMetadata)
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("checkpoint at exactly the default metadata ceiling rejected: %v", err)
	}

	_, _, _, overLimit := validContractFixture(t)
	overLimit.SummaryMetadata = bytes.Repeat([]byte("m"), DefaultMaxCheckpointMetadata+1)
	if err := overLimit.Validate(); err == nil {
		t.Fatal("checkpoint one byte over the default metadata ceiling validated")
	}
}
