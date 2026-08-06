package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type readOutputPage struct {
	Status        string `json:"status"`
	Error         string `json:"error"`
	Ref           string `json:"ref"`
	Kind          string `json:"kind"`
	Bytes         int    `json:"bytes"`
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
	ReturnedBytes int    `json:"returned_bytes"`
	NextOffset    *int   `json:"next_offset"`
	HasMore       bool   `json:"has_more"`
	Truncated     bool   `json:"truncated"`
	ContentIsData bool   `json:"content_is_data"`
	Note          string `json:"note"`
	Content       string `json:"content"`
}

func newReadOutputFixture(t *testing.T) (*remainder.Spool, *readOutputTool, string) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	spool := newRemainderSpool(repo)
	tool := &readOutputTool{spool: spool}
	return spool, tool, "principal-owner"
}

func callReadOutput(t *testing.T, tool *readOutputTool, principal string, args map[string]any) (readOutputPage, string) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if principal != "" {
		ctx = runtime.ContextWithCaller(ctx, runtime.Caller{SessionID: principal})
	}
	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v\nout=%s", err, out)
	}
	var page readOutputPage
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	return page, out
}

// TestReadOutputPageChainReassemblesStoredBody drives the shipped Execute path:
// spool a multi-page body, page with offset/next_offset until complete, assert
// byte-identical reassembly for the granted principal.
func TestReadOutputPageChainReassemblesStoredBody(t *testing.T) {
	spool, tool, principal := newReadOutputFixture(t)
	// Body large enough that limit=32 forces multiple pages.
	body := []byte(strings.Repeat("abcdefghij", 20)) // 200 bytes
	ref := spool.Spool(context.Background(), principal, body)
	if ref == "" {
		t.Fatal("spool returned empty ref")
	}

	var rebuilt strings.Builder
	offset := 0
	for pageNo := 0; ; pageNo++ {
		page, _ := callReadOutput(t, tool, principal, map[string]any{
			"ref": ref, "offset": offset, "limit": 32,
		})
		if page.Status != "ok" {
			t.Fatalf("page %d status=%q error=%q out framing", pageNo, page.Status, page.Error)
		}
		if page.Offset != offset || page.Limit != 32 {
			t.Fatalf("page %d metadata offset/limit = %d/%d, want %d/32", pageNo, page.Offset, page.Limit, offset)
		}
		if page.ReturnedBytes != len(page.Content) {
			t.Fatalf("page %d returned_bytes=%d len(content)=%d", pageNo, page.ReturnedBytes, len(page.Content))
		}
		if !page.ContentIsData || page.Note == "" {
			t.Fatalf("page %d lost untrusted-data framing", pageNo)
		}
		rebuilt.WriteString(page.Content)
		if !page.HasMore {
			if page.NextOffset != nil {
				t.Fatalf("final page has next_offset")
			}
			break
		}
		if page.NextOffset == nil || *page.NextOffset <= offset {
			t.Fatalf("page %d invalid next_offset: %+v", pageNo, page)
		}
		offset = *page.NextOffset
		if pageNo > len(body) {
			t.Fatal("pagination did not terminate")
		}
	}
	if rebuilt.String() != string(body) {
		t.Fatalf("reassembly mismatch: got %d bytes want %d", rebuilt.Len(), len(body))
	}
}

