package chatsync

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// SyncTelemetry records local synchronization stages. Counters are process
// local; log records carry identifiers for one event and upload attempt.
type SyncTelemetry struct {
	logger         *slog.Logger
	produced       atomic.Uint64
	projected      atomic.Uint64
	appended       atomic.Uint64
	uploaded       atomic.Uint64
	uploadFailures atomic.Uint64
	lastAckSeq     atomic.Int64
	lastSuccessAt  atomic.Int64
	outboxDepth    atomic.Int64
}

// NewSyncTelemetry creates telemetry using logger. A nil logger disables logs
// but keeps counters active.
func NewSyncTelemetry(logger *slog.Logger) *SyncTelemetry {
	return &SyncTelemetry{logger: logger}
}

// SyncTelemetrySnapshot is a point-in-time metrics view for diagnostics.
type SyncTelemetrySnapshot struct {
	Produced       uint64
	Projected      uint64
	Appended       uint64
	Uploaded       uint64
	UploadFailures uint64
	LastAckSeq     int64
	LastSuccessAt  time.Time
	OutboxDepth    int64
}

func (t *SyncTelemetry) Snapshot() SyncTelemetrySnapshot {
	if t == nil {
		return SyncTelemetrySnapshot{}
	}
	last := t.lastSuccessAt.Load()
	var lastAt time.Time
	if last != 0 {
		lastAt = time.Unix(0, last)
	}
	return SyncTelemetrySnapshot{
		Produced:       t.produced.Load(),
		Projected:      t.projected.Load(),
		Appended:       t.appended.Load(),
		Uploaded:       t.uploaded.Load(),
		UploadFailures: t.uploadFailures.Load(),
		LastAckSeq:     t.lastAckSeq.Load(),
		LastSuccessAt:  lastAt,
		OutboxDepth:    t.outboxDepth.Load(),
	}
}

func (t *SyncTelemetry) record(stage, sessionID, writerID, batchID string, seq int64, attrs ...any) {
	if t == nil || t.logger == nil {
		return
	}
	t.logger.Info("chat sync", append([]any{
		"stage", stage,
		"session_id", sessionID,
		"writer_id", writerID,
		"upload_batch_id", batchID,
		"event_seq", seq,
	}, attrs...)...)
}

func (t *SyncTelemetry) producedEvent(sessionID, writerID string, seq int64, kind string) {
	if t == nil {
		return
	}
	t.produced.Add(1)
	t.record("event_produced", sessionID, writerID, "", seq, "event_kind", kind)
}

func (t *SyncTelemetry) projectedEvent(sessionID, writerID string, seq int64, kind string) {
	if t == nil {
		return
	}
	t.projected.Add(1)
	t.record("event_projected", sessionID, writerID, "", seq, "event_kind", kind)
}

func (t *SyncTelemetry) appendedEvent(sessionID, writerID string, seq int64, depth int) {
	if t == nil {
		return
	}
	t.appended.Add(1)
	t.outboxDepth.Store(int64(depth))
	t.record("event_appended_to_outbox", sessionID, writerID, "", seq, "outbox_depth", depth)
}

func (t *SyncTelemetry) uploadStarted(sessionID, writerID, batchID string, first, last int64, depth int) {
	if t == nil {
		return
	}
	t.outboxDepth.Store(int64(depth))
	t.record("upload_started", sessionID, writerID, batchID, first, "last_event_seq", last, "outbox_depth", depth)
}

func (t *SyncTelemetry) uploadFinished(sessionID, writerID, batchID string, ack int64, depth, moved int) {
	if t == nil {
		return
	}
	t.uploaded.Add(uint64(moved))
	t.lastAckSeq.Store(ack)
	t.lastSuccessAt.Store(time.Now().UnixNano())
	t.outboxDepth.Store(int64(depth))
	t.record("upload_acknowledged", sessionID, writerID, batchID, ack, "inserted_events", moved, "outbox_depth", depth)
}

func (t *SyncTelemetry) uploadFailed(sessionID, writerID, batchID string, first int64, depth int, err error) {
	if t == nil {
		return
	}
	t.uploadFailures.Add(1)
	t.outboxDepth.Store(int64(depth))
	t.record("upload_failed", sessionID, writerID, batchID, first, "outbox_depth", depth, "error", err.Error())
}
