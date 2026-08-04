package ledger

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Compile-time contract: StorageRepository must satisfy the Repository
// boundary declared in repository.go. If storage.go drifts from the
// interface, this package fails to compile.
var _ Repository = (*StorageRepository)(nil)

// TestSentinels pins the sentinel error contract from repository.go:
// all eight errors are non-nil, each matches itself via errors.Is, no two
// distinct sentinels match via errors.Is, and each carries an informative
// message containing the expected substring.
func TestSentinels(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
		want string
	}{
		{"ErrDuplicate", ErrDuplicate, "duplicate"},
		{"ErrNotFound", ErrNotFound, "not found"},
		{"ErrConflict", ErrConflict, "conflict"},
		{"ErrInvalidTransition", ErrInvalidTransition, "invalid state transition"},
		{"ErrClaimHeld", ErrClaimHeld, "claim held"},
		{"ErrClaimNotHeld", ErrClaimNotHeld, "claim not held"},
		{"ErrClosed", ErrClosed, "closed"},
		{"ErrContentNotFound", ErrContentNotFound, "content not found"},
	}

	// Non-nil, self-matching, informative message.
	for _, s := range sentinels {
		if s.err == nil {
			t.Errorf("%s: sentinel must be non-nil", s.name)
			continue
		}
		if !errors.Is(s.err, s.err) {
			t.Errorf("%s: sentinel must match itself via errors.Is", s.name)
		}
		if !strings.Contains(s.err.Error(), s.want) {
			t.Errorf("%s: Error() = %q, want substring %q", s.name, s.err.Error(), s.want)
		}
	}

	// Pairwise distinct: errors.Is(e1, e2) must be false in both directions
	// for every distinct pair.
	for i := 0; i < len(sentinels); i++ {
		for j := i + 1; j < len(sentinels); j++ {
			a, b := sentinels[i], sentinels[j]
			if errors.Is(a.err, b.err) {
				t.Errorf("%s and %s must be distinct: errors.Is(%s, %s) = true", a.name, b.name, a.name, b.name)
			}
			if errors.Is(b.err, a.err) {
				t.Errorf("%s and %s must be distinct: errors.Is(%s, %s) = true", a.name, b.name, b.name, a.name)
			}
		}
	}
}

// TestRepositoryContract pins the RecoveredRun storage-boundary shape: the
// zero value is usable, and the exported fields marshal to JSON under their
// default (untagged) field names.
func TestRepositoryContract(t *testing.T) {
	t.Run("RecoveredRun zero value is usable", func(t *testing.T) {
		b, err := json.Marshal(RecoveredRun{})
		if err != nil {
			t.Fatalf("zero RecoveredRun must marshal without error: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("zero RecoveredRun JSON must unmarshal: %v", err)
		}
		for _, key := range []string{"RunID", "WorkflowName", "Status", "WasInterrupted", "CreatedAt"} {
			if _, ok := m[key]; !ok {
				t.Errorf("RecoveredRun JSON missing default field %q: %v", key, m)
			}
		}
		if len(m) != 5 {
			t.Errorf("RecoveredRun must marshal exactly the 5 default fields, got %d: %v", len(m), m)
		}
	})

	t.Run("RecoveredRun round-trips populated values", func(t *testing.T) {
		now := time.Date(2025, 6, 1, 12, 30, 0, 0, time.UTC)
		in := RecoveredRun{
			RunID:          "wfr-abc",
			WorkflowName:   "demo-workflow",
			Status:         RunStatusFailed,
			WasInterrupted: true,
			CreatedAt:      now,
		}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("RecoveredRun must marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("RecoveredRun JSON must unmarshal: %v", err)
		}
		want := map[string]any{
			"RunID":          "wfr-abc",
			"WorkflowName":   "demo-workflow",
			"Status":         string(RunStatusFailed),
			"WasInterrupted": true,
			"CreatedAt":      now.Format(time.RFC3339Nano),
		}
		for key, wantVal := range want {
			if gotVal, ok := m[key]; !ok || gotVal != wantVal {
				t.Errorf("RecoveredRun JSON field %q = %v, want %v", key, gotVal, wantVal)
			}
		}
		var got RecoveredRun
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("RecoveredRun JSON must unmarshal into struct: %v", err)
		}
		if got != in {
			t.Errorf("RecoveredRun round-trip mismatch: got %+v, want %+v", got, in)
		}
	})
}
