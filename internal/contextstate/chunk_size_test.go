package contextstate

import "testing"

func TestPayloadChunkSizeDefaultAndOverride(t *testing.T) {
	SetLimits(Limits{})
	t.Cleanup(func() { SetLimits(DefaultLimits()) })
	if got := PayloadChunkSize(); got != DefaultPayloadChunkBytes {
		t.Fatalf("default chunk size = %d, want %d", got, DefaultPayloadChunkBytes)
	}
	SetLimits(Limits{SourceEventBytes: 8192})
	if got := PayloadChunkSize(); got != 8192 {
		t.Fatalf("override chunk size = %d, want 8192", got)
	}
}
