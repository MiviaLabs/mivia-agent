package provider

// A response-header bound is a stall detector, and it is only a stall detector
// for a request that answers immediately. A streaming request does: headers
// come back before the model has written a word. A non-stream completion does
// not - it sends no byte until the generation is finished, so its wait for
// headers IS the model's thinking time, and a header bound placed on it is a
// generation ceiling wearing a stall detector's clothes.
//
// One 120-second bound was applied to every request of both kinds, so any
// non-stream turn that thought for longer than two minutes was killed
// regardless of the request budget the operator configured.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The seam must produce two transports: one that bounds the header wait for
// requests whose headers are immediate, and one with no such bound for
// requests whose header wait is the generation.
func TestCompatBaseRoundTripperSplitsHeaderBoundByPhase(t *testing.T) {
	modal, ok := compatBaseRoundTripper(nil).(*modalHeaderTransport)
	if !ok {
		t.Fatalf("compatBaseRoundTripper(nil) = %T, want *modalHeaderTransport", compatBaseRoundTripper(nil))
	}
	if modal.streamed.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Errorf("streamed ResponseHeaderTimeout = %v, want %v",
			modal.streamed.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
	if modal.generation.ResponseHeaderTimeout != 0 {
		t.Errorf("generation ResponseHeaderTimeout = %v, want none: the header wait is the model's work",
			modal.generation.ResponseHeaderTimeout)
	}
}

// The loopback pin is the security gate, and it must reach BOTH transports.
// A pin applied to only one of them would let a non-stream request dial an
// address the resolver chose.
func TestCompatBaseRoundTripperPinsBothTransports(t *testing.T) {
	pin := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil }
	modal, ok := compatBaseRoundTripper(pin).(*modalHeaderTransport)
	if !ok {
		t.Fatalf("compatBaseRoundTripper(pin) = %T, want *modalHeaderTransport", compatBaseRoundTripper(pin))
	}
	for name, tr := range map[string]*http.Transport{"streamed": modal.streamed, "generation": modal.generation} {
		if tr.DialContext == nil {
			t.Fatalf("%s transport lost the pinned dial", name)
		}
		if _, err := tr.DialContext(context.Background(), "tcp", "example.invalid:443"); err != nil {
			t.Fatalf("%s transport is not using the pinned dial: %v", name, err)
		}
		if tr.DialTLSContext != nil {
			t.Errorf("%s transport must not set DialTLSContext", name)
		}
	}
	// Fresh per call, never shared, never the global.
	other, _ := compatBaseRoundTripper(pin).(*modalHeaderTransport)
	if other.streamed == modal.streamed || other.generation == modal.generation {
		t.Fatal("compatBaseRoundTripper must return fresh transports per call")
	}
	def := http.DefaultTransport.(*http.Transport)
	if modal.streamed == def || modal.generation == def {
		t.Fatal("a client must never own http.DefaultTransport")
	}
}

// Behavioural: the same slow-to-answer server kills an unmarked request on the
// header bound and serves a marked one to completion.
func TestModalHeaderTransportSelectsByContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond) // "generating"
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	bounded := http.DefaultTransport.(*http.Transport).Clone()
	bounded.ResponseHeaderTimeout = 50 * time.Millisecond
	unbounded := http.DefaultTransport.(*http.Transport).Clone()
	unbounded.ResponseHeaderTimeout = 0
	modal := &modalHeaderTransport{streamed: bounded, generation: unbounded}
	client := &http.Client{Transport: modal}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := client.Do(req); err == nil {
		t.Error("an unmarked (streaming-shaped) request must still be bounded")
	}

	marked, _ := http.NewRequestWithContext(withGenerationHeaderPhase(context.Background()), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(marked)
	if err != nil {
		t.Fatalf("a generation-phase request must not be cut by the header bound: %v", err)
	}
	defer resp.Body.Close()
}

// The marker has to be applied where the request is built, or the transport
// cannot tell the two phases apart. Both clients build requests in exactly one
// place each.
func TestOpenAICompatMarksOnlyNonStreamRequests(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "probe", BaseURL: "https://example.invalid", APIKey: "k"})
	base := Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}

	nonStream, err := c.newRequest(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !generationHeaderPhase(nonStream.Context()) {
		t.Error("a non-stream completion's header wait is the generation; it must be marked")
	}

	streaming := base
	streaming.Stream = true
	streamReq, err := c.newRequest(context.Background(), streaming)
	if err != nil {
		t.Fatal(err)
	}
	if generationHeaderPhase(streamReq.Context()) {
		t.Error("a streaming request answers immediately and must keep the header bound")
	}
}

func TestAnthropicMarksOnlyNonStreamRequests(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	req := anthropicTestRequest([]Message{{Role: RoleUser, Content: "hi"}})
	body, err := c.buildRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}

	nonStream, cancel, err := c.newHTTPRequest(context.Background(), req, body)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if !generationHeaderPhase(nonStream.Context()) {
		t.Error("a non-stream Messages request must be marked as generation phase")
	}

	streamBody := map[string]any{"stream": true}
	for k, v := range body {
		streamBody[k] = v
	}
	streamReq, cancelStream, err := c.newHTTPRequest(context.Background(), req, streamBody)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelStream()
	if generationHeaderPhase(streamReq.Context()) {
		t.Error("a streamed Messages request must keep the header bound")
	}
}