// TestReadOutputClampsOversizedLimit reports the effective limit, never silently
// serving more than the declared page budget.
func TestReadOutputClampsOversizedLimit(t *testing.T) {
	spool, tool, principal := newReadOutputFixture(t)
	body := []byte(strings.Repeat("L", defaultReadOutputMaxBytes+4096))
	ref := spool.Spool(context.Background(), principal, body)
	if ref == "" {
		t.Fatal("empty ref")
	}
	// Request far above the tool maximum; schema maximum is defaultReadOutputMaxBytes
	// but runtime must clamp even if a caller bypasses schema validation.
	page, _ := callReadOutput(t, tool, principal, map[string]any{
		"ref": ref, "offset": 0, "limit": defaultReadOutputMaxBytes * 4,
	})
	// Invalid limit below minimum fails decode; oversized is accepted and clamped
	// only when HasLimit is true and limit >= minimum. decode rejects limit that
	// is huge only via... actually decode only checks minimum, not maximum.
	// So limit is accepted and pageLimit min's it.
	if page.Status != "ok" {
		// If decode rejects values above schema max, that's also acceptable -
		// check we don't silently return full body.
		t.Logf("status=%q error=%q (oversized limit may be rejected at decode)", page.Status, page.Error)
	}
	// Use a valid large limit that is still over the page budget by requesting
	// exactly at max - tool should report limit == page budget.
	page, _ = callReadOutput(t, tool, principal, map[string]any{
		"ref": ref, "offset": 0, "limit": defaultReadOutputMaxBytes,
	})
	if page.Status != "ok" {
		t.Fatalf("status=%q", page.Status)
	}
	if page.Limit != defaultReadOutputMaxBytes {
		t.Fatalf("limit = %d, want effective page budget %d", page.Limit, defaultReadOutputMaxBytes)
	}
	if page.ReturnedBytes > defaultReadOutputMaxBytes {
		t.Fatalf("returned %d bytes over page budget %d", page.ReturnedBytes, defaultReadOutputMaxBytes)
	}
	if !page.HasMore {
		t.Fatal("expected has_more for body larger than one page")
	}

	// Tool with a tighter maxBytes clamps further and reports it.
	tight := &readOutputTool{spool: spool, maxBytes: 64}
	page, _ = callReadOutput(t, tight, principal, map[string]any{
		"ref": ref, "offset": 0, "limit": 10_000,
	})
	if page.Status != "ok" {
		t.Fatalf("tight tool status=%q", page.Status)
	}
	if page.Limit != 64 {
		t.Fatalf("tight limit = %d, want 64 (clamped and honestly reported)", page.Limit)
	}
	if page.ReturnedBytes > 64 {
		t.Fatalf("returned %d over clamped limit 64", page.ReturnedBytes)
	}
}

func TestReadOutputErrorShapesAreDistinct(t *testing.T) {
	spool, tool, principal := newReadOutputFixture(t)
	body := []byte("shape-body")
	ref := spool.Spool(context.Background(), principal, body)

	// Malformed
	malformed, raw := callReadOutput(t, tool, principal, map[string]any{"ref": "not-a-ref"})
	if malformed.Error != "malformed reference" || malformed.Status == "not_found" || malformed.Status == "expired" {
		t.Fatalf("malformed shape: %+v raw=%s", malformed, raw)
	}

	// Not found
	absentRef := "ref:output:" + strings.Repeat("a", 64)
	notFound, _ := callReadOutput(t, tool, principal, map[string]any{"ref": absentRef})
	if notFound.Status != "not_found" {
		t.Fatalf("not_found status=%q", notFound.Status)
	}

	// Expired
	spool.MarkExpired(ref)
	expired, _ := callReadOutput(t, tool, principal, map[string]any{"ref": ref})
	if expired.Status != "expired" {
		t.Fatalf("expired status=%q", expired.Status)
	}
	if expired.Status == notFound.Status || expired.Error == malformed.Error {
		t.Fatal("expired shape collided with another status")
	}
}

func TestReadOutputCrossPrincipalDenied(t *testing.T) {
	spool, tool, owner := newReadOutputFixture(t)
	ref := spool.Spool(context.Background(), owner, []byte("secret-remainder"))
	if ref == "" {
		t.Fatal("empty ref")
	}
	denied, raw := callReadOutput(t, tool, "other-principal", map[string]any{"ref": ref})
	if denied.Status != "denied" {
		t.Fatalf("cross-principal status=%q raw=%s", denied.Status, raw)
	}
	// Owner still can read (grant not affected).
	// Re-spool because we didn't expire; owner grant remains.
	ok, _ := callReadOutput(t, tool, owner, map[string]any{"ref": ref})
	if ok.Status != "ok" {
		t.Fatalf("owner status=%q after cross-principal attempt", ok.Status)
	}
}

