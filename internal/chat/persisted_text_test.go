package chat

import (
	"context"
	"io"
	"strings"
	"testing"
)

// TestSendUserWithEventAndPersistedTextCommitsTheShortFormOnTheDurablePath
// closes a real coverage gap an adversarial review surfaced after
// e58149f1 (skill-invocation history bloat fix): SendUserWithEventAndPersistedText
// had zero test coverage anywhere in this repo, including through the
// durable-context-manager path its production caller (uiadapter.Conversation.Send)
// actually exercises. This pins that persistedText - not the full sent
// text - is what lands in both the live session history and the durable
// checkpoint's committed Active context.
func TestSendUserWithEventAndPersistedTextCommitsTheShortFormOnTheDurablePath(t *testing.T) {
	sess, principal, _ := boundaryCompactionSession(t)
	fullText := "<skill-instructions name=\"bug-audit\">\nthousands of tokens of instructions\n</skill-instructions>"
	shortText := "/bug-audit"

	if _, err := sess.SendUserWithEventAndPersistedText(context.Background(), fullText, shortText, io.Discard, nil); err != nil {
		t.Fatal(err)
	}

	live := sess.MessagesCopy()
	var sawFull, sawShort bool
	for _, m := range live {
		if strings.Contains(m.Content, "thousands of tokens of instructions") {
			sawFull = true
		}
		if m.Content == shortText {
			sawShort = true
		}
	}
	if sawFull {
		t.Fatal("full text leaked into live session history")
	}
	if !sawShort {
		t.Fatalf("short persisted text missing from live session history: %+v", live)
	}

	snapshot, err := sess.ContextStore().Load(context.Background(), principal, sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	committed := decodeActiveContextMessages(t, snapshot.Active.ActiveContext)
	sawFull, sawShort = false, false
	for _, m := range committed {
		if strings.Contains(m.Content, "thousands of tokens of instructions") {
			sawFull = true
		}
		if m.Content == shortText {
			sawShort = true
		}
	}
	if sawFull {
		t.Fatal("full text leaked into the durable checkpoint's Active context")
	}
	if !sawShort {
		t.Fatalf("short persisted text missing from the durable checkpoint: %+v", committed)
	}
}
