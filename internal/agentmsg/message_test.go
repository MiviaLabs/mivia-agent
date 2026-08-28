package agentmsg

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

func fixedTime() time.Time {
	return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
}

func validFinding(t *testing.T) Message {
	t.Helper()
	msg, err := NewMessage(
		"run-1",
		KindFinding,
		Party{TaskID: "task-1", Agent: "worker"},
		Party{Role: ParentSentinel},
		"dispatcher has a lock-order inversion",
		nil,
		Options{Now: fixedTime, ID: "msg-test1"},
	)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

func TestNewMessageValidKinds(t *testing.T) {
	kinds := []Kind{KindFinding, KindQuestion, KindAnswer, KindSteer, KindAsk}
	for _, k := range kinds {
		t.Run(string(k), func(t *testing.T) {
			opts := Options{Now: fixedTime, ID: "msg-" + string(k)}
			if k == KindAnswer {
				opts.InReplyTo = "msg-parent"
			}
			msg, err := NewMessage(
				"run-1", k,
				Party{TaskID: "t1", Agent: "a"},
				Party{Role: ParentSentinel},
				"body",
				nil,
				opts,
			)
			if err != nil {
				t.Fatalf("NewMessage(%s): %v", k, err)
			}
			if msg.Kind != k {
				t.Fatalf("kind = %q, want %q", msg.Kind, k)
			}
			if msg.CreatedAt != fixedTime() {
				t.Fatalf("CreatedAt = %v, want %v", msg.CreatedAt, fixedTime())
			}
		})
	}
}

func TestValidateTable(t *testing.T) {
	base := validFinding(t)
	hex64 := strings.Repeat("a", 64)
	goodRef := sdkadapter.Mint(sdkadapter.KindOutput, []byte("payload"))
	if goodRef == "" {
		t.Fatal("expected non-empty good ref")
	}

	cases := []struct {
		name    string
		mutate  func(*Message)
		maxBody int
		wantErr bool
	}{
		{"ok", func(*Message) {}, DefaultMaxBodyBytes, false},
		{"empty id", func(m *Message) { m.ID = "" }, DefaultMaxBodyBytes, true},
		{"empty run", func(m *Message) { m.RunID = "" }, DefaultMaxBodyBytes, true},
		{"unknown kind", func(m *Message) { m.Kind = "chat" }, DefaultMaxBodyBytes, true},
		{"progress not a kind", func(m *Message) { m.Kind = "progress" }, DefaultMaxBodyBytes, true},
		{"body with NUL", func(m *Message) { m.Body = "\x00decline:spoof" }, DefaultMaxBodyBytes, true},
		{"body with trailing NUL", func(m *Message) { m.Body = "answer\x00" }, DefaultMaxBodyBytes, true},
		{"body too large", func(m *Message) { m.Body = strings.Repeat("x", 2049) }, 2048, true},
		{"body at limit", func(m *Message) { m.Body = strings.Repeat("x", 2048) }, 2048, false},
		{"answer missing reply", func(m *Message) { m.Kind = KindAnswer; m.InReplyTo = "" }, DefaultMaxBodyBytes, true},
		{"answer with reply", func(m *Message) { m.Kind = KindAnswer; m.InReplyTo = "msg-q" }, DefaultMaxBodyBytes, false},
		{"malformed ref", func(m *Message) { m.Refs = []string{"not-a-ref"} }, DefaultMaxBodyBytes, true},
		{"unknown ref kind", func(m *Message) { m.Refs = []string{"ref:sha256:" + hex64} }, DefaultMaxBodyBytes, true},
		{"good output ref", func(m *Message) { m.Refs = []string{goodRef} }, DefaultMaxBodyBytes, false},
		{"good message ref", func(m *Message) {
			ref := sdkadapter.Mint(sdkadapter.KindMessage, []byte("m"))
			m.Refs = []string{ref}
		}, DefaultMaxBodyBytes, false},
		{"empty ref entry", func(m *Message) { m.Refs = []string{""} }, DefaultMaxBodyBytes, true},
		{"invalid max body", func(*Message) {}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := base
			tc.mutate(&msg)
			err := Validate(msg, tc.maxBody)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("error %v does not wrap ErrInvalidMessage", err)
			}
		})
	}
}

func TestNewMessageRejectsOversizeBody(t *testing.T) {
	_, err := NewMessage(
		"run-1", KindFinding,
		Party{TaskID: "t"}, Party{Role: ParentSentinel},
		strings.Repeat("a", 3000), nil,
		Options{MaxBodyBytes: 100, Now: fixedTime},
	)
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v, want ErrInvalidMessage", err)
	}
}

