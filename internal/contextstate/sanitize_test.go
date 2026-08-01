package contextstate

import (
	"context"
	"errors"
	"testing"
)

func TestSanitizeSourcePayloadConfiguredAndHashOnly(t *testing.T) {
	principal, err := NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("bounded source value")

	hashOnly, err := SanitizeSourcePayload(context.Background(), principal, data, RedactionPolicy{})
	if err != nil {
		t.Fatalf("hash-only sanitize: %v", err)
	}
	if !hashOnly.HashOnly || hashOnly.Dereferenceable || len(hashOnly.Bytes) != 0 {
		t.Fatalf("unconfigured payload = %+v, want non-dereferenceable hash-only", hashOnly)
	}

	// A flagged payload is never refused - that would destroy a finished turn
	// (INV-AG-35). With no redactor configured it degrades to metadata, which
	// stores nothing and so leaks nothing.
	configured := RedactionPolicy{Configured: true, Patterns: []string{"blocked-value"}}
	flagged, err := SanitizeSourcePayload(context.Background(), principal, []byte("contains blocked-value"), configured)
	if err != nil {
		t.Fatalf("configured classifier refused a payload: %v", err)
	}
	if !flagged.HashOnly || flagged.Dereferenceable || len(flagged.Bytes) != 0 {
		t.Fatalf("flagged payload = %+v, want hash-only", flagged)
	}
	configured.Patterns = []string{"not-present"}
	full, err := SanitizeSourcePayload(context.Background(), principal, data, configured)
	if err != nil {
		t.Fatalf("configured sanitize: %v", err)
	}
	if full.HashOnly || !full.Dereferenceable || string(full.Bytes) != string(data) {
		t.Fatalf("configured payload = %+v, want bounded bytes", full)
	}
	if _, err := SanitizeSourcePayload(context.Background(), principal, []byte{0xff}, configured); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("malformed UTF-8 error = %v, want ErrInvalidDTO", err)
	}
}
