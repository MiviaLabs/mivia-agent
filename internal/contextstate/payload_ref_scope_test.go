package contextstate

import (
	"context"
	"testing"
)

func mustPayloadRef(t *testing.T, workspaceID, sessionID, subjectID, content string) ContentRef {
	t.Helper()
	principal, err := NewPrincipal(workspaceID, sessionID, subjectID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := SanitizeSourcePayload(context.Background(), principal, []byte(content), RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := payload.Ref.Validate(); err != nil {
		t.Fatalf("minted reference is invalid: %v", err)
	}
	return payload.Ref
}

// TestSourcePayloadRefIsScopedToItsOwner pins the fix for the live defect.
//
// context_payloads.ref is a GLOBAL primary key whose stored owner columns must
// equal the writing principal. While the reference was the bare content
// digest, the first session in a workspace store to persist a byte sequence
// owned that key forever: every later session that produced identical content
// - the same greeting typed twice across two `mivia chat` runs - had its whole
// commit transaction rejected with ErrCheckpointConflict, leaving no source
// events, no checkpoint and no operation row, and losing the turn.
func TestSourcePayloadRefIsScopedToItsOwner(t *testing.T) {
	const content = "hello"
	first := mustPayloadRef(t, "workspace", "session-one", "local-user", content)
	second := mustPayloadRef(t, "workspace", "session-two", "local-user", content)

	if first.Ref == second.Ref {
		t.Fatalf("two sessions minted the same payload key %q for identical content", first.Ref)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("content digest changed with the owner: %q vs %q", first.SHA256, second.SHA256)
	}
	if first.Size != len(content) || second.Size != len(content) {
		t.Fatalf("sizes = %d/%d, want %d", first.Size, second.Size, len(content))
	}

	otherSubject := mustPayloadRef(t, "workspace", "session-one", "other-user", content)
	if otherSubject.Ref == first.Ref {
		t.Fatalf("two subjects minted the same payload key %q", first.Ref)
	}
	otherWorkspace := mustPayloadRef(t, "other-workspace", "session-one", "local-user", content)
	if otherWorkspace.Ref == first.Ref {
		t.Fatalf("two workspaces minted the same payload key %q", first.Ref)
	}
}

// TestSourcePayloadRefDeduplicatesWithinOneOwner keeps the property the global
// key did provide: one owner repeating the same content resolves to one row.
func TestSourcePayloadRefDeduplicatesWithinOneOwner(t *testing.T) {
	principal, err := NewPrincipal("workspace", "session", "local-user")
	if err != nil {
		t.Fatal(err)
	}
	first, err := SanitizeSourcePayload(context.Background(), principal, []byte("repeated"), RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := SanitizeSourcePayload(context.Background(), principal, []byte("repeated"), RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref != second.Ref {
		t.Fatalf("one owner minted two keys for identical content: %+v vs %+v", first.Ref, second.Ref)
	}
	different, err := SanitizeSourcePayload(context.Background(), principal, []byte("repeatee"), RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if different.Ref.Ref == first.Ref.Ref {
		t.Fatalf("distinct content shares a payload key %q", first.Ref.Ref)
	}
}

// TestSourcePayloadRefOwnerEncodingIsUnambiguous rejects a scope built by
// concatenation: owner tuples that differ only in where the boundaries fall
// must not collapse onto one key.
func TestSourcePayloadRefOwnerEncodingIsUnambiguous(t *testing.T) {
	const content = "same bytes"
	left := mustPayloadRef(t, "workspace", "ab", "cd", content)
	right := mustPayloadRef(t, "workspace", "a", "bcd", content)
	if left.Ref == right.Ref {
		t.Fatalf("ambiguous owner encoding collapsed %q", left.Ref)
	}
	outer := mustPayloadRef(t, "wo", "rkspaceab", "cd", content)
	if outer.Ref == left.Ref {
		t.Fatalf("ambiguous owner encoding collapsed across fields: %q", left.Ref)
	}
}