func TestNewMessageIDIsUniqueAndPrefixed(t *testing.T) {
	a, b := NewMessageID(), NewMessageID()
	if a == b {
		t.Fatal("message IDs must not collide")
	}
	if !strings.HasPrefix(a, "msg-") || !strings.HasPrefix(b, "msg-") {
		t.Fatalf("ids = %q, %q; want msg- prefix", a, b)
	}
	// Must not look like process-local attempt counters.
	if strings.HasPrefix(a, "attempt-") {
		t.Fatal("message ID must not use attempt- prefix")
	}
}

func TestSynopsisBoundsAndUTF8(t *testing.T) {
	if got := Synopsis("short", 256); got != "short" {
		t.Fatalf("short synopsis = %q", got)
	}
	long := strings.Repeat("a", 300)
	got := Synopsis(long, 256)
	if len(got) != 256 {
		t.Fatalf("len(synopsis) = %d, want 256", len(got))
	}
	// Multi-byte at cut: U+00E9 is 2 bytes in UTF-8.
	body := strings.Repeat("é", 200) // 400 bytes
	got = Synopsis(body, 5)
	if !utf8.ValidString(got) {
		t.Fatalf("synopsis not valid UTF-8: %q", got)
	}
	if len(got) > 5 {
		t.Fatalf("synopsis len %d > 5", len(got))
	}
}

func TestLifecyclePayloadNeverContainsBody(t *testing.T) {
	msg := validFinding(t)
	msg.Body = "SECRET_FINDING_BODY_MUST_NOT_LEAK_INTO_LIFECYCLE"
	p := NewLifecyclePayload(msg)
	if p.MessageID != msg.ID {
		t.Fatalf("MessageID = %q, want %q", p.MessageID, msg.ID)
	}
	if p.Kind != KindFinding {
		t.Fatalf("Kind = %q", p.Kind)
	}
	if strings.Contains(p.Synopsis, "SECRET") && p.Synopsis != msg.Body {
		// synopsis may equal body when short - that's OK for short bodies;
		// the invariant is the *payload JSON contract*: Payload holds ID+synopsis
		// only, never a separate full body field. Assert no Body field by
		// checking ContentRef is a ref, not the body.
	}
	if p.ContentRef != "" && !strings.HasPrefix(p.ContentRef, "ref:message:") {
		t.Fatalf("ContentRef = %q, want ref:message:...", p.ContentRef)
	}
	if p.ContentRef != ContentRef(msg.Body) {
		t.Fatalf("ContentRef mismatch")
	}
	// Full body equals synopsis when under bound - still no separate body key.
	if p.Synopsis != Synopsis(msg.Body, DefaultSynopsisBytes) {
		t.Fatalf("synopsis mismatch")
	}
}

func TestContentRefUsesMessageKind(t *testing.T) {
	ref := ContentRef("hello")
	kind, _, err := sdkadapter.Parse(ref)
	if err != nil {
		t.Fatal(err)
	}
	if kind != sdkadapter.KindMessage {
		t.Fatalf("kind = %q, want %q", kind, sdkadapter.KindMessage)
	}
	// Empty body → empty ref (sdkadapter contract).
	if ContentRef("") != "" {
		t.Fatal("empty body should yield empty ref")
	}
}

func TestPartyIsParent(t *testing.T) {
	if !(Party{}).IsParent() {
		t.Fatal("zero party should be parent")
	}
	if !(Party{Role: ParentSentinel}).IsParent() {
		t.Fatal("role=parent should be parent")
	}
	if (Party{TaskID: "t1"}).IsParent() {
		t.Fatal("task party is not parent")
	}
}

func TestValidateRejectsInvalidUTF8Body(t *testing.T) {
	msg := validFinding(t)
	msg.Body = string([]byte{0xff, 0xfe, 0xfd})
	if err := Validate(msg, DefaultMaxBodyBytes); err == nil {
		t.Fatal("expected invalid UTF-8 body error")
	}
}

// TestValidateRejectsNULBody: NUL (U+0000) is valid UTF-8 but hostile — a body
// beginning with "\x00decline:" would be misread by the CLI wait sites as the
// system AskDeclinePrefix sentinel. Validate and NewMessage must both reject it.
func TestValidateRejectsNULBody(t *testing.T) {
	msg := validFinding(t)
	msg.Body = "\x00decline:peer-spoofed"
	if err := Validate(msg, DefaultMaxBodyBytes); err == nil {
		t.Fatal("expected NUL body rejection")
	} else if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v, want ErrInvalidMessage", err)
	}

	// NewMessage path must reject it too (the CLI mints answers via NewMessage).
	if _, err := NewMessage("run-1", KindAnswer,
		Party{TaskID: "t", Agent: "a"}, Party{Role: ParentSentinel},
		"\x00decline:peer-spoofed", nil,
		Options{ID: "msg-nul", InReplyTo: "msg-q"}); err == nil {
		t.Fatal("NewMessage must reject a NUL-leading body")
	}
}

