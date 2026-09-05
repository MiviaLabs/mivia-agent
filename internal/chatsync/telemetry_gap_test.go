package chatsync

import "testing"

func TestSyncTelemetryNilReceiverIsSafe(t *testing.T) {
	var nilTelemetry *SyncTelemetry
	if got := nilTelemetry.Snapshot(); got != (SyncTelemetrySnapshot{}) {
		t.Fatalf("nil SyncTelemetry.Snapshot() = %+v, want the zero value", got)
	}
	nilTelemetry.record("stage", "sess", "writer", "batch", 1) // must not panic
}

func TestSyncTelemetryRecordNoopsWithoutALogger(t *testing.T) {
	telemetry := NewSyncTelemetry(nil)
	telemetry.record("stage", "sess", "writer", "batch", 1) // must not panic
}
