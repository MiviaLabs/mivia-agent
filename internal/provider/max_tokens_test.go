package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// maxTokensIncidentEnvelope is the rejection a tighter upstream route returns
// when the wire max_tokens exceeds its real output cap.
const maxTokensIncidentEnvelope = `{"error":{"message":"max_tokens exceeds the allowed limit","type":"invalid_request_error"}}`

// maxTokensUnrelatedEnvelope is a plain invalid_request_error that must NOT
// trigger the max-tokens clamp.
const maxTokensUnrelatedEnvelope = `{"error":{"message":"unknown parameter","type":"invalid_request_error"}}`

// maxTokensWire is the subset of the request body the clamp tests inspect.
type maxTokensWire struct {
	MaxTokens *int `json:"max_tokens"`
}

// maxTokensProbe records what the max-tokens clamp tests observe: how many
// requests reached the httptest server and the max_tokens each carried.
// httptest handlers run in a goroutine, so all access is mutex-guarded.
type maxTokensProbe struct {
	mu        sync.Mutex
	count     int
	maxTokens []int
}

// record appends one observed request. An absent max_tokens is recorded as 0
// so the probe always keeps one entry per request.
func (p *maxTokensProbe) record(maxTokens *int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	if maxTokens == nil {
		p.maxTokens = append(p.maxTokens, 0)
		return
	}
	p.maxTokens = append(p.maxTokens, *maxTokens)
}

// snapshot returns a consistent (count, max_tokens) view of the probe.
func (p *maxTokensProbe) snapshot() (int, []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count, append([]int(nil), p.maxTokens...)
}

// decodeMaxTokensBody extracts the wire max_tokens from one request body.
func decodeMaxTokensBody(r *http.Request) *int {
	var body maxTokensWire
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.MaxTokens
}

// newMaxTokensTestClient builds the standard OpenAI-compatible client the
// clamp tests share.
func newMaxTokensTestClient(t *testing.T, srv *httptest.Server) *OpenAICompat {
	t.Helper()
	return NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
}

func TestIsMaxTokensCapMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{"incident message", "max_tokens exceeds the allowed limit", true},
		{"explicit cap in message", "Invalid value for 'max_tokens': 32768. This model supports at most 32000 completion tokens.", true},
		{"larger than maximum", "max_tokens is larger than the maximum allowed value", true},
		{"no underscore variant", "max tokens exceeds the allowed limit", true},
		{"unknown parameter", "unknown parameter", false},
		{"required", "max_tokens is required", false},
		{"context length", "This model's maximum context length is 16384 tokens. However, you requested 32768 tokens", false},
		{"temperature", "invalid value for temperature", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMaxTokensCapMessage(tt.msg); got != tt.want {
				t.Fatalf("isMaxTokensCapMessage(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestOpenAIErrorParserWrapsMaxTokensCapRejection(t *testing.T) {
	incident := []byte(maxTokensIncidentEnvelope)
	// A plain HTTP 400 rejection must carry the sentinel.
	if err := openaiErrorParser(http.StatusBadRequest, incident); !errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("400 rejection err=%v, want errors.Is(err, ErrMaxTokensExceeded)", err)
	}
	// The same envelope delivered in-band on HTTP 200 must carry it too.
	if err := openaiErrorParser(http.StatusOK, incident); !errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("200 in-band err=%v, want errors.Is(err, ErrMaxTokensExceeded)", err)
	}
	// An unrelated invalid_request_error stays an error but must NOT wrap the
	// sentinel.
	unrelated := []byte(maxTokensUnrelatedEnvelope)
	err := openaiErrorParser(http.StatusBadRequest, unrelated)
	if err == nil {
		t.Fatal("unrelated 400 must be an error")
	}
	if errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("unrelated 400 err=%v must NOT wrap ErrMaxTokensExceeded", err)
	}
}

func TestChatTurnClampsMaxTokensOnCapRejection(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt := decodeMaxTokensBody(r)
		probe.record(mt)
		if mt != nil && *mt > 20000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: &mt,
	})
	if err != nil {
		t.Fatalf("ChatTurn err=%v, want nil after clamp", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp=%+v, want Content %q", resp, "ok")
	}
	count, captured := probe.snapshot()
	if count != 2 {
		t.Fatalf("requestCount=%d, want 2", count)
	}
	if len(captured) != 2 || captured[0] != 32768 || captured[1] != 16384 {
		t.Fatalf("capturedMaxTokens=%v, want [32768 16384]", captured)
	}
}

func TestChatTurnClampRetriesBounded(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.record(decodeMaxTokensBody(r))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	_, err := c.ChatTurn(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: &mt,
	})
	if err == nil {
		t.Fatal("ChatTurn err=nil, want an error after the clamp budget is spent")
	}
	if !errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("err=%v, want errors.Is(err, ErrMaxTokensExceeded)", err)
	}
	count, _ := probe.snapshot()
	if want := 1 + maxMaxTokensClampRetries; count != want {
		t.Fatalf("requestCount=%d, want %d", count, want)
	}
}

