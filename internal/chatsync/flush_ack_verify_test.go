package chatsync

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestVerifyStoredMatchesSent_MissingTypeAndBodyMismatch drives
// verifyStoredMatchesSent's three failure branches directly: a sent seq
// absent from the readback, a stored type that differs from what was sent,
// and a stored body that decodes but does not match.
func TestVerifyStoredMatchesSent_MissingTypeAndBodyMismatch(t *testing.T) {
	sent := []StoredEvent{{Seq: 1, Type: "turn_start", Payload: json.RawMessage(`{}`)}}

	if err := verifyStoredMatchesSent(sent, nil); err == nil {
		t.Fatal("verifyStoredMatchesSent accepted a seq missing from the readback")
	} else if !strings.Contains(err.Error(), "neither inserted nor readable back") {
		t.Fatalf("err = %v", err)
	}

	wrongType := []StoredEvent{{Seq: 1, Type: "turn_end", Payload: json.RawMessage(`{}`)}}
	if err := verifyStoredMatchesSent(sent, wrongType); err == nil {
		t.Fatal("verifyStoredMatchesSent accepted a type the client never sent")
	} else if !strings.Contains(err.Error(), "this client sent") {
		t.Fatalf("err = %v", err)
	}

	badJSON := []StoredEvent{{Seq: 1, Type: "turn_start", Payload: json.RawMessage(`not json`)}}
	if err := verifyStoredMatchesSent(sent, badJSON); err == nil {
		t.Fatal("verifyStoredMatchesSent accepted an undecodable stored payload")
	}

	wrongBody := []StoredEvent{{Seq: 1, Type: "turn_start", Payload: json.RawMessage(`{"x":1}`)}}
	if err := verifyStoredMatchesSent(sent, wrongBody); err == nil {
		t.Fatal("verifyStoredMatchesSent accepted a body this client did not send")
	} else if !strings.Contains(err.Error(), "did not send") {
		t.Fatalf("err = %v", err)
	}
}

// TestDecodePair_ErrorsOnEitherSide pins both of decodePair's guards: the
// local (sent) side and the stored side are decoded and reported
// independently, so a caller can tell which body is actually malformed.
func TestDecodePair_ErrorsOnEitherSide(t *testing.T) {
	ok := json.RawMessage(`{}`)
	bad := json.RawMessage(`not json`)

	if _, _, err := decodePair(bad, ok); err == nil {
		t.Fatal("decodePair accepted an undecodable local payload")
	} else if !strings.Contains(err.Error(), "local payload") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := decodePair(ok, bad); err == nil {
		t.Fatal("decodePair accepted an undecodable stored payload")
	} else if !strings.Contains(err.Error(), "stored payload") {
		t.Fatalf("err = %v", err)
	}
}

// TestRepairedValueMatches_TypeMismatchesAndSlices covers the branches the
// existing repair tests do not: a stored value whose Go type differs from
// what was sent (string-vs-other, map-vs-other, slice-vs-other), a slice
// whose length or elements differ, and a map whose length differs.
func TestRepairedValueMatches_TypeMismatchesAndSlices(t *testing.T) {
	if repairedValueMatches("sent", 1.0) {
		t.Error("string sent must not match a non-string stored value")
	}
	if repairedValueMatches(map[string]any{"a": 1.0}, "not a map") {
		t.Error("map sent must not match a non-map stored value")
	}
	if repairedValueMatches(map[string]any{"a": 1.0, "b": 2.0}, map[string]any{"a": 1.0}) {
		t.Error("maps of different length must not match")
	}
	if repairedValueMatches(map[string]any{"a": 1.0}, map[string]any{"b": 1.0}) {
		t.Error("a map missing the sent key must not match")
	}
	if repairedValueMatches([]any{"a"}, "not a slice") {
		t.Error("slice sent must not match a non-slice stored value")
	}
	if repairedValueMatches([]any{"a", "b"}, []any{"a"}) {
		t.Error("slices of different length must not match")
	}
	if !repairedValueMatches([]any{"a\x00b"}, []any{"ab"}) {
		t.Error("a slice element with its NUL removed must still match")
	}
	if repairedValueMatches([]any{"a"}, []any{"b"}) {
		t.Error("a mismatched slice element must not match")
	}
	if !repairedValueMatches(3.0, 3.0) {
		t.Error("equal non-string, non-map, non-slice values must match via the default branch")
	}
}
