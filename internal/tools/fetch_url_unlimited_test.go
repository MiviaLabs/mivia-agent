package tools

import (
	"context"
	"encoding/json"
	"fmt"
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

// A non-text Content-Type must be refused before the body is read: the tool
// should error out instead of surfacing binary bytes as page text.
func TestFetchURLRefusesBinaryContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("\x00\x01\x02 binary payload"))
	}))
	t.Cleanup(srv.Close)

	tool := &fetchURLTool{
		maxLocalBytes:     4 << 20,
		maxFetchKB:        0,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err == nil {
		t.Fatal("expected an error for a binary Content-Type, got nil")
	}
	if !strings.Contains(err.Error(), "refused binary content") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "refused binary content")
	}
}

// offset+limit pagination must return the requested window and a trailer that
// points at the next page. Plain newlines are collapsed by stripHTMLTags, so
// the multi-line page is built from block-level <li> tags, which produce the
// structural newlines the pagination splits on.
func TestFetchURLPagination(t *testing.T) {
	var items strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&items, "<li>line %d</li>", i)
	}
	srv := bodyServer(t, "<ul>"+items.String()+"</ul>")

	tool := &fetchURLTool{
		maxLocalBytes:     4 << 20,
		maxFetchKB:        0,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`","offset":5,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	// Lines 5-7 (1-based) are the requested window.
	for _, want := range []string{"line 5", "line 6", "line 7"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// Lines outside the window must not leak into the page.
	for _, unwanted := range []string{"line 4", "line 8"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output contains %q, want only the requested window:\n%s", unwanted, out)
		}
	}
	// 20 total lines; the 5-7 window leaves 13, and the next page starts at 8.
	if !strings.Contains(out, "... 13 more lines (use offset=8 to continue)") {
		t.Fatalf("output missing pagination trailer:\n%s", out)
	}
}

// An offset beyond the last line must yield an empty page rather than an
// out-of-range error or a partial tail.
func TestFetchURLOffsetPastEnd(t *testing.T) {
	var items strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&items, "<li>line %d</li>", i)
	}
	srv := bodyServer(t, "<ul>"+items.String()+"</ul>")

	tool := &fetchURLTool{
		maxLocalBytes:     4 << 20,
		maxFetchKB:        0,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`","offset":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(empty page)" {
		t.Fatalf("out = %q, want %q", out, "(empty page)")
	}
}
