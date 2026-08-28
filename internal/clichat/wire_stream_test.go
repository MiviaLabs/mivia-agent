package clichat

// Tests for the [subagents] wire_stream knob. The knob sends nested subagent
// calls to the provider's SSE endpoint while the call keeps its non-stream
// contract. An explicit false is the operator opt-out; an absent key resolves
// on.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestSkillHandlerCarriesWireStream checks the resolved wire-stream switch on
// the skill surface's constructed handler (field assertion, not wire time),
// so the same knob that shapes routed-agent calls also shapes skill runs.
func TestSkillHandlerCarriesWireStream(t *testing.T) {
	t.Parallel()
	on := true
	off := false
	cases := []struct {
		name       string
		wireStream *bool
		want       bool
	}{
		{name: "nil_resolves_on", wireStream: nil, want: true},
		{name: "true_resolves_on", wireStream: &on, want: true},
		{name: "false_opts_out", wireStream: &off, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.SubagentConfig{WireStream: tc.wireStream}
			h := newSkillMultiStepHandler(skillHandlerDeps{}, cfg, skills.Definition{Name: "review"})
			if h.WireStreamTransport != tc.want {
				t.Fatalf("skill handler WireStreamTransport = %v, want %v", h.WireStreamTransport, tc.want)
			}
		})
	}
}

// wireStreamE2EServer answers every chat request and records the first wire
// body. A stream:true body gets an SSE answer; any other body gets a plain
// non-stream JSON answer. The server mirrors the wire bodies a nested call
// sends, so a test can check the transport shape the knob chose.
func wireStreamE2EServer(t *testing.T, firstBody *[]byte, calls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if atomic.AddInt32(calls, 1) == 1 {
			*firstBody = body
		}
		if bytes.Contains(body, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"streamed"}}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"plain"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWireStreamOptOutE2E drives a nested-shaped call - a oneshot dispatch
// through a real session dispatcher and a real provider client - and checks
// the wire body. wire_stream = false keeps the plain non-stream endpoint;
// the absent key sends stream:true and still returns the full answer.
func TestWireStreamOptOutE2E(t *testing.T) {
	on := true
	off := false
	cases := []struct {
		name         string
		wireStream   *bool
		wantWireTrue bool
		wantWireOn   bool
		wantContent  string
	}{
		{name: "false_opts_out_keeps_plain_wire", wireStream: &off, wantWireOn: true, wantContent: "plain"},
		{name: "nil_streams_on_the_wire", wireStream: nil, wantWireTrue: true, wantContent: "streamed"},
		{name: "true_streams_on_the_wire", wireStream: &on, wantWireTrue: true, wantContent: "streamed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var firstBody []byte
			var calls int32
			srv := wireStreamE2EServer(t, &firstBody, &calls)

			ws, err := workspace.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultSubagentConfig
			cfg.WireStream = tc.wireStream
			comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
			d, err := NewSessionDispatcher(SessionDispatcherOpts{
				Registry:  tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}),
				Completer: comp,
				Model:     "test-model",
				Config:    cfg,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()

			result := d.Invoke(context.Background(), runtime.Request{
				Kind:  runtime.Subagent,
				Name:  cliorchestrate.HandlerOneshot,
				Input: json.RawMessage(`"work"`),
			})
			if result.Err != nil {
				t.Fatalf("dispatch: %v", result.Err)
			}
			if !strings.Contains(string(result.Output), tc.wantContent) {
				t.Fatalf("result envelope %s misses answer %q", result.Output, tc.wantContent)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Fatalf("provider requests = %d, want 1", got)
			}
			if tc.wantWireTrue && !bytes.Contains(firstBody, []byte(`"stream":true`)) {
				t.Fatalf("first wire body misses \"stream\":true: %s", firstBody)
			}
			if tc.wantWireOn {
				if !bytes.Contains(firstBody, []byte(`"stream":false`)) {
					t.Fatalf("first wire body misses \"stream\":false: %s", firstBody)
				}
				if bytes.Contains(firstBody, []byte(`"stream":true`)) {
					t.Fatalf("opted-out call sent stream:true on the wire: %s", firstBody)
				}
			}
		})
	}
}
