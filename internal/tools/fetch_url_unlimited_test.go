package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bodyServer serves a fixed text/plain body of the given size.
func bodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fetchBody is a body whose only occurrence of the marker string is at its
// very end, so the marker's presence/absence in the tool output proves
// whether the read reached the tail. A uniform body would make the tail
// substring appear in every window and the assertion meaningless.
const fetchTailMarker = "TAILMARKER"

func fetchBody(size int) string {
	return strings.Repeat("x", size-len(fetchTailMarker)) + fetchTailMarker
}

// maxFetchKB=0 must mean unlimited: the whole body is read, even when it is
// far larger than the old 1024 KiB default the registry used to hardcode. The
// fetch_url tool, not the registry, owns this meaning now.
func TestFetchURLMaxFetchKBZeroReadsWholeBody(t *testing.T) {
	const bodyBytes = 2 << 20 // 2 MiB, twice the old 1024 KiB default
	body := fetchBody(bodyBytes)
	srv := bodyServer(t, body)

	tool := &fetchURLTool{
		maxLocalBytes:     4 << 20, // room for the whole body in the result
		maxFetchKB:        0,       // unlimited
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	// The end-of-body marker only appears if the read reached the tail, so
	// its presence proves the read was not bounded by maxFetchKB.
	if !strings.Contains(out, fetchTailMarker) {
		t.Fatalf("maxFetchKB=0 did not read the whole body: output %d bytes, want the %d-byte body's tail",
			len(out), bodyBytes)
	}
}

// The bounded case is the contrast that makes the unlimited assertion real:
// with a small positive maxFetchKB the same server's body is cut before the
// tail, so the unlimited test above is not passing because of a
// generous-but-finite bound.
func TestFetchURLMaxFetchKBPositiveTruncatesBody(t *testing.T) {
	const bodyBytes = 2 << 20
	body := fetchBody(bodyBytes)
	srv := bodyServer(t, body)

	tool := &fetchURLTool{
		maxLocalBytes:     4 << 20, // result budget is not the limiting factor
		maxFetchKB:        1,       // 1 KiB
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	// The 1 KiB limit leaves no room for the end-of-body marker, so its
	// absence is the proof the positive bound was applied.
	if strings.Contains(out, fetchTailMarker) {
		t.Fatalf("maxFetchKB=1 read past its 1 KiB bound: body tail present in output")
	}
	if len(out) > 4096 {
		t.Fatalf("maxFetchKB=1 produced %d bytes of output, want ~1 KiB", len(out))
	}
}
