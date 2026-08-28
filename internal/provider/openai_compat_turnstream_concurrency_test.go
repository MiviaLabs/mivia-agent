package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestChatTurnStreamTransportConcurrentMultiStepDoesNotHang is the
// investigation record for a real user-reported incident: two independent
// sessions saw dispatch_tasks batches (default_worker concurrency, 3-4
// parallel subagent tasks) reach "running" and never complete or surface
// output once [subagents] wire_stream defaulted on (d2b1430c). Disabling
// the default (f899ceaf) confirmed unblocked them, but this test's job is
// to try to reproduce the actual hang against a controlled mock provider -
// several workers sharing ONE *OpenAICompat client, each running several
// SEQUENTIAL turns (a multi-step loop), with the mock server periodically
// stalling one stream attempt (forcing the content-idle watchdog's
// stall-retry and stream-hostile-memory paths) under concurrent load.
//
// Run with -race: this reproduced NO hang and NO race across repeated
// runs, so the incident most likely needs the real (non-mock) provider's
// behavior under concurrent SSE connections to reproduce - a proxy/gateway
// handling concurrent streams differently than a direct connection, rate
// limiting, or connection-pool contention this local server does not
// replicate. Keep this test as a standing regression guard for the
// mechanism this local harness CAN exercise (concurrent workers + stall
// retries sharing one client's streamHostile memory) even though it did
// not itself catch the reported incident.
func TestChatTurnStreamTransportConcurrentMultiStepDoesNotHang(t *testing.T) {
	withStreamContentIdleTimeout(t, 300*time.Millisecond)
	const workers = 4
	const stepsPerWorker = 5

	var reqNum int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		isStream := bytes.Contains(body, []byte(`"stream":true`))
		n := atomic.AddInt64(&reqNum, 1)
		if isStream && n%4 == 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(500 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
		if isStream {
			sseChunks(w, []string{
				`{"choices":[{"delta":{"content":"partial "}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			})
			return
		}
		jsonAnswer(w, r)
	}))
	defer srv.Close()

	c := streamTransportClient(t, srv, false)

	done := make(chan error, workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			for step := 0; step < stepsPerWorker; step++ {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				req := turnStreamReq()
				req.Timeout = 5 * time.Second
				_, err := c.ChatTurn(ctx, req)
				cancel()
				if err != nil {
					done <- fmt.Errorf("worker %d step %d: %w", i, step, err)
					return
				}
			}
			done <- nil
		}()
	}

	deadline := time.After(25 * time.Second)
	for i := 0; i < workers; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker error: %v", err)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for concurrent multi-step workers (%d/%d completed) - genuine hang reproduced", i, workers)
		}
	}
}
