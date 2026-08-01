package contextstate

import (
	"bytes"
	"fmt"
)

func validateCommitRequest(r CommitRequest) error {
	if err := validateCommitIdentity(r); err != nil {
		return err
	}
	if err := validateCommitRevision(r); err != nil {
		return err
	}
	if err := validateCommitEvents(r); err != nil {
		return err
	}
	if err := validateCommitPayloads(r); err != nil {
		return err
	}
	return validateCommitCheckpoint(r)
}

func validateCommitIdentity(r CommitRequest) error {
	if err := r.Principal.Validate(); err != nil {
		return err
	}
	if !r.Principal.IsBound() {
		return invalid("principal", "owner capability is not bound")
	}
	if r.SessionID != r.Principal.SessionID {
		return invalid("session_id", "does not match principal")
	}
	if err := validateIdentifier("operation_id", r.OperationID); err != nil {
		return err
	}
	if len(r.BaseDigest) != 64 || !isLowerHex(r.BaseDigest) {
		return invalid("base_digest", "must be a lowercase SHA-256 digest")
	}
	if len(r.Fingerprint) != 64 || !isLowerHex(r.Fingerprint) {
		return invalid("fingerprint", "must be a lowercase SHA-256 digest")
	}
	want, err := FingerprintCommitRequest(r)
	if err != nil {
		return err
	}
	if r.Fingerprint != want {
		return invalid("fingerprint", "does not match request contents")
	}
	if err := r.ExpectedBinding.Validate(); err != nil {
		return err
	}
	return r.NewBinding.Validate()
}

func validateCommitRevision(r CommitRequest) error {
	if r.NewSession != r.Expected.Session+1 || r.NewDurable != r.Expected.Durable+1 {
		return invalid("revision", "new revisions are not the next revision")
	}
	if r.NewSourceSequence < r.Expected.Source {
		return invalid("new_source_sequence", "outside source sequence limit")
	}
	if bound := CurrentLimits().CommitEvents; bound > 0 && r.NewSourceSequence-r.Expected.Source > uint64(bound) {
		return invalid("new_source_sequence", "outside source sequence limit")
	}
	if r.NewSourceSequence != r.Expected.Source+uint64(len(r.NewSourceEvents)) {
		return invalid("new_source_sequence", "does not match source events")
	}
	return nil
}

func validateCommitEvents(r CommitRequest) error {
	limits := CurrentLimits()
	if exceedsLimit(len(r.NewSourceEvents), limits.CommitEvents) {
		return invalid("new_source_events", "too many source events")
	}
	total := 0
	for i, event := range r.NewSourceEvents {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("source event %d: %w", i, err)
		}
		if event.ID.SessionID != r.SessionID {
			return invalid("new_source_events", "event belongs to another session")
		}
		if event.ID.Sequence != r.Expected.Source+uint64(i)+1 {
			return invalid("new_source_events", "source sequence is not contiguous")
		}
		total += event.Size
	}
	if exceedsLimit(total, limits.CommitEventBytes) {
		return invalid("new_source_events", "aggregate event bytes exceed limit")
	}
	return nil
}

func validateCommitPayloads(r CommitRequest) error {
	for i, payload := range r.Payloads {
		if err := payload.Validate(); err != nil {
			return fmt.Errorf("payload %d: %w", i, err)
		}
		ref := payload.Ref
		if ref.SessionID != r.SessionID || ref.WorkspaceID != r.Principal.WorkspaceID || ref.SubjectID != r.Principal.SubjectID {
			return invalid("payloads", "payload owner does not match principal")
		}
	}
	return nil
}

func validateCommitCheckpoint(r CommitRequest) error {
	if err := r.Checkpoint.Validate(); err != nil {
		return err
	}
	wantRevision := Revision{Session: r.NewSession, Durable: r.NewDurable, Source: r.NewSourceSequence}
	if r.Checkpoint.Revision != wantRevision {
		return invalid("checkpoint.revision", "does not match new revision")
	}
	if r.Checkpoint.Binding != r.NewBinding {
		return invalid("checkpoint.binding", "does not match new binding")
	}
	if r.Checkpoint.TurnID != r.TurnID || r.TurnID == 0 {
		return invalid("turn_id", "does not match checkpoint")
	}
	if !bytes.Equal(r.ActiveContext, r.Checkpoint.ActiveContext) || len(r.ActiveContext) == 0 {
		return invalid("active_context", "does not match checkpoint")
	}
	if exceedsLimit(len(r.ActiveContext), CurrentLimits().SessionStateBytes) {
		return invalid("active_context", "session state is too large")
	}
	if len(r.NewSourceEvents) > 0 {
		first := r.NewSourceEvents[0].ID.Sequence
		last := r.NewSourceEvents[len(r.NewSourceEvents)-1].ID.Sequence
		if r.Checkpoint.SourceRange.Start.Sequence > first || r.Checkpoint.SourceRange.End.Sequence != last {
			return invalid("checkpoint.source_range", "does not cover new source events")
		}
	} else if r.Checkpoint.SourceRange.End.Sequence != r.Expected.Source {
		return invalid("checkpoint.source_range", "empty commit range does not end at source head")
	}
	if !r.Checkpoint.Complete {
		return invalid("checkpoint.complete", "checkpoint is not complete")
	}
	return nil
}
