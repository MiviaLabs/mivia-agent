package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The fake exists to be WRONG in the same places the server is wrong. These
// tests pin it against the behaviour the livechat probes recorded from the
// deployed API, so a handler tested against the fake is tested against the real
// contiguity contract rather than against a canned status code.
//
// Live-probe provenance is named per test. Where no probe pins the behaviour,
// the test says so.

func fakeClient(t *testing.T, f *fakeAPI) *Client {
	t.Helper()
	return newTestClient(t, ClientOptions{BaseURL: f.URL()})
}

func items(seqs ...int64) []EventItem {
	out := make([]EventItem, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, EventItem{
			Seq:     s,
			Type:    TypeTurnStarted,
			Payload: json.RawMessage(`{"v":1}`),
		})
	}
	return out
}

// Pinned by live_contract_test.go checkRegistration.
func TestFakeAPI_CreateReturnsRunningAtSeqZero(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)

	s, err := c.CreateSession(context.Background(), CreateSessionParams{Title: "fake"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.ID == "" {
		t.Error("no session id")
	}
	if s.Status != "running" {
		t.Errorf("status = %q, want running", s.Status)
	}
	if s.LastSeq != 0 {
		t.Errorf("lastSeq = %d, want 0", s.LastSeq)
	}
	if s.EndedAt != nil {
		t.Errorf("endedAt = %v, want nil", *s.EndedAt)
	}
}

// Pinned by live_contract_test.go checkAppend and checkReplayIdempotent.
func TestFakeAPI_AppendAdvancesThenReplayIsIdempotent(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("append")

	res, err := c.AppendEvents(context.Background(), id, items(1, 2, 3))
	if err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	if res.LastSeq != 3 || res.InsertedCount != 3 {
		t.Errorf("first append = %+v, want lastSeq=3 insertedCount=3", res)
	}

	replay, err := c.AppendEvents(context.Background(), id, items(1, 2, 3))
	if err != nil {
		t.Fatalf("replay AppendEvents: %v", err)
	}
	if replay.InsertedCount != 0 {
		t.Errorf("insertedCount on replay = %d, want 0: the batch was duplicated", replay.InsertedCount)
	}
	if replay.LastSeq != 3 {
		t.Errorf("lastSeq after replay = %d, want 3", replay.LastSeq)
	}
	if got := len(f.Events(id)); got != 3 {
		t.Errorf("the fake stored %d events after a replay, want 3", got)
	}
}

// A resend that overlaps the acknowledged prefix is what the settled gap-400
// recovery produces (chat-sync-cli-slice.md:195). NOT pinned by a live probe:
// the modelled behaviour is the ON CONFLICT DO NOTHING rule the plan cites
// (chat-sync-cli-slice.md section 2), not a recorded response.
func TestFakeAPI_OverlappingResendInsertsOnlyTheNewSuffix(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("overlap")

	if _, err := c.AppendEvents(context.Background(), id, items(1, 2, 3)); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	res, err := c.AppendEvents(context.Background(), id, items(2, 3, 4, 5))
	if err != nil {
		t.Fatalf("overlapping AppendEvents: %v", err)
	}
	if res.InsertedCount != 2 {
		t.Errorf("insertedCount = %d, want 2 (only seq 4 and 5 are new)", res.InsertedCount)
	}
	if res.LastSeq != 5 {
		t.Errorf("lastSeq = %d, want 5", res.LastSeq)
	}
}

// Pinned by live_guards_test.go "a forward sequence gap is rejected", which
// also pins that the message names the sequence problem.
func TestFakeAPI_ForwardSequenceGapIs400(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("gap")

	_, err := c.AppendEvents(context.Background(), id, items(50))
	if err == nil {
		t.Fatal("a forward gap was accepted")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("gap error = %v, want a 400", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "sequence") {
		t.Errorf("gap error = %v; it must name the sequence problem", err)
	}
}

// Pinned by live_guards_test.go TestLiveChatSessionRejectsIntraBatchGap. That
// probe pins only "not 200"; 400 specifically is this fake's choice.
func TestFakeAPI_IntraBatchGapIs400(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("intra-gap")

	_, err := c.AppendEvents(context.Background(), id, []EventItem{
		{Seq: 1, Type: TypeTurnStarted, Payload: json.RawMessage(`{}`)},
		{Seq: 99, Type: TypeTurnStarted, Payload: json.RawMessage(`{}`)},
	})
	if err == nil {
		t.Fatal("a batch with an internal gap (1 then 99) was accepted")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(strings.ToLower(err.Error()), "sequence") {
		t.Errorf("intra-batch gap error = %v, want a 400 naming the sequence problem", err)
	}
	if got := len(f.Events(id)); got != 0 {
		t.Errorf("the fake stored %d events from a rejected batch, want 0", got)
	}
}

// Pinned by live_guards_test.go "seq below 1 is rejected".
func TestFakeAPI_SeqBelowOneIs400(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("seq-zero")

	if _, err := c.AppendEvents(context.Background(), id, items(0)); err == nil {
		t.Fatal("seq 0 was accepted")
	} else if !strings.Contains(err.Error(), "400") {
		t.Errorf("seq 0 error = %v, want a 400", err)
	}
}

// Pinned by live_guards_test.go "an empty batch is rejected".
func TestFakeAPI_EmptyBatchIs400(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("empty")

	if _, err := c.AppendEvents(context.Background(), id, nil); err == nil {
		t.Fatal("an empty batch was accepted")
	} else if !strings.Contains(err.Error(), "400") {
		t.Errorf("empty batch error = %v, want a 400", err)
	}
}

// Pinned by live_guards_test.go TestLiveChatSessionPayloadBoundIsAClientError.
// That probe pins 4xx and not-200; 400 specifically is this fake's choice.
func TestFakeAPI_OversizePayloadIs400(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("oversize")

	blob, _ := json.Marshal(map[string]string{"blob": strings.Repeat("x", 70*1024)})
	_, err := c.AppendEvents(context.Background(), id, []EventItem{
		{Seq: 1, Type: TypeTurnStarted, Payload: blob},
	})
	if err == nil {
		t.Fatal("a 70 KiB payload was accepted; the 64 KiB ceiling is not modelled")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("oversize error = %v, want a 400", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "sequence") {
		t.Error("an oversize rejection must not read as a sequence gap; the handler branches on it")
	}
}

// Pinned by live_guards_test.go "an unknown session is a 404, not a 403".
func TestFakeAPI_UnknownSessionIs404(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)

	if _, err := c.GetSession(context.Background(), "no-such-session"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSession(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := c.AppendEvents(context.Background(), "no-such-session", items(1)); !errors.Is(err, ErrNotFound) {
		t.Errorf("AppendEvents(unknown) = %v, want ErrNotFound", err)
	}
}

// Pinned by live_guards_test.go TestLiveChatSessionEndIsTerminal, which pins
// "not 200"; 409 specifically is what the settled remote-end policy needs
// (chat-sync-cli-slice.md:197).
func TestFakeAPI_AppendToEndedSessionIs409(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("terminal")

	if _, err := c.AppendEvents(context.Background(), id, items(1)); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	if _, err := c.EndSession(context.Background(), id); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := c.AppendEvents(context.Background(), id, items(2)); !errors.Is(err, ErrConflict) {
		t.Errorf("append to an ended session = %v, want ErrConflict", err)
	}
}

// A foreign writer owning the session. NOT pinned by any live probe: the
// deployed API's only observed 409 on this path is an ended session.
func TestFakeAPI_ForeignWriterIs409(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("foreign")

	f.ClaimForeignWriter(id)
	if _, err := c.AppendEvents(context.Background(), id, items(1)); !errors.Is(err, ErrConflict) {
		t.Errorf("append under a foreign writer = %v, want ErrConflict", err)
	}
}

// Pinned by live_contract_test.go checkCursorRead and checkLimit.
func TestFakeAPI_CursorReadIsOrderedAndBounded(t *testing.T) {
	f := newFakeAPI(t)
	c := fakeClient(t, f)
	id := f.NewSession("cursor")

	if _, err := c.AppendEvents(context.Background(), id, items(1, 2, 3)); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	after, err := c.GetEvents(context.Background(), id, 1, 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(after) != 2 || after[0].Seq != 2 || after[1].Seq != 3 {
		t.Errorf("events after seq 1 = %+v, want seqs 2,3", after)
	}

	page, err := c.GetEvents(context.Background(), id, 0, 1)
	if err != nil {
		t.Fatalf("GetEvents with limit: %v", err)
	}
	if len(page) != 1 || page[0].Seq != 1 {
		t.Errorf("limit=1 page = %+v, want one event at seq 1", page)
	}
}

// Pinned by live_guards_test.go "an unauthenticated request is refused". The
// fake fails closed so a client that forgets the bearer cannot pass a test.
func TestFakeAPI_MissingBearerIs401(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("anon")

	c, err := NewClient(func(context.Context, bool) (string, error) { return "", nil }, ClientOptions{BaseURL: f.URL()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// An empty token is refused client-side before it can reach the fake, so
	// drive the transport directly to reach the fake's own guard.
	if _, err := c.GetSession(context.Background(), id); !errors.Is(err, ErrEmptyToken) {
		t.Fatalf("empty token = %v, want ErrEmptyToken", err)
	}
	if status := f.RawGetStatus(t, "/v1/chat-sessions/"+id, ""); status != 401 {
		t.Errorf("unauthenticated GET = %d, want 401", status)
	}
}
