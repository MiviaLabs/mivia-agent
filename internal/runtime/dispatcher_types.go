package runtime

import "time"

// Metadata is the audit record for one invocation.
//
// InputPreview and OutputPreview are bounded previews of the payloads: at most
// 256 bytes each. They are redacted ONLY to the extent the workspace's
// configured redaction policy removes something; an unconfigured workspace
// gets raw content, so treat them as payload, not as sanitised text. They are
// empty unless a Policy.Sink is attached - with no sink there is no consumer,
// so the previews are not computed at all.
type Metadata struct {
	ID, ParentID, TurnID, Name, Kind, Status, Scope, InputHash, OutputHash string
	Duration                                                               time.Duration
	InputPreview, OutputPreview                                            string
}

// Event is one dispatched invocation's lifecycle observation for a Policy.Sink.
type Event struct {
	Type     string
	Metadata Metadata
}
