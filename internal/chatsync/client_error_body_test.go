package chatsync

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// badRequestServer returns a client whose append endpoint always answers the
// given status with the given raw body, the shape TestClient409ConflictTypedError
// uses. The body is sent verbatim, so a test controls exact bytes.
func badRequestServer(t *testing.T, body string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newTestClient(t, ClientOptions{BaseURL: srv.URL})
}

// TestParseErrorResponse_EmptyBodyNamesTheGap covers the 400 whose body is
// empty or unreadable. A bare "chatsync bad request (400): " with nothing
// after it tells an operator nothing about what the server said, so the error
// must say explicitly that the body carried no readable text.
func TestParseErrorResponse_EmptyBodyNamesTheGap(t *testing.T) {
	client := badRequestServer(t, "")
	_, err := client.AppendEvents(t.Context(), "sess-1", []EventItem{{Seq: 1, Type: TypeTurnStarted}})
	if err == nil {
		t.Fatal("expected a bad request error, got nil")
	}
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Fatalf("errors.As(err, &badRequestError) = false, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "empty or unreadable") {
		t.Errorf("Error() = %q, want it to say the body was empty or unreadable", err.Error())
	}
}

// TestParseErrorResponse_RawBodyCappedAndSanitized covers the 400 whose body
// is not an error envelope: HTML, a proxy banner, a binary blob. The raw text
// must reach the message only as a bounded snippet - capped at 512 bytes, cut
// on a rune boundary, control characters removed - so a huge or binary body
// cannot flood a log or carry an invalid rune into the store.
func TestParseErrorResponse_RawBodyCappedAndSanitized(t *testing.T) {
	// 510 plain bytes, then a two-byte rune that straddles the 512-byte cut,
	// then control characters a binary body would carry.
	body := strings.Repeat("a", 510) + "é" + strings.Repeat("b", 40) + "\x00\x01\x07" + strings.Repeat("c", 40)
	client := badRequestServer(t, body)

	_, err := client.AppendEvents(t.Context(), "sess-1", []EventItem{{Seq: 1, Type: TypeTurnStarted}})
	if err == nil {
		t.Fatal("expected a bad request error, got nil")
	}
	prefix := "chatsync bad request (400): "
	msg := strings.TrimPrefix(err.Error(), prefix)
	if got := len(msg); got > 512 {
		t.Errorf("snippet is %d bytes, want at most 512", got)
	}
	if !utf8.ValidString(err.Error()) {
		t.Errorf("Error() holds an invalid rune sequence: %q", err.Error())
	}
	if strings.ContainsAny(err.Error(), "\x00\x01\x07") {
		t.Errorf("Error() kept control characters: %q", err.Error())
	}
	if !strings.Contains(err.Error(), strings.Repeat("a", 32)) {
		t.Errorf("Error() = %q, want the body's leading bytes in the snippet", err.Error())
	}
}

// TestParseErrorResponse_EnvelopeMessageAndCodePinned covers the healthy path
// a regression could break while adding the fallbacks: a valid envelope's
// message passes through unchanged and the envelope's error code still lands
// in the typed error's Code field.
func TestParseErrorResponse_EnvelopeMessageAndCodePinned(t *testing.T) {
	client := badRequestServer(t, func() string {
		out, err := json.Marshal(ErrorEnvelope{StatusCode: 400, Error: "ValidationError", Message: json.RawMessage(`"payload for seq 1 is over the ceiling"`)})
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		return string(out)
	}())

	_, err := client.AppendEvents(t.Context(), "sess-1", []EventItem{{Seq: 1, Type: TypeTurnStarted}})
	if err == nil {
		t.Fatal("expected a bad request error, got nil")
	}
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Fatalf("errors.As(err, &badRequestError) = false, got %T: %v", err, err)
	}
	if bad.Message != "payload for seq 1 is over the ceiling" {
		t.Errorf("Message = %q, want the envelope's message unchanged", bad.Message)
	}
	if bad.Code != "ValidationError" {
		t.Errorf("Code = %q, want the envelope's error code", bad.Code)
	}
	if bad.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", bad.StatusCode)
	}
}

// TestParseErrorResponse_EnvelopeMessageSanitized pins that envelope-delivered
// text meets the same hygiene as the raw-body snippet: control characters a
// proxy or misbehaving CDN error page can carry - NUL, newline, escape - do
// not reach the operator-facing message or the code field.
func TestParseErrorResponse_EnvelopeMessageSanitized(t *testing.T) {
	client := badRequestServer(t, func() string {
		out, err := json.Marshal(ErrorEnvelope{StatusCode: 400, Error: "Bad\x07Request", Message: json.RawMessage(`"ab\u0000c\nd"`)})
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		return string(out)
	}())

	_, err := client.AppendEvents(t.Context(), "sess-1", []EventItem{{Seq: 1, Type: TypeTurnStarted}})
	if err == nil {
		t.Fatal("expected a bad request error, got nil")
	}
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Fatalf("errors.As(err, &badRequestError) = false, got %T: %v", err, err)
	}
	for _, r := range bad.Message {
		if isControlRune(r) {
			t.Errorf("Message %q still carries control character %#x", bad.Message, r)
		}
	}
	for _, r := range bad.Code {
		if isControlRune(r) {
			t.Errorf("Code %q still carries control character %#x", bad.Code, r)
		}
	}
}
