package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientMethods_RejectInvalidPathID pins that every client method
// taking a path-embedded id actually calls validatePathID and returns its
// error, rather than letting an invalid id reach the request path.
// validatePathID's own rules are pinned in TestValidatePathID; this proves
// each call site is wired to it.
func TestClientMethods_RejectInvalidPathID(t *testing.T) {
	client := newTestClient(t, ClientOptions{BaseURL: "http://unused.invalid"})
	ctx := context.Background()
	const bad = "../escape"

	if _, err := client.GetSession(ctx, bad); err == nil {
		t.Error("GetSession accepted an invalid session id")
	}
	if _, err := client.AppendEventsWithTrace(ctx, bad, nil, "", ""); err == nil {
		t.Error("AppendEventsWithTrace accepted an invalid session id")
	}
	if _, err := client.GetEvents(ctx, bad, 0, 10); err == nil {
		t.Error("GetEvents accepted an invalid session id")
	}
	if _, err := client.ConsumeInput(ctx, bad, "input-1"); err == nil {
		t.Error("ConsumeInput accepted an invalid session id")
	}
	if _, err := client.ConsumeInput(ctx, "sess-1", bad); err == nil {
		t.Error("ConsumeInput accepted an invalid input id")
	}
	if _, err := client.Heartbeat(ctx, bad, "running"); err == nil {
		t.Error("Heartbeat accepted an invalid session id")
	}
	if _, err := client.EndSession(ctx, bad); err == nil {
		t.Error("EndSession accepted an invalid session id")
	}
}

// TestClientEndSession_ServerErrorSurfaces pins EndSession's own
// doJSON-failure propagation, distinct from its validatePathID guard above:
// a well-formed id whose request the server refuses outright.
func TestClientEndSession_ServerErrorSurfaces(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	if _, err := client.EndSession(context.Background(), "sess-1"); err == nil {
		t.Fatal("EndSession accepted a server error")
	}
}

// TestClientBuildRequest_MarshalAndCreateRequestErrors pins buildRequest's
// two earliest guards directly: a request body encoding/json cannot marshal
// (a channel), and a method string http.NewRequestWithContext refuses (an
// embedded control character, which the net/http method validator rejects
// outright).
func TestClientBuildRequest_MarshalAndCreateRequestErrors(t *testing.T) {
	client := newTestClient(t, ClientOptions{BaseURL: "http://unused.invalid"})
	ctx := context.Background()

	if _, err := client.buildRequest(ctx, http.MethodPost, "/x", make(chan int), false, nil); err == nil {
		t.Fatal("buildRequest accepted a request body encoding/json cannot marshal")
	} else if !strings.Contains(err.Error(), "marshal request body") {
		t.Fatalf("err = %v, want the marshal wrap", err)
	}

	if _, err := client.buildRequest(ctx, "GET\x00", "/x", nil, false, nil); err == nil {
		t.Fatal("buildRequest accepted a method string with an embedded NUL")
	} else if !strings.Contains(err.Error(), "create request") {
		t.Fatalf("err = %v, want the create-request wrap", err)
	}
}

// TestClientDoJSON_DecodeErrorSurfaces pins doJSON's response-decode guard:
// a 2xx response whose body is not valid JSON for the target type must fail
// explicitly rather than leaving respBody's zero value silently in place.
func TestClientDoJSON_DecodeErrorSurfaces(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	if _, err := client.GetSession(context.Background(), "sess-1"); err == nil {
		t.Fatal("GetSession accepted an undecodable 2xx body")
	} else if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("err = %v, want the decode wrap", err)
	}
}

// TestParseErrorMessage covers all three shapes parseErrorMessage accepts:
// a plain JSON string, a JSON array joined with "; ", and raw bytes echoed
// back verbatim when neither parse succeeds.
func TestParseErrorMessage(t *testing.T) {
	if got := parseErrorMessage(nil); got != "" {
		t.Errorf("parseErrorMessage(nil) = %q, want empty", got)
	}
	if got := parseErrorMessage(json.RawMessage(`"a plain message"`)); got != "a plain message" {
		t.Errorf("parseErrorMessage(string) = %q", got)
	}
	if got := parseErrorMessage(json.RawMessage(`["first","second"]`)); got != "first; second" {
		t.Errorf("parseErrorMessage(array) = %q, want %q", got, "first; second")
	}
	if got := parseErrorMessage(json.RawMessage(`123`)); got != "123" {
		t.Errorf("parseErrorMessage(neither) = %q, want the raw bytes echoed back", got)
	}
}

// TestConflictError_MessageWithoutCode pins ConflictError.Error's no-code
// branch: the code suffix is omitted, not printed empty.
func TestConflictError_MessageWithoutCode(t *testing.T) {
	e := &ConflictError{StatusCode: 409, Message: "already exists"}
	got := e.Error()
	if strings.Contains(got, "code:") {
		t.Errorf("Error() = %q, must not mention a code when none was set", got)
	}
	if !strings.Contains(got, "already exists") {
		t.Errorf("Error() = %q, want the message included", got)
	}
}
