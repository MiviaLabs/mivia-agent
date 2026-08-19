package uievent

import (
	"encoding/json"
	"fmt"
	"time"
)

// wireEvent is the on-disk/on-wire shape: Kind decides which Go type Body
// unmarshals into. Used by testdata/ fixtures and the --output json
// renderer.
type wireEvent struct {
	Kind   Kind            `json:"kind"`
	TurnID string          `json:"turn_id"`
	Seq    uint64          `json:"seq"`
	At     time.Time       `json:"at"`
	Body   json.RawMessage `json:"body"`
}

// MarshalJSON implements a discriminated union: Kind names the Go type,
// Body carries that type's fields.
func (e Event) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(e.Body)
	if err != nil {
		return nil, fmt.Errorf("uievent: marshal body for kind %q: %w", e.Kind, err)
	}
	return json.Marshal(wireEvent{
		Kind:   e.Kind,
		TurnID: e.TurnID,
		Seq:    e.Seq,
		At:     e.At,
		Body:   body,
	})
}

// UnmarshalJSON reconstructs the correct Body type from Kind.
func (e *Event) UnmarshalJSON(data []byte) error {
	var w wireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("uievent: unmarshal envelope: %w", err)
	}

	body, err := unmarshalBody(w.Kind, w.Body)
	if err != nil {
		return err
	}

	e.Kind = w.Kind
	e.TurnID = w.TurnID
	e.Seq = w.Seq
	e.At = w.At
	e.Body = body
	return nil
}

func unmarshalBody(kind Kind, raw json.RawMessage) (Body, error) {
	var body Body
	switch kind {
	case KindTurnStart:
		body = &TurnStartBody{}
	case KindTextDelta:
		body = &TextDeltaBody{}
	case KindTextEnd:
		body = &TextEndBody{}
	case KindReasoning:
		body = &ReasoningDeltaBody{}
	case KindToolPending:
		body = &ToolPendingBody{}
	case KindToolStart:
		body = &ToolStartBody{}
	case KindToolOutput:
		body = &ToolOutputBody{}
	case KindToolEnd:
		body = &ToolEndBody{}
	case KindPlan:
		body = &PlanBody{}
	case KindNotice:
		body = &NoticeBody{}
	case KindUsage:
		body = &UsageBody{}
	case KindError:
		body = &ErrorBody{}
	case KindTurnEnd:
		body = &TurnEndBody{}
	default:
		return nil, fmt.Errorf("uievent: unknown kind %q", kind)
	}
	if err := json.Unmarshal(raw, body); err != nil {
		return nil, fmt.Errorf("uievent: unmarshal body for kind %q: %w", kind, err)
	}
	return derefBody(body), nil
}

// derefBody returns the pointed-to value so Body holds the same
// (non-pointer) concrete type MarshalJSON produces from a literal, keeping
// equality comparisons in tests straightforward.
func derefBody(body Body) Body {
	switch b := body.(type) {
	case *TurnStartBody:
		return *b
	case *TextDeltaBody:
		return *b
	case *TextEndBody:
		return *b
	case *ReasoningDeltaBody:
		return *b
	case *ToolPendingBody:
		return *b
	case *ToolStartBody:
		return *b
	case *ToolOutputBody:
		return *b
	case *ToolEndBody:
		return *b
	case *PlanBody:
		return *b
	case *NoticeBody:
		return *b
	case *UsageBody:
		return *b
	case *ErrorBody:
		return *b
	case *TurnEndBody:
		return *b
	default:
		return body
	}
}
