package contextstate

import (
	"crypto/sha256"
	"errors"
	"testing"
)

func TestNewSourceRangeValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		start   SourceID
		end     SourceID
		wantErr bool
	}{
		{"valid same event", SourceID{SessionID: "s", Sequence: 1}, SourceID{SessionID: "s", Sequence: 1}, false},
		{"valid range", SourceID{SessionID: "s", Sequence: 1}, SourceID{SessionID: "s", Sequence: 3}, false},
		{"different sessions", SourceID{SessionID: "a", Sequence: 1}, SourceID{SessionID: "b", Sequence: 1}, true},
		{"start after end", SourceID{SessionID: "s", Sequence: 3}, SourceID{SessionID: "s", Sequence: 1}, true},
		{"invalid start", SourceID{SessionID: "", Sequence: 1}, SourceID{SessionID: "s", Sequence: 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSourceRange(tt.start, tt.end)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSourceRange() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidDTO) {
				t.Errorf("NewSourceRange() error = %v, want ErrInvalidDTO wrapped", err)
			}
		})
	}
}

func TestSourceRangeExceedsLimit(t *testing.T) {
	start := SourceID{SessionID: "s", Sequence: 1}
	end := SourceID{SessionID: "s", Sequence: MaxSourceRangeEvents + 1}
	_, err := NewSourceRange(start, end)
	if err == nil {
		t.Fatal("expected range limit error")
	}
}

func TestNewBindingRevisionValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		provider   string
		model      string
		generation uint64
		wantErr    bool
	}{
		{"valid", "p", "m", 1, false},
		{"zero generation", "p", "m", 0, true},
		{"empty provider", "", "m", 1, true},
		{"empty model", "p", "", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewBindingRevision(tt.provider, tt.model, tt.generation)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewBindingRevision() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewRevisionValidation(t *testing.T) {
	t.Parallel()
	// Revision.Validate always returns nil
	r := NewRevision(1, 2, 3)
	if err := r.Validate(); err != nil {
		t.Errorf("Revision.Validate() error = %v", err)
	}
}

func TestNewCheckpointIDValidation(t *testing.T) {
	t.Parallel()
	validSource := func() SourceRange {
		s := SourceID{SessionID: "s", Sequence: 1}
		e := SourceID{SessionID: "s", Sequence: 2}
		r, _ := NewSourceRange(s, e)
		return r
	}
	tests := []struct {
		name          string
		sessionID     string
		sourceRange   SourceRange
		algorithm     string
		schemaVersion uint32
		summaryModel  string
		key           string
		wantErr       bool
	}{
		{"valid", "s", validSource(), "alg", 1, "model", "key", false},
		{"empty session", "", validSource(), "alg", 1, "model", "key", true},
		{"empty algorithm", "s", validSource(), "", 1, "model", "key", true},
		{"zero schema", "s", validSource(), "alg", 0, "model", "key", true},
		{"empty key", "s", validSource(), "alg", 1, "model", "", true},
		{"session mismatch with range", "other", validSource(), "alg", 1, "model", "key", true},
		{"empty summary_model is allowed", "s", validSource(), "alg", 1, "", "key", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCheckpointID(tt.sessionID, tt.sourceRange, tt.algorithm, tt.schemaVersion, tt.summaryModel, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewCheckpointID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewPrincipalValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		workspaceID string
		sessionID   string
		subjectID   string
		wantErr     bool
	}{
		{"valid", "ws", "sess", "subj", false},
		{"empty workspace", "", "sess", "subj", true},
		{"empty session", "ws", "", "subj", true},
		{"empty subject", "ws", "sess", "", true},
		{"with spaces", "ws ", "sess", "subj", false},
		{"with control char", "ws\t", "sess", "subj", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPrincipal(tt.workspaceID, tt.sessionID, tt.subjectID)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewPrincipal() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !p.IsBound() {
				t.Error("valid principal should be bound")
			}
			if err == nil && p.CapabilityDigest() == "" {
				t.Error("bound principal should have a capability digest")
			}
		})
	}
}

func TestPrincipalCapabilityDigest(t *testing.T) {
	// Unbound principal returns empty digest
	p := Principal{WorkspaceID: "ws", SessionID: "s", SubjectID: "sub"}
	if p.IsBound() {
		t.Fatal("zero-value principal should not be bound")
	}
	if p.CapabilityDigest() != "" {
		t.Fatal("unbound principal should return empty digest")
	}
}

func TestValidateBoundedText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		field      string
		value      string
		max        int
		allowEmpty bool
		wantErr    bool
	}{
		{"valid", "f", "hello", 128, false, false},
		{"empty disallowed", "f", "", 128, false, true},
		{"empty allowed", "f", "", 128, true, false},
		{"whitespace disallowed", "f", "  ", 128, false, true},
		{"whitespace allowed", "f", "  ", 128, true, false},
		{"too long", "f", string(make([]byte, 129)), 128, false, true},
		{"control char", "f", "hel\x00lo", 128, false, true},
		{"valid unicode", "f", "héllo", 128, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBoundedText(tt.field, tt.value, tt.max, tt.allowEmpty)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBoundedText() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsLowerHex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty", "", true},
		{"lowercase hex", "abcdef0123456789", true},
		{"uppercase", "ABCDEF", false},
		{"mixed", "aBcDeF", false},
		{"with G", "abcdefg", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLowerHex(tt.value); got != tt.want {
				t.Errorf("isLowerHex(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestContentRefValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ref  ContentRef
		err  bool
	}{
		{"valid", ContentRef{
			Ref: "ctxp_abc123", Namespace: Namespace,
			SHA256:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 10,
		}, false},
		{"wrong namespace", ContentRef{
			Ref: "x", Namespace: "wrong",
			SHA256:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 10,
		}, true},
		{"bad sha length", ContentRef{
			Ref: "x", Namespace: Namespace, SHA256: "abc",
			WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 10,
		}, true},
		{"uppercase sha", ContentRef{
			Ref: "x", Namespace: Namespace,
			SHA256:      "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789",
			WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 10,
		}, true},
		{"negative size", ContentRef{
			Ref: "x", Namespace: Namespace,
			SHA256:      "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: -1,
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.ref.Validate(); (err != nil) != tt.err {
				t.Errorf("ContentRef.Validate() error = %v, want err %v", err, tt.err)
			}
		})
	}
}

func TestPayloadRecordValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rec  PayloadRecord
		err  bool
	}{
		{"valid with data", PayloadRecord{
			Ref: ContentRef{
				Ref: "ctxp_abc", Namespace: Namespace,
				SHA256:      sha256Hex([]byte("hello")),
				WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 5,
			},
			Retention: RetentionSession, Data: []byte("hello"),
		}, false},
		{"empty retention", PayloadRecord{
			Ref: ContentRef{
				Ref: "ctxp_abc", Namespace: Namespace,
				SHA256:      sha256Hex([]byte("hello")),
				WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 5,
			},
		}, true},
		{"size mismatch", PayloadRecord{
			Ref: ContentRef{
				Ref: "ctxp_abc", Namespace: Namespace,
				SHA256:      sha256Hex([]byte("hello")),
				WorkspaceID: "ws", SessionID: "s", SubjectID: "sub", Size: 99,
			},
			Retention: RetentionSession, Data: []byte("hello"),
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.rec.Validate(); (err != nil) != tt.err {
				t.Errorf("PayloadRecord.Validate() error = %v, want err %v", err, tt.err)
			}
		})
	}
}

func TestSourceEventContractValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		event SourceEvent
		err   bool
	}{
		{"valid", SourceEvent{
			ID:   SourceID{SessionID: "s", Sequence: 1},
			Kind: "message", Role: "user", Provenance: "host",
			RedactionStatus: "metadata", Size: 10,
		}, false},
		{"empty kind", SourceEvent{
			ID:   SourceID{SessionID: "s", Sequence: 1},
			Kind: "", Role: "user", Provenance: "host",
			RedactionStatus: "metadata", Size: 10,
		}, true},
		{"empty role", SourceEvent{
			ID:   SourceID{SessionID: "s", Sequence: 1},
			Kind: "message", Role: "", Provenance: "host",
			RedactionStatus: "metadata", Size: 10,
		}, true},
		{"negative size", SourceEvent{
			ID:   SourceID{SessionID: "s", Sequence: 1},
			Kind: "message", Role: "user", Provenance: "host",
			RedactionStatus: "metadata", Size: -1,
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.event.Validate(); (err != nil) != tt.err {
				t.Errorf("SourceEvent.Validate() error = %v, want err %v", err, tt.err)
			}
		})
	}
}

func sha256Hex(data []byte) string {
	d := sha256.Sum256(data)
	return hexEncode(d[:])
}

func hexEncode(b []byte) string {
	const h = "0123456789abcdef"
	result := make([]byte, len(b)*2)
	for i, v := range b {
		result[i*2] = h[v>>4]
		result[i*2+1] = h[v&0x0f]
	}
	return string(result)
}

func TestCheckpointRecordContractValidation(t *testing.T) {
	t.Parallel()
	source := func() SourceRange {
		s := SourceID{SessionID: "s", Sequence: 1}
		e := SourceID{SessionID: "s", Sequence: 1}
		r, _ := NewSourceRange(s, e)
		return r
	}
	binding := func() BindingRevision {
		b, _ := NewBindingRevision("p", "m", 1)
		return b
	}
	cid := func() CheckpointID {
		id, _ := NewCheckpointID("s", source(), "alg", 1, "m", "key")
		return id
	}
	tests := []struct {
		name string
		rec  CheckpointRecord
		err  bool
	}{
		{"valid", CheckpointRecord{
			ID: cid(), Revision: Revision{Session: 1, Durable: 1, Source: 1},
			Binding: binding(), SourceRange: source(),
			ActiveContext: []byte(`{"m":[]}`), TurnID: 1,
		}, false},
		{"empty active context", CheckpointRecord{
			ID: cid(), Revision: Revision{Session: 1, Durable: 1, Source: 1},
			Binding: binding(), SourceRange: source(),
			ActiveContext: []byte{}, TurnID: 1,
		}, true},
		{"zero turn id", CheckpointRecord{
			ID: cid(), Revision: Revision{Session: 1, Durable: 1, Source: 1},
			Binding: binding(), SourceRange: source(),
			ActiveContext: []byte(`{"m":[]}`), TurnID: 0,
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.rec.Validate(); (err != nil) != tt.err {
				t.Errorf("CheckpointRecord.Validate() error = %v, want err %v", err, tt.err)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	err := invalid("field", "reason")
	if !errors.Is(err, ErrInvalidDTO) {
		t.Error("invalid() should wrap ErrInvalidDTO")
	}
	ve := &ValidationError{Field: "f", Reason: "r"}
	if !errors.Is(ve, ErrInvalidDTO) {
		t.Error("ValidationError should unwrap to ErrInvalidDTO")
	}
	if ve.Error() != "invalid context DTO: f: r" {
		t.Errorf("unexpected error message: %s", ve.Error())
	}
}

func TestMarshalCanonical(t *testing.T) {
	t.Parallel()
	// Test with a map that should produce sorted keys
	input := map[string]string{"b": "2", "a": "1"}
	data, err := MarshalCanonical(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"a":"1","b":"2"}` {
		t.Errorf("MarshalCanonical() = %s, want sorted keys", string(data))
	}
}

func TestUnmarshalCanonical(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		data    string
		target  any
		wantErr bool
	}{
		{"valid", `{"a":"1"}`, &map[string]string{}, false},
		{"nil target", `{}`, nil, true},
		{"multiple values", `{"a":1}{"b":2}`, &map[string]string{}, true},
		{"invalid UTF-8", "{\xff}", &map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := UnmarshalCanonical([]byte(tt.data), tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalCanonical() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