func TestChatTurnClampRespectsDisableProviderReplay(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.record(decodeMaxTokensBody(r))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	_, err := c.ChatTurn(context.Background(), Request{
		Model:                 "m",
		Messages:              []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens:             &mt,
		DisableProviderReplay: true,
	})
	if err == nil {
		t.Fatal("ChatTurn err=nil, want an error")
	}
	if !errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("err=%v, want errors.Is(err, ErrMaxTokensExceeded)", err)
	}
	count, _ := probe.snapshot()
	if count != 1 {
		t.Fatalf("requestCount=%d, want 1 (no replay)", count)
	}
}

func TestChatTurnNoClampOnUnrelatedBadRequest(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.record(decodeMaxTokensBody(r))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(maxTokensUnrelatedEnvelope))
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	_, err := c.ChatTurn(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: &mt,
	})
	if err == nil {
		t.Fatal("ChatTurn err=nil, want an error")
	}
	if errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("err=%v must NOT wrap ErrMaxTokensExceeded", err)
	}
	count, _ := probe.snapshot()
	if count != 1 {
		t.Fatalf("requestCount=%d, want 1", count)
	}
}

func TestChatStreamClampsMaxTokensOnCapRejection(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt := decodeMaxTokensBody(r)
		probe.record(mt)
		if mt != nil && *mt > 20000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	out, err := c.ChatStream(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: &mt,
	}, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream err=%v, want nil after clamp", err)
	}
	if out != "hi" {
		t.Fatalf("out=%q, want %q", out, "hi")
	}
	count, captured := probe.snapshot()
	if count != 2 {
		t.Fatalf("requestCount=%d, want 2", count)
	}
	if len(captured) != 2 || captured[1] != 16384 {
		t.Fatalf("capturedMaxTokens=%v, want second request at 16384", captured)
	}
}

func TestChatTurnWithToolsClampsMaxTokensOnCapRejection(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt := decodeMaxTokensBody(r)
		probe.record(mt)
		if mt != nil && *mt > 20000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: &mt,
		Stream:    true,
		Tools:     []ToolSpec{{"type": "function", "function": map[string]any{"name": "read_file"}}},
	})
	if err != nil {
		t.Fatalf("ChatTurn err=%v, want nil after clamp", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp=%+v, want Content %q", resp, "ok")
	}
	count, captured := probe.snapshot()
	if count != 2 {
		t.Fatalf("requestCount=%d, want 2", count)
	}
	if len(captured) != 2 || captured[1] != 16384 {
		t.Fatalf("capturedMaxTokens=%v, want second request at 16384", captured)
	}
}

// TestOpenAIErrorParserKeepsTransientClassForInBandCapWording pins the
// conservative ordering in the HTTP-200 in-band branch: an envelope that
// combines max-tokens-cap wording with a transient fault type keeps its
// pre-existing transient classification (the step-retry layer retries it),
// and only a NON-transient in-band cap rejection carries the clamp sentinel.
func TestOpenAIErrorParserKeepsTransientClassForInBandCapWording(t *testing.T) {
	transient := []byte(`{"error":{"message":"max_tokens exceeds the allowed limit","type":"server_error"}}`)
	err := openaiErrorParser(http.StatusOK, transient)
	if !IsTransient(err) {
		t.Fatalf("transient-typed in-band err=%v, want IsTransient", err)
	}
	if errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("transient-typed in-band err=%v must NOT wrap ErrMaxTokensExceeded", err)
	}
	permanent := []byte(maxTokensIncidentEnvelope)
	err = openaiErrorParser(http.StatusOK, permanent)
	if !errors.Is(err, ErrMaxTokensExceeded) {
		t.Fatalf("permanent in-band err=%v, want errors.Is(err, ErrMaxTokensExceeded)", err)
	}
}

// TestChatTurnClampHalvingEdges pins the halving floor and the no-progress
// guard: a 2 or 3 cap halves to 1 exactly once, and the next clamp is refused
// because a 1-cap re-issue could not shrink further.
func TestChatTurnClampHalvingEdges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cap    int
		second int
	}{
		{"cap 2", 2, 1},
		{"cap 3", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &maxTokensProbe{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probe.record(decodeMaxTokensBody(r))
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			}))
			defer srv.Close()

			c := newMaxTokensTestClient(t, srv)
			mt := tc.cap
			_, err := c.ChatTurn(context.Background(), Request{
				Model:     "m",
				Messages:  []Message{{Role: RoleUser, Content: "hi"}},
				MaxTokens: &mt,
			})
			if err == nil {
				t.Fatal("ChatTurn err=nil, want an error")
			}
			count, captured := probe.snapshot()
			if count != 2 {
				t.Fatalf("requestCount=%d, want 2", count)
			}
			if len(captured) != 2 || captured[1] != tc.second {
				t.Fatalf("capturedMaxTokens=%v, want second request at %d", captured, tc.second)
			}
		})
	}
}

