package ports

import "context"

// CompactionEvent is the UI-safe lifecycle signal for an asynchronous
// context compaction. It contains no chat or storage types.
type CompactionEvent struct {
	SessionID string
	Phase     string
	Detail    string
	Done      bool
	Notice    string
	Err       error
}

// CompactionHandle owns one asynchronous compaction operation.
type CompactionHandle interface {
	Events() <-chan CompactionEvent
	Cancel()
}

// AsyncCompactionRunner is an optional extension to CommandRunner. Keeping
// it optional preserves the small command seam used by embedded screens and
// tests while allowing the real adapter to be event driven.
type AsyncCompactionRunner interface {
	StartCompaction(context.Context, string) (CompactionHandle, error)
}
