package agentmsg

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// Kind is the fixed message-kind vocabulary. Unknown kinds are rejected.
type Kind string

const (
	KindFinding  Kind = "finding"  // child→parent, durable, blackboard
	KindQuestion Kind = "question" // child→parent, blocking (phase 02)
	KindAnswer   Kind = "answer"   // reply to question/ask (phases 03, 04)
	KindSteer    Kind = "steer"    // parent→child (phase 03)
	KindAsk      Kind = "ask"      // peer via parent router (phase 04)
)

// AskDeclinePrefix is the wire-format prefix for a system decline delivered
// as an ask answer body. A body starting with this prefix is never a peer's
// real answer: it is a stable, machine-readable decline signal (e.g. the ask
// target task finalized without answering). The CLI wave depends on this
// exact constant name and value — do not rename.
const AskDeclinePrefix = "\x00decline:"

// DeclineReasonTargetTerminal is the single vocabulary for "the ask's target
// is terminal / not live": one reason string for the same fact whether it is
// declined at ask time (target not running) or mid-park (responder reached
// terminal status without answering). Appended verbatim to AskDeclinePrefix
// to form the delivered answer body. The CLI wave depends on this exact
// constant name and value — do not rename.
const DeclineReasonTargetTerminal = "target_terminal"

// DeclineReasonResponderTerminal is the stable decline reason reported when
// the ask's target task reached terminal status without answering. The value
// is unified with the ask-time route decline reason
// (agentmsg.DeclineTargetNotRunning) — both describe a terminal target — but
// the exported name is retained so existing callers compile unchanged. The CLI
// wave depends on this exact constant name — do not rename.
const DeclineReasonResponderTerminal = DeclineReasonTargetTerminal

// DeclineReasonParentNonInteractive is the stable decline reason delivered at
// park time when a run's parent is a non-interactive controller that can never
// answer child questions. Generic mechanism: any parent that cannot mediate
// Q&A opts in via the run-creation flag, and the coordinator declines the
// asker's park immediately instead of letting it burn the full wait_seconds.
// Appended verbatim to AskDeclinePrefix to form the delivered answer body, so
// the CLI wait site reports {status:"no_answer", reason:...} with nil error.
const DeclineReasonParentNonInteractive = "parent_non_interactive"

// ParentSentinel is the To/From value for the parent principal.
const ParentSentinel = "parent"

// DefaultMaxBodyBytes is the default inline body budget (matches config default).
const DefaultMaxBodyBytes = 2048

// DefaultSynopsisBytes is the max synopsis length stamped into lifecycle payloads.
const DefaultSynopsisBytes = 256

// ErrInvalidMessage reports a message that fails strict validation.
var ErrInvalidMessage = errors.New("invalid agent message")

// Party identifies a message endpoint: a task, an agent role, or the parent.
type Party struct {
	TaskID string `json:"task_id,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Role   string `json:"role,omitempty"`
}

// IsParent reports whether p is the parent sentinel (empty TaskID/Agent/Role
// with no fields, or Role == ParentSentinel alone).
func (p Party) IsParent() bool {
	if p.Role == ParentSentinel && p.TaskID == "" && p.Agent == "" {
		return true
	}
	return p.TaskID == "" && p.Agent == "" && p.Role == ""
}

// Message is the typed, budgeted, attributable envelope for sparse messaging.
// Append-only: no mutation or deletion after construction.
type Message struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Kind      Kind      `json:"kind"`
	From      Party     `json:"from"`
	To        Party     `json:"to"`
	InReplyTo string    `json:"in_reply_to,omitempty"`
	Body      string    `json:"body"`
	Refs      []string  `json:"refs,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Interrupt marks a mid-step steer: the receiving task should stop its
	// current step and act on this steer immediately. Only KindSteer may
	// carry it (enforced by Validate).
	Interrupt bool `json:"interrupt,omitempty"`
}

// Options controls construction-time validation budgets.
type Options struct {
	// MaxBodyBytes bounds Body. Zero means DefaultMaxBodyBytes.
	MaxBodyBytes int
	// Now overrides the clock (tests). Nil uses time.Now.
	Now func() time.Time
	// ID overrides minted ID (tests). Empty mints a new durable ID.
	ID string
	// InReplyTo links an answer (or ask reply) to a prior message ID.
	InReplyTo string
	// Interrupt marks the message as a mid-step interrupt steer.
	Interrupt bool
}

