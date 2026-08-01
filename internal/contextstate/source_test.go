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
	event.Size = MaxSourceEventBytes + 1
	if err := ValidateSourceEvent(event); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("size error = %v, want ErrInvalidDTO", err)
	}
}
