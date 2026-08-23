package contextstate

import (
	"reflect"
	"testing"

	sdkctx "github.com/MiviaLabs/mivia-ai-sdk/contextstate"
)

// TestReexportedShapeConstants pins that the CLI's shape constants are
// the SDK's constants. A drift here means the CLI silently widened or
// tightened an identifier bound relative to the SDK.
func TestReexportedShapeConstants(t *testing.T) {
	t.Parallel()
	if MaxIdentifierBytes != sdkctx.MaxIdentifierBytes {
		t.Errorf("MaxIdentifierBytes = %d, want %d", MaxIdentifierBytes, sdkctx.MaxIdentifierBytes)
	}
	if MaxPayloadReferenceBytes != sdkctx.MaxPayloadReferenceBytes {
		t.Errorf("MaxPayloadReferenceBytes = %d, want %d", MaxPayloadReferenceBytes, sdkctx.MaxPayloadReferenceBytes)
	}
	if MaxSourceRangeEvents != sdkctx.MaxSourceRangeEvents {
		t.Errorf("MaxSourceRangeEvents = %d, want %d", MaxSourceRangeEvents, sdkctx.MaxSourceRangeEvents)
	}
	if HashPrefix != sdkctx.HashPrefix {
		t.Errorf("HashPrefix = %q, want %q", HashPrefix, sdkctx.HashPrefix)
	}
}

// TestReexportedRefHelpers pins that Digest, Mint, IsRef, and
// NewContentRef resolve to the SDK's implementations. A drift here
// means the CLI silently forked the ref-mint pipeline.
func TestReexportedRefHelpers(t *testing.T) {
	t.Parallel()
	if Digest([]byte("hello")) != sdkctx.Digest([]byte("hello")) {
		t.Error("Digest diverged from sdkctx.Digest")
	}
	if Mint([]byte("hello")) != sdkctx.Mint([]byte("hello")) {
		t.Error("Mint diverged from sdkctx.Mint")
	}
	if IsRef(sdkctx.Mint([]byte("hello"))) != sdkctx.IsRef(sdkctx.Mint([]byte("hello"))) {
		t.Error("IsRef diverged from sdkctx.IsRef")
	}
	got, err := NewContentRef("ns", "ws", "s", "sub", []byte("hello"))
	if err != nil {
		t.Fatalf("NewContentRef: %v", err)
	}
	want, err := sdkctx.NewContentRef("ns", "ws", "s", "sub", []byte("hello"))
	if err != nil {
		t.Fatalf("sdkctx.NewContentRef: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewContentRef = %+v, want %+v", got, want)
	}
}

// TestNewContentRefReturnTypeIsSDK pins that the re-exported
// NewContentRef returns the SDK's ContentRef, not the CLI's local
// ContentRef. This matters because callers may compare the returned
// value against sdkctx.ContentRef fields or pass it back to the SDK.
func TestNewContentRefReturnTypeIsSDK(t *testing.T) {
	t.Parallel()
	got, err := NewContentRef("ns", "ws", "s", "sub", []byte("hello"))
	if err != nil {
		t.Fatalf("NewContentRef: %v", err)
	}
	gotType := reflect.TypeOf(got)
	wantType := reflect.TypeOf(sdkctx.ContentRef{})
	if gotType != wantType {
		t.Errorf("NewContentRef return type = %v, want %v", gotType, wantType)
	}
}
