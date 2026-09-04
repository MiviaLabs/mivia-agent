package chatsync

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func (s *SyncSession) processEvent(ctx context.Context, ev events.Event) {
	s.mu.Lock()
	wireEvents := s.projector.ProjectWithDrops(ev, s.currentDrops())
	if len(wireEvents) == 0 {
		s.mu.Unlock()
		return
	}
	for _, wire := range wireEvents {
		s.telemetry.producedEvent(s.localSessionID, s.opts.ProjectorOptions.WriterID, wire.Seq, wire.Type)
		s.telemetry.projectedEvent(s.localSessionID, s.opts.ProjectorOptions.WriterID, wire.Seq, wire.Type)
	}

	if err := s.appendLocked(wireEvents); err != nil {
		// One SOURCE event was lost, whatever number of wire events its
		// projection produced. Counting wire events here would overstate the
		// hole against the two upstream counters, which both count source
		// events. The next projected event carries a sync.dropped marker
		// covering it, because currentDrops now includes this counter.
		//
		// A batch holding ONLY a sync.dropped marker is not counted: the
		// source event projected to nothing, so a healthy append would have
		// stored nothing for it either and no content was lost. The marker's
		// own loss is already repaired by RollbackDrops. Counting it would
		// report a hole that does not exist, and would grow by one for every
		// such event while an outbox stays full.
		if carriesContent(wireEvents) {
			s.appendDrops.Add(1)
		}
		s.mu.Unlock()
		return
	}
	depth := s.outbox.UnflushedCount()
	for _, wire := range wireEvents {
		s.telemetry.appendedEvent(s.localSessionID, s.opts.ProjectorOptions.WriterID, wire.Seq, depth)
	}
	s.mu.Unlock()

	// A wake, not a push: the uploader does the round trip on its own
	// goroutine, so this returns in microseconds whatever the network does.
	s.triggerFlush()
}

// appendLocked durably appends wireEvents and rolls the seq counter back on
// ANY failure, not only on overflow.
//
// A seq the projector consumed but the outbox never stored leaves a permanent
// hole in the stream. The server's contiguity check then rejects every later
// append with a 400 for the rest of the process lifetime, so a single transient
// write or fsync error wedges sync for good. The counter must therefore track
// what was STORED, never what was merely assigned.
//
// The caller must hold s.mu.
func (s *SyncSession) appendLocked(wireEvents []WireEvent) error {
	if err := s.appender.Append(wireEvents...); err != nil {
		s.projector.RollbackSeq(len(wireEvents))
		s.projector.RollbackDrops(droppedDelta(wireEvents))
		// The streaming counters are state too. Left advanced, they make the
		// settled message omit text whose fragments were never stored, and
		// the turn's whole reply is lost rather than degraded.
		s.projector.RollbackStreaming(wireEvents)
		return err
	}
	return nil
}

// carriesContent reports whether a projected batch holds anything other than
// loss markers - that is, whether losing it actually removes transcript
// content a reader would otherwise have seen.
func carriesContent(wireEvents []WireEvent) bool {
	for _, we := range wireEvents {
		if we.Type != TypeSyncDropped {
			return true
		}
	}
	return false
}

// droppedDelta sums the loss reported by the sync.dropped markers in a batch.
// A batch carries at most one, but summing keeps the helper correct rather than
// resting on that.
func droppedDelta(wireEvents []WireEvent) uint64 {
	var total uint64
	for _, we := range wireEvents {
		if we.Type != TypeSyncDropped {
			continue
		}
		if p, ok := we.Payload.(*SyncDroppedPayload); ok {
			total += p.Dropped
		}
	}
	return total
}

// currentDrops reports every event lost before projection. The caller must
// hold s.mu.
func (s *SyncSession) currentDrops() uint64 {
	if s.dropSource == nil {
		return 0
	}
	return s.dropSource()
}

// preProjectionDrops is the total loss this session must report: the bus's own
// drop-oldest shedding, this session's handler-to-worker channel, and events
// whose durable append failed. All three counters are monotonic, so their sum
// is monotonic and the projector's advance check stays correct.
//
// The third term is not, strictly, loss before projection - the projection was
// built and then thrown away. It belongs here anyway, because what the marker
// reports to a reader is "this transcript is missing N events", and a hole is a
// hole whichever hop dropped it. The alternative was a second, separate marker
// for the same fact.
func (s *SyncSession) preProjectionDrops() uint64 {
	var busDropped uint64
	if s.sub != nil {
		busDropped = s.sub.Drops()
	}
	return busDropped + s.chanDrops.Load() + s.appendDrops.Load()
}