// NewMessage constructs and validates a message. The ID is minted once
// (crypto/rand + base32, msg- prefix) unless Options.ID is set.
func NewMessage(runID string, kind Kind, from, to Party, body string, refs []string, opts Options) (Message, error) {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	id := opts.ID
	if id == "" {
		id = NewMessageID()
	}
	msg := Message{
		ID:        id,
		RunID:     runID,
		Kind:      kind,
		From:      from,
		To:        to,
		InReplyTo: opts.InReplyTo,
		Body:      body,
		Refs:      cloneRefs(refs),
		CreatedAt: now().UTC(),
		Interrupt: opts.Interrupt,
	}
	if err := Validate(msg, maxBody); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// Validate applies strict envelope rules. maxBodyBytes must be positive.
func Validate(msg Message, maxBodyBytes int) error {
	if maxBodyBytes <= 0 {
		return fmt.Errorf("%w: max body bytes must be positive", ErrInvalidMessage)
	}
	if msg.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidMessage)
	}
	if msg.RunID == "" {
		return fmt.Errorf("%w: run_id is required", ErrInvalidMessage)
	}
	if !ValidKind(msg.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidMessage, msg.Kind)
	}
	if !utf8.ValidString(msg.Body) {
		return fmt.Errorf("%w: body is not valid UTF-8", ErrInvalidMessage)
	}
	// NUL (U+0000) is valid UTF-8 but hostile here: the CLI wait sites treat a
	// body starting with the AskDeclinePrefix sentinel ("\x00decline:") as a
	// system decline, so a real peer answer body beginning with NUL would be
	// misread as a decline and silently dropped. Same policy as
	// internal/skills/resources.go validResourceText. The decline sentinel
	// itself is delivered only via the park channel (DeliverAnswer) and never
	// passes through NewMessage/Validate/PostTaskMessage, so this check cannot
	// block it.
	if strings.IndexByte(msg.Body, 0) >= 0 {
		return fmt.Errorf("%w: body contains NUL byte", ErrInvalidMessage)
	}
	if len(msg.Body) > maxBodyBytes {
		return fmt.Errorf("%w: body length %d exceeds max_body_bytes %d", ErrInvalidMessage, len(msg.Body), maxBodyBytes)
	}
	if msg.Kind == KindAnswer {
		if strings.TrimSpace(msg.InReplyTo) == "" {
			return fmt.Errorf("%w: answer requires in_reply_to", ErrInvalidMessage)
		}
	}
	// The interrupt flag is a steer-only signal: it is meaningless (and must
	// be rejected) on every other kind.
	if msg.Interrupt && msg.Kind != KindSteer {
		return fmt.Errorf("%w: interrupt flag requires kind steer", ErrInvalidMessage)
	}
	for i, ref := range msg.Refs {
		if err := validateRef(ref); err != nil {
			// %w, not %v: the inner error wraps sdkadapter's
			// ErrMalformedReference, and wrapping it through keeps
			// errors.Is(err, sdkadapter.ErrMalformedReference) true for
			// callers that branch on the failure mode rather than the
			// message text. Strictly additive: the chain previously
			// carried only ErrInvalidMessage.
			return fmt.Errorf("%w: refs[%d]: %w", ErrInvalidMessage, i, err)
		}
	}
	return nil
}

// ValidKind reports whether kind is in the fixed vocabulary.
func ValidKind(k Kind) bool {
	switch k {
	case KindFinding, KindQuestion, KindAnswer, KindSteer, KindAsk:
		return true
	default:
		return false
	}
}

// NewMessageID returns an unguessable durable message ID (msg-<base32>).
// Same crypto/rand convention as coordinator run IDs; not the process-local
// attempt counter.
func NewMessageID() string {
	var token [16]byte
	_, _ = rand.Read(token[:])
	return "msg-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(token[:])
}

// Synopsis returns a bounded, redacted-safe one-line preview of the body for
// lifecycle payloads (never the full body). Truncates on a UTF-8 boundary.
func Synopsis(body string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultSynopsisBytes
	}
	if len(body) <= maxBytes {
		return body
	}
	// Walk back to a valid UTF-8 boundary.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return body[:cut]
}

// ContentRef mints the content-addressed reference for a message body.
// Empty body yields "".
func ContentRef(body string) string {
	if body == "" {
		return ""
	}
	return sdkadapter.Mint(sdkadapter.KindMessage, []byte(body))
}

// LifecyclePayload is the bounded announcement shape for task_message events.
// Bodies never appear here - only ID + synopsis (and kind for routing).
type LifecyclePayload struct {
	MessageID string `json:"message_id"`
	Kind      Kind   `json:"kind"`
	Synopsis  string `json:"synopsis"`
	// ContentRef is the durable body reference when the body was stored.
	// Empty when body was empty.
	ContentRef string `json:"content_ref,omitempty"`
}

// NewLifecyclePayload builds the ID+synopsis announcement for a message.
func NewLifecyclePayload(msg Message) LifecyclePayload {
	return LifecyclePayload{
		MessageID:  msg.ID,
		Kind:       msg.Kind,
		Synopsis:   Synopsis(msg.Body, DefaultSynopsisBytes),
		ContentRef: ContentRef(msg.Body),
	}
}

func validateRef(ref string) error {
	if ref == "" {
		return errors.New("empty ref")
	}
	if _, _, err := sdkadapter.Parse(ref); err != nil {
		// The ref reaches this check verbatim from model-authored tool
		// arguments, and the bare Parse error ("sdkadapter: malformed
		// content reference") reads like an internal fault while teaching
		// nothing: live dispatches burned a second tool call re-guessing
		// after passing a package name. Name the offending value, the
		// expected shape, and both recoveries, so one round trip repairs
		// the call (DC-14: the caller is a model interface we do not own).
		return fmt.Errorf("%w: %q is not a content reference - pass a "+
			"ref:<kind>:<digest> handle verbatim from output_ref/error_ref "+
			"or run_messages content_ref, or omit refs",
			sdkadapter.ErrMalformedReference, ref)
	}
	return nil
}

func cloneRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, len(refs))
	copy(out, refs)
	return out
}

// RequireInReplyTo is a small helper for answer construction.
func RequireInReplyTo(inReplyTo string) error {
	if strings.TrimSpace(inReplyTo) == "" {
		return fmt.Errorf("%w: in_reply_to is required", ErrInvalidMessage)
	}
	return nil
}