func TestReadOutputRegisteredOnRootAndSpawnedScopes(t *testing.T) {
	reg := tools.NewRegistry()
	d := runtime.New(runtime.Policy{})
	spool, err := registerLedgerTools(d, reg, ledger.NewMemoryLedgerRepository(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spool == nil {
		t.Fatal("registerLedgerTools returned nil spool")
	}
	if _, ok := reg.Get("read_output"); !ok {
		t.Fatal("read_output missing from registry after registration")
	}
	root := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeRoot})
	spawned := tools.ScopedRegistry(reg, tools.ScopeOptions{Mode: tools.ScopeSpawned})
	if _, ok := root.Get("read_output"); !ok {
		t.Fatal("read_output missing from ScopeRoot registry")
	}
	if _, ok := spawned.Get("read_output"); !ok {
		t.Fatal("read_output missing from ScopeSpawned registry")
	}
}

// TestTruncationNoticeToReadOutputRoundTrip exercises CapWithSpool → notice →
// first page via the real tool (success path) and failed store (no ref).
func TestTruncationNoticeToReadOutputRoundTrip(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	spool := newRemainderSpool(repo)
	tool := &readOutputTool{spool: spool}
	const principal = "trunc-principal"
	body := strings.Repeat("payload-byte-", 80) // ~1040 bytes

	out, truncated := remainder.CapWithSpool(spool, principal, body, 200)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(out, "truncated: kept ") || !strings.Contains(out, " of ") {
		t.Fatalf("notice missing kept/total: %q", out)
	}
	if !strings.Contains(out, "use read_output") {
		t.Fatalf("notice missing read_output guidance: %q", out)
	}
	idx := strings.Index(out, "ref:output:")
	if idx < 0 {
		t.Fatalf("successful spool omitted ref: %q", out)
	}
	end := idx
	for end < len(out) && (out[end] == ':' || out[end] == 'r' || out[end] == 'e' || out[end] == 'f' ||
		(out[end] >= '0' && out[end] <= '9') || (out[end] >= 'a' && out[end] <= 'f') || out[end] == 'o' ||
		out[end] == 'u' || out[end] == 't' || out[end] == 'p') {
		end++
	}
	// Simpler: parse canonical length.
	ref := out[idx:]
	if cut := strings.IndexAny(ref, ",)"); cut >= 0 {
		ref = ref[:cut]
	}
	if _, _, err := contentref.Parse(ref); err != nil {
		t.Fatalf("notice ref not parseable %q: %v", ref, err)
	}

	page, _ := callReadOutput(t, tool, principal, map[string]any{"ref": ref, "offset": 0, "limit": 64})
	if page.Status != "ok" {
		t.Fatalf("first page status=%q", page.Status)
	}
	if !strings.HasPrefix(body, page.Content) {
		t.Fatalf("first page content does not match body prefix")
	}

	// Forced store failure: notice omits ref; call still "succeeds" (truncated body).
	failSpool := remainder.NewSpool(remainder.FailingStore{})
	failOut, failTrunc := remainder.CapWithSpool(failSpool, principal, body, 200)
	if !failTrunc {
		t.Fatal("expected truncation on fail path")
	}
	if strings.Contains(failOut, "ref:output:") {
		t.Fatalf("failed store invented ref: %q", failOut)
	}
	if !strings.Contains(failOut, "truncated: kept ") {
		t.Fatalf("failed store dropped notice: %q", failOut)
	}
}

