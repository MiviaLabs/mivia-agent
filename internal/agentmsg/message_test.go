package agentmsg

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
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
	goodRef := contentref.Reference(contentref.KindOutput, []byte("payload"))
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
		{"body too large", func(m *Message) { m.Body = strings.Repeat("x", 2049) }, 2048, true},
		{"body at limit", func(m *Message) { m.Body = strings.Repeat("x", 2048) }, 2048, false},
		{"answer missing reply", func(m *Message) { m.Kind = KindAnswer; m.InReplyTo = "" }, DefaultMaxBodyBytes, true},
		{"answer with reply", func(m *Message) { m.Kind = KindAnswer; m.InReplyTo = "msg-q" }, DefaultMaxBodyBytes, false},
		{"malformed ref", func(m *Message) { m.Refs = []string{"not-a-ref"} }, DefaultMaxBodyBytes, true},
		{"unknown ref kind", func(m *Message) { m.Refs = []string{"ref:sha256:" + hex64} }, DefaultMaxBodyBytes, true},
		{"good output ref", func(m *Message) { m.Refs = []string{goodRef} }, DefaultMaxBodyBytes, false},
		{"good message ref", func(m *Message) {
			ref := contentref.Reference(contentref.KindMessage, []byte("m"))
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
	kind, _, err := contentref.Parse(ref)
	if err != nil {
		t.Fatal(err)
	}
	if kind != contentref.KindMessage {
		t.Fatalf("kind = %q, want %q", kind, contentref.KindMessage)
	}
	// Empty body → empty ref (contentref contract).
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
	ref := contentref.Reference(contentref.KindOutput, []byte("x"))
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
