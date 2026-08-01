package contextstate

import (
	"errors"
	"testing"
)

func TestSourceEventValidation(t *testing.T) {
	id, err := NewSourceID("session", 1)
	if err != nil {
		t.Fatal(err)
	}
	event := SourceEvent{
		ID: id, Kind: "message", Role: "tool", ToolCallID: "call-1",
		Provenance: "host", RedactionStatus: "sanitized", Size: 4,
	}
	if err := ValidateSourceEvent(event); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	event.ToolCallID = "bad\ncall"
	if err := ValidateSourceEvent(event); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("control character error = %v, want ErrInvalidDTO", err)
	}
	event.ToolCallID = "call-1"
	// The payload bound is operator configuration and ships uncapped, so an
	// oversized event is only invalid once a ceiling is actually installed.
	event.Size = 1 << 20
	if err := ValidateSourceEvent(event); err != nil {
		t.Fatalf("uncapped default rejected a large event: %v", err)
	}
	restore := CurrentLimits()
	t.Cleanup(func() { SetLimits(restore) })
	SetLimits(Limits{SourceEventBytes: 64 * 1024})
	if err := ValidateSourceEvent(event); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("size error = %v, want ErrInvalidDTO", err)
	}
	event.Size = 4
	if err := ValidateSourceEvent(event); err != nil {
		t.Fatalf("in-bound event rejected under a configured ceiling: %v", err)
	}
}