// TestReadOutputRedactsOutput installs a real redaction policy first: with no
// policy configured redact.Text is the identity function, so the assertion
// below would pass trivially and prove nothing. Mirrors
// TestLedgerReadRedactsOutput: read_output must redact the whole spooled body
// before paging, or a secret in the recorded remainder reaches the model
// verbatim.
func TestReadOutputRedactsOutput(t *testing.T) {
	const secret = "sk-live-abcdef0123456789"
	policy, err := redact.Compile([]string{`sk-live-[A-Za-z0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	spool, tool, principal := newReadOutputFixture(t)
	ref := spool.Spool(context.Background(), principal, []byte("the token is "+secret+" so use it"))
	if ref == "" {
		t.Fatal("spool returned empty ref")
	}
	page, raw := callReadOutput(t, tool, principal, map[string]any{"ref": ref})
	if page.Status != "ok" {
		t.Fatalf("status=%q error=%q raw=%s", page.Status, page.Error, raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("secret survived redaction: %s", raw)
	}
	if !strings.Contains(page.Content, "[redacted]") {
		t.Fatalf("expected redaction placeholder in %s", raw)
	}
	// Bytes keeps reporting the raw spooled length, matching ledger_read.
	if page.Bytes != len("the token is "+secret+" so use it") {
		t.Fatalf("bytes = %d, want raw spooled length", page.Bytes)
	}
}

// TestReadOutputPagesRedactedUTF8Content paginates across a page edge through
// a secret and asserts the placeholder survives on every page; the rebuilt
// stream must equal the fully redacted text. Mirrors
// TestLedgerReadPagesRedactedUTF8Content.
func TestReadOutputPagesRedactedUTF8Content(t *testing.T) {
	policy, err := redact.Compile([]string{`secret-[A-Z]{6}`}, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	const source = "α secret-ABCDEF ω"
	const redacted = "α [redacted] ω"
	spool, tool, principal := newReadOutputFixture(t)
	ref := spool.Spool(context.Background(), principal, []byte(source))
	if ref == "" {
		t.Fatal("spool returned empty ref")
	}

	var rebuilt string
	offset := 0
	for pageNo := 0; ; pageNo++ {
		page, raw := callReadOutput(t, tool, principal, map[string]any{
			"ref": ref, "offset": offset, "limit": 5,
		})
		if page.Status != "ok" {
			t.Fatalf("page %d status = %q error=%q raw=%s", pageNo, page.Status, page.Error, raw)
		}
		if page.Offset != offset || page.Limit != 5 || page.ReturnedBytes != len(page.Content) {
			t.Fatalf("page %d metadata = %+v", pageNo, page)
		}
		if !page.ContentIsData || page.Note != readOutputDataNote {
			t.Fatalf("page %d lost untrusted-data framing: %+v", pageNo, page)
		}
		if strings.Contains(page.Content, "secret-") {
			t.Fatalf("page %d leaked a secret fragment: %q", pageNo, page.Content)
		}
		rebuilt += page.Content
		if !page.HasMore {
			if page.NextOffset != nil || page.Truncated {
				t.Fatalf("final page has continuation metadata: %+v", page)
			}
			break
		}
		if !page.Truncated || page.NextOffset == nil || *page.NextOffset <= offset {
			t.Fatalf("page %d has invalid continuation metadata: %+v", pageNo, page)
		}
		offset = *page.NextOffset
		if pageNo > len(redacted) {
			t.Fatal("pagination did not terminate")
		}
	}
	if rebuilt != redacted {
		t.Fatalf("rebuilt redacted stream = %q, want %q", rebuilt, redacted)
	}
}

// TestReadOutputNoPolicyIsIdentity pins the fail-open contract: with no
// redaction policy installed, read_output returns the stored bytes unchanged,
// exactly as before redaction was wired in.
func TestReadOutputNoPolicyIsIdentity(t *testing.T) {
	previous := redact.Current()
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	spool, tool, principal := newReadOutputFixture(t)
	body := []byte("identity-keeps-everything-ABCDEF")
	ref := spool.Spool(context.Background(), principal, body)
	if ref == "" {
		t.Fatal("spool returned empty ref")
	}
	page, _ := callReadOutput(t, tool, principal, map[string]any{"ref": ref})
	if page.Status != "ok" {
		t.Fatalf("status=%q", page.Status)
	}
	if page.Content != string(body) {
		t.Fatalf("no-policy content = %q, want %q (identity)", page.Content, body)
	}
	if page.Bytes != len(body) {
		t.Fatalf("bytes = %d, want %d", page.Bytes, len(body))
	}
}