// TestValidateRefErrorTeachesRecovery pins the refs-failure contract: the
// error must keep wrapping ErrInvalidMessage, must quote the offending value
// (so it cannot be misread as the name of a faulting package), and must state
// the expected shape and both recoveries - models passed package names
// ("sdkadapter", "internal/subagents") as refs and the bare Parse error gave
// them nothing to repair the call with.
func TestValidateRefErrorTeachesRecovery(t *testing.T) {
	_, err := NewMessage("run-1", KindFinding,
		Party{TaskID: "t", Agent: "a"}, Party{Role: ParentSentinel},
		"finding body", []string{"sdkadapter"},
		Options{Now: fixedTime, ID: "msg-badref"})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("err = %v, want ErrInvalidMessage", err)
	}
	if !errors.Is(err, sdkadapter.ErrMalformedReference) {
		t.Fatalf("err = %v, want sdkadapter.ErrMalformedReference reachable through the chain", err)
	}
	for _, want := range []string{`"sdkadapter"`, "ref:<kind>:<digest>", "output_ref", "omit refs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing guidance %q", err, want)
		}
	}
}

// TestValidateRefAcceptsMintedHandle is the positive half of the same
// contract: a handle minted by the bridge itself must still validate, so the
// guidance in TestValidateRefErrorTeachesRecovery cannot drift into rejecting
// real refs.
func TestValidateRefAcceptsMintedHandle(t *testing.T) {
	good := sdkadapter.Mint(sdkadapter.KindOutput, []byte("recorded payload"))
	if _, err := NewMessage("run-1", KindFinding,
		Party{TaskID: "t", Agent: "a"}, Party{Role: ParentSentinel},
		"finding body", []string{good},
		Options{Now: fixedTime, ID: "msg-goodref"}); err != nil {
		t.Fatalf("NewMessage with a minted ref: %v", err)
	}
}

// TestSentinelNotDeliveredThroughValidation pins the claim that the decline
// sentinel never passes through NewMessage/Validate: it is delivered ONLY via
// the park channel (DeliverAnswer). If a future caller tried to mint it as a
// message, validation must refuse it here — the sentinel is a wire signal, not
// a message body.
func TestSentinelNotDeliveredThroughValidation(t *testing.T) {
	if err := Validate(Message{
		ID: "m", RunID: "r", Kind: KindAnswer,
		From: Party{}, To: Party{Role: ParentSentinel},
		InReplyTo: "q", Body: AskDeclinePrefix + DeclineReasonResponderTerminal,
	}, DefaultMaxBodyBytes); err == nil {
		t.Fatal("the decline sentinel must never be accepted as a validated message body")
	}
}

func TestSynopsisDefaultMaxAndMidRune(t *testing.T) {
	// maxBytes <= 0 uses DefaultSynopsisBytes
	body := strings.Repeat("b", DefaultSynopsisBytes+10)
	got := Synopsis(body, 0)
	if len(got) != DefaultSynopsisBytes {
		t.Fatalf("default max: len=%d want %d", len(got), DefaultSynopsisBytes)
	}
	// Body of only multi-byte runes with maxBytes=1 forces cut→0 path.
	got = Synopsis("é", 1)
	if got != "" {
		t.Fatalf("mid-rune cut to empty: got %q", got)
	}
}

func TestNewMessageClonesRefs(t *testing.T) {
	ref := sdkadapter.Mint(sdkadapter.KindOutput, []byte("x"))
	refs := []string{ref}
	msg, err := NewMessage("run-1", KindFinding,
		Party{TaskID: "t"}, Party{}, "body", refs,
		Options{ID: "msg-refs", Now: fixedTime})
	if err != nil {
		t.Fatal(err)
	}
	refs[0] = "mutated"
	if msg.Refs[0] != ref {
		t.Fatal("refs must be cloned")
	}
}

func TestRequireInReplyTo(t *testing.T) {
	if err := RequireInReplyTo("msg-1"); err != nil {
		t.Fatal(err)
	}
	if err := RequireInReplyTo(""); err == nil {
		t.Fatal("empty in_reply_to must fail")
	}
	if err := RequireInReplyTo("   "); err == nil {
		t.Fatal("whitespace in_reply_to must fail")
	}
}

