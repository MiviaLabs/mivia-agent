package contextstate

import "sync/atomic"

// DefaultPayloadChunkBytes is the built-in source-event payload chunk size when
// SourceEventBytes is 0 (uncapped volume). Large payloads are split into
// ordered chunks of this size under one content ref; they are never rejected
// for whole-payload size under defaults.
const DefaultPayloadChunkBytes = 64 << 10 // 64 KiB

// Limits is the single declaration of every durable context bound that scales
// with how much the user and the model actually said.
//
// A ZERO field means UNCAPPED, and every field is zero by default. That is not
// an oversight, it is the lesson of the wedge this file exists to prevent: the
// bounds used to be compiled-in constants (32 KiB of active context, 64 KiB per
// message) sized for a demo, while the models this product ships against carry
// 200k-1M token windows and `read_file`, `grep` and `run_command` results are
// uncapped by default. The first turn in which the agent did real work
// therefore exceeded them, and because publication is one transaction the WHOLE
// commit was refused - no source events, no checkpoint, no operation row. An
// active context only grows, so that turn wedged the session permanently and
// history stopped persisting with no way back.
//
// A durable bound must never be able to destroy work the agent already
// finished. Anything set here is a deliberate operator ceiling, chosen by
// someone who knows their storage, not a default this binary imposes.
//
// Bounds that describe SHAPE rather than volume stay compiled in, because they
// are correctness invariants rather than capacity policy: identifier and
// reference lengths, the source-range span that keeps range arithmetic honest,
// and the host-authored metadata envelopes the summarizer produces.
type Limits struct {
	// SourceEventBytes is the payload CHUNK size for durable source events
	// ([context] max_source_event_bytes). 0 means use DefaultPayloadChunkBytes.
	// It is NOT a whole-payload reject bound: multi-chunk payloads of any size
	// reassemble under one content ref.
	SourceEventBytes int
	// CheckpointBytes bounds a checkpoint's serialized active context, which is
	// the conversation the provider is sent. Its natural ceiling is the model's
	// prompt budget, which the planner already enforces upstream.
	CheckpointBytes int
	// CommitEvents bounds how many messages one turn may publish.
	CommitEvents int
	// CommitEventBytes bounds the aggregate payload bytes of one turn.
	CommitEventBytes int
	// SessionStateBytes bounds a stored session's serialized message state.
	SessionStateBytes int
	// ExportBytes bounds a context export.
	ExportBytes int
	// SummaryMetadataBytes bounds the persisted summary envelope.
	// Zero (uncapped by default) means the host imposes no compiled-in ceiling
	// on model-generated summary content.
	SummaryMetadataBytes int
	// CheckpointMetadataBytes bounds the summary_metadata column within a
	// checkpoint record. Zero means uncapped.
	CheckpointMetadataBytes int
}

// DefaultLimits is the shipped policy: every volume bound uncapped.
func DefaultLimits() Limits { return Limits{} }

var activeLimits atomic.Pointer[Limits]

// SetLimits installs the operator's durable ceilings process-wide. The host
// calls it once during startup from configuration, exactly as it installs the
// redaction policy; a process that never calls it stays uncapped, so any path
// that runs before startup - tests, `mivia version`, a store constructed
// directly - is uncapped rather than falling back to a compiled ceiling.
func SetLimits(limits Limits) {
	stored := limits
	activeLimits.Store(&stored)
}

// CurrentLimits returns the installed ceilings, or the uncapped default.
func CurrentLimits() Limits {
	if stored := activeLimits.Load(); stored != nil {
		return *stored
	}
	return DefaultLimits()
}

// exceedsLimit reports whether size breaks an ENABLED bound. A zero or negative
// bound is uncapped, so the comparison is skipped rather than treated as zero.
func exceedsLimit(size, bound int) bool { return bound > 0 && size > bound }

// Exceeds is exceedsLimit for hosts outside this package, so every layer asks
// the same question about a bound and a zero never reads as "allow nothing".
func Exceeds(size, bound int) bool { return exceedsLimit(size, bound) }

// PayloadChunkSize returns the effective per-chunk byte size for source-event
// payloads. Zero / negative SourceEventBytes settles to DefaultPayloadChunkBytes
// so storage always has a finite chunk granularity without whole-payload reject.
func PayloadChunkSize() int {
	n := CurrentLimits().SourceEventBytes
	if n <= 0 {
		return DefaultPayloadChunkBytes
	}
	return n
}