// TestChatTurnClampMultiRetryThenSuccess exercises both clamp retries: the
// declared cap is rejected twice (32768 then 16384) and the halved 8192 is
// accepted, so all three wire requests must be observed and the turn succeeds.
func TestChatTurnClampMultiRetryThenSuccess(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt := decodeMaxTokensBody(r)
		probe.record(mt)
		if mt != nil && *mt > 10000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := newMaxTokensTestClient(t, srv)
	mt := 32768
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:     "m",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: &mt,
	})
	if err != nil {
		t.Fatalf("ChatTurn err=%v, want nil after two clamps", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp=%+v, want Content %q", resp, "ok")
	}
	count, captured := probe.snapshot()
	if count != 3 {
		t.Fatalf("requestCount=%d, want 3", count)
	}
	want := []int{32768, 16384, 8192}
	if len(captured) != len(want) {
		t.Fatalf("capturedMaxTokens=%v, want %v", captured, want)
	}
	for i := range want {
		if captured[i] != want[i] {
			t.Fatalf("capturedMaxTokens=%v, want %v", captured, want)
		}
	}
}

// TestChatTurnClampsFloorMaxTokensOnCapRejection pins the fix for a
// regression the max_tokens-floor change (effectiveMaxTokens) introduced:
// clampMaxTokensForRetry gates recovery on Request.MaxTokens, which
// effectiveMaxTokens computed for the WIRE body but never wrote back onto
// req before doChatRequest's clamp loop ran. A request that never set
// MaxTokens explicitly (the exact shape the floor exists for) would hit a
// cap rejection on the guessed floor and get zero retries, because
// clampMaxTokensForRetry saw req.MaxTokens == nil and bailed immediately.
// doChatRequest now materializes req.MaxTokens = c.effectiveMaxTokens(req)
// before that loop, so this must clamp and recover exactly like the
// explicit-MaxTokens case above.
func TestChatTurnClampsFloorMaxTokensOnCapRejection(t *testing.T) {
	probe := &maxTokensProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt := decodeMaxTokensBody(r)
		probe.record(mt)
		if mt != nil && *mt > 20000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(maxTokensIncidentEnvelope))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "zai", BaseURL: srv.URL, APIKey: "k"})
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:          "glm-5.3-flash",
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ReasoningLevel: reasoning.Max,
	})
	if err != nil {
		t.Fatalf("ChatTurn err=%v, want nil after clamp recovers the floor-guessed max_tokens", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("resp=%+v, want Content %q", resp, "ok")
	}
	count, captured := probe.snapshot()
	if count != 3 {
		t.Fatalf("requestCount=%d, want 3 (initial floor guess, then two clamps: 65536 -> 32768 -> 16384)", count)
	}
	want := []int{65536, 32768, 16384}
	if len(captured) != len(want) {
		t.Fatalf("capturedMaxTokens=%v, want %v", captured, want)
	}
	for i := range want {
		if captured[i] != want[i] {
			t.Fatalf("capturedMaxTokens=%v, want %v", captured, want)
		}
	}
}

// TestUnsetMaxTokensGetsReasoningFloorNotOmitted pins the fix for the
// always-thinking-model empty-response failure: an OpenAI-compatible request
// with an active, wire-carried reasoning level but no explicit MaxTokens must
// carry a max_tokens scaled to that level, never an omitted field. Omitting
// it does NOT mean "use the model's declared max_output_tokens" - it means
// "use whatever small default this route happens to apply", which an
// always-thinking model (z.ai's GLM-5.3 family, dialect thinking_preserved)
// burns entirely on reasoning before producing any answer text.
func TestUnsetMaxTokensGetsReasoningFloorNotOmitted(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "zai", BaseURL: "https://example.test", APIKey: "k"})
	req := Request{
		Model:          "glm-5.3-flash",
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ReasoningLevel: reasoning.Max,
	}
	raw, err := c.marshalBody(req)
	if err != nil {
		t.Fatalf("marshalBody: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding marshaled body: %v", err)
	}
	got, ok := body["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens missing from body (relying on the route's own default): %s", raw)
	}
	if int(got) != 65536 {
		t.Fatalf("max_tokens=%v, want reasoningMaxTokensFloor(Max)=65536", got)
	}
}

// TestUnsetMaxTokensWithNoWireLevelKeepsPlainFloor proves the floor only
// scales up when the level actually reaches the wire: a level requested for
// a provider with no vetted dialect (so reasoningBodyFields sends nothing)
// must not silently inflate max_tokens either - it falls back to the same
// floor an unset level gets.
func TestUnsetMaxTokensWithNoWireLevelKeepsPlainFloor(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "kimi", BaseURL: "https://example.test", APIKey: "k"})
	withLevel := Request{
		Model:          "m",
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ReasoningLevel: reasoning.High,
	}
	raw, err := c.marshalBody(withLevel)
	if err != nil {
		t.Fatalf("marshalBody: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding marshaled body: %v", err)
	}
	if got := body["max_tokens"]; got != float64(4096) {
		t.Fatalf("max_tokens=%v, want 4096 (unvetted provider never told to reason)", got)
	}
}