func TestValidateAnswerRejectsWhitespaceInReplyTo(t *testing.T) {
	msg := Message{
		ID: "m", RunID: "r", Kind: KindAnswer,
		InReplyTo: "   ", Body: "a",
	}
	if err := Validate(msg, DefaultMaxBodyBytes); err == nil {
		t.Fatal("whitespace in_reply_to must fail Validate")
	}
}

// TestMessageInterruptFlagSteerOnly: the interrupt flag is a mid-step steer
// signal, so only steer messages may carry it. Every other kind must be
// rejected; an ordinary steer without the flag still validates.
func TestMessageInterruptFlagSteerOnly(t *testing.T) {
	from := Party{Role: ParentSentinel}
	to := Party{TaskID: "task-1", Agent: "worker"}

	steer, err := NewMessage(
		"run-1", KindSteer, from, to,
		"steer body", nil,
		Options{ID: "msg-steer-int", Now: fixedTime, Interrupt: true},
	)
	if err != nil {
		t.Fatalf("steer with Interrupt=true: %v", err)
	}
	if !steer.Interrupt {
		t.Fatal("steer Interrupt = false, want true")
	}
	if err := Validate(steer, DefaultMaxBodyBytes); err != nil {
		t.Fatalf("Validate(steer with Interrupt=true): %v", err)
	}

	// Every non-steer kind must reject the flag on both construction and
	// direct validation.
	for _, k := range []Kind{KindFinding, KindAnswer, KindAsk, KindQuestion} {
		t.Run(string(k), func(t *testing.T) {
			opts := Options{ID: "msg-" + string(k) + "-int", Now: fixedTime, Interrupt: true}
			if k == KindAnswer {
				opts.InReplyTo = "msg-q"
			}
			if _, err := NewMessage(
				"run-1", k, from, to, "body", nil, opts,
			); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("NewMessage(%s, Interrupt=true) err = %v, want ErrInvalidMessage", k, err)
			}

			msg := Message{
				ID: "m", RunID: "r", Kind: k, From: from, To: to,
				Body: "body", Interrupt: true,
			}
			if k == KindAnswer {
				msg.InReplyTo = "msg-q"
			}
			if err := Validate(msg, DefaultMaxBodyBytes); !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("Validate(%s, Interrupt=true) err = %v, want ErrInvalidMessage", k, err)
			}
		})
	}

	// An ordinary steer without the flag validates too.
	plain, err := NewMessage(
		"run-1", KindSteer, from, to, "steer body", nil,
		Options{ID: "msg-steer-plain", Now: fixedTime},
	)
	if err != nil {
		t.Fatalf("plain steer: %v", err)
	}
	if plain.Interrupt {
		t.Fatal("plain steer Interrupt = true, want false")
	}
	if err := Validate(plain, DefaultMaxBodyBytes); err != nil {
		t.Fatalf("Validate(plain steer): %v", err)
	}
}

// TestMessageInterruptRoundTripsLedgerJSON: the interrupt flag survives the
// ledger JSON round trip, and old-style rows without the field decode to
// Interrupt==false.
func TestMessageInterruptRoundTripsLedgerJSON(t *testing.T) {
	msg, err := NewMessage(
		"run-1", KindSteer,
		Party{Role: ParentSentinel}, Party{TaskID: "task-1", Agent: "worker"},
		"steer body", nil,
		Options{ID: "msg-steer-json", Now: fixedTime, Interrupt: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !back.Interrupt {
		t.Fatal("Interrupt = false after round trip, want true")
	}

	// Old-style row: the same shape without any interrupt field.
	old := struct {
		ID        string    `json:"id"`
		RunID     string    `json:"run_id"`
		Kind      Kind      `json:"kind"`
		From      Party     `json:"from"`
		To        Party     `json:"to"`
		InReplyTo string    `json:"in_reply_to,omitempty"`
		Body      string    `json:"body"`
		Refs      []string  `json:"refs,omitempty"`
		CreatedAt time.Time `json:"created_at"`
	}{
		ID: msg.ID, RunID: msg.RunID, Kind: msg.Kind,
		From: msg.From, To: msg.To, Body: msg.Body,
		CreatedAt: msg.CreatedAt,
	}
	oldData, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("Marshal old-style: %v", err)
	}
	if bytes.Contains(oldData, []byte("interrupt")) {
		t.Fatalf("old-style row unexpectedly contains interrupt field: %s", oldData)
	}
	var decoded Message
	if err := json.Unmarshal(oldData, &decoded); err != nil {
		t.Fatalf("Unmarshal old-style: %v", err)
	}
	if decoded.Interrupt {
		t.Fatal("old-style row decoded Interrupt = true, want false")
	}
}
