package chatsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeAPI is the shared test double for /v1/chat-sessions.
//
// What makes it different from an ad-hoc httptest mux is that it holds REAL
// per-session sequence state and enforces the server's contiguity contract
// against it. A mux that returns a canned 400 proves only that the client can
// read a status line; it cannot produce a gap-rebase, an honest insertedCount,
// or an oversize rejection CONSISTENTLY with the state the client believes the
// server holds. A handler tested against a canned response therefore proves
// nothing about the case it claims to cover - which is how the missing 400
// handling shipped green.
//
// The modelled contract, and its provenance in the livechat probes, is pinned
// by fakeapi_contract_test.go. Two behaviours are NOT pinned by any probe and
// are noted there: the exact status for an intra-batch gap and for an oversize
// payload (the probes pin only "not 200"/"4xx"), and the foreign-writer 409
// (the deployed API's only observed 409 on the append path is an ended
// session).
type fakeAPI struct {
	server *httptest.Server

	// maxPayloadBytes is the per-event ceiling. It mirrors the documented
	// 64 KiB Postgres CHECK. Tests may lower it to reach the oversize branch
	// without building a 64 KiB fixture.
	maxPayloadBytes int

	mu       sync.Mutex
	sessions map[string]*fakeSession
	nextID   int

	// appendBatches records every batch the fake ACCEPTED as a request, in
	// order, including rejected ones. A retry test needs to see the identical
	// body arrive twice.
	appendBatches [][]EventItem

	rejectAppend     *fakeRejection
	rejectCreate     *fakeRejection
	failSessionReads bool
	createdIDs       []string
	createAttempts   int
	createTimes      []time.Time

	// requests records every request the fake received, target and body, so a
	// test can assert that a value NEVER crossed the wire.
	requests []recordedRequest
}

type fakeSession struct {
	id            string
	title         string
	status        string
	lastSeq       int64
	events        []StoredEvent
	endedAt       *string
	foreignWriter bool
	inputs        []*SessionInput
}

const fakeMaxPayloadBytes = 64 * 1024

// newFakeAPI starts a fake server bound to the test's lifetime.
func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		maxPayloadBytes: fakeMaxPayloadBytes,
		sessions:        make(map[string]*fakeSession),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", f.requireAuth(f.handleCreate))
	mux.HandleFunc("GET /v1/chat-sessions/{id}", f.requireAuth(f.handleGet))
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", f.requireAuth(f.handleAppend))
	mux.HandleFunc("GET /v1/chat-sessions/{id}/events", f.requireAuth(f.handleReadEvents))
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", f.requireAuth(f.handleHeartbeat))
	mux.HandleFunc("POST /v1/chat-sessions/{id}/end", f.requireAuth(f.handleEnd))
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", f.requireAuth(f.handleNextInput))
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", f.requireAuth(f.handleConsume))

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// URL is the fake's base URL for ClientOptions.
func (f *fakeAPI) URL() string { return f.server.URL }

// NewSession pre-registers a running session and returns its id, for tests
// that need an id before they build a client.
func (f *fakeAPI) NewSession(title string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newSessionLocked(title).id
}

func (f *fakeAPI) newSessionLocked(title string) *fakeSession {
	f.nextID++
	s := &fakeSession{
		id:     fmt.Sprintf("fake-session-%d", f.nextID),
		title:  title,
		status: "running",
	}
	f.sessions[s.id] = s
	f.createdIDs = append(f.createdIDs, s.id)
	return s
}

// Events returns a copy of everything the fake durably stored for a session.
func (f *fakeAPI) Events(id string) []StoredEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	if s == nil {
		return nil
	}
	out := make([]StoredEvent, len(s.events))
	copy(out, s.events)
	return out
}

// LastSeq is the session's server-side high-water mark.
func (f *fakeAPI) LastSeq(id string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sessions[id]; s != nil {
		return s.lastSeq
	}
	return 0
}

// Batches returns every append batch the fake received, rejected ones included.
func (f *fakeAPI) Batches() [][]EventItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]EventItem, len(f.appendBatches))
	copy(out, f.appendBatches)
	return out
}

// ClaimForeignWriter makes every later append to this session 409, modelling a
// second writer that took the session over.
func (f *fakeAPI) ClaimForeignWriter(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sessions[id]; s != nil {
		s.foreignWriter = true
	}
}

// SetMaxPayloadBytes lowers the per-event ceiling so a test can reach the
// oversize branch cheaply.
func (f *fakeAPI) SetMaxPayloadBytes(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maxPayloadBytes = n
}

// AdvanceServerSeq plants events the client did not send, so the server is
// ahead of the client's cursor without the client having any record of them.
// This is the state a lost ack leaves behind.
func (f *fakeAPI) AdvanceServerSeq(id string, upTo int64) {
	f.AdvanceServerSeqAs(id, upTo, "")
}

// AdvanceServerSeqAs plants server-ahead events stamped with a writer id, so
// a test can drive the adopt branch (our own id) and the fork branch (anyone
// else's) from the same state.
func (f *fakeAPI) AdvanceServerSeqAs(id string, upTo int64, writerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	if s == nil {
		return
	}
	payload := json.RawMessage(`{"planted":true}`)
	if writerID != "" {
		payload = json.RawMessage(fmt.Sprintf(`{"planted":true,"writer_id":%q}`, writerID))
	}
	for seq := s.lastSeq + 1; seq <= upTo; seq++ {
		s.events = append(s.events, StoredEvent{
			SessionID: id,
			Seq:       seq,
			Type:      TypeTurnStarted,
			Payload:   payload,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		s.lastSeq = seq
	}
}

// RawGetStatus issues a bare GET with the supplied bearer (empty for none) and
// returns the status, for guards the typed client refuses to reach.
func (f *fakeAPI) RawGetStatus(t *testing.T, path, bearer string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.URL()+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatalf("raw GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// requireAuth fails closed on a missing bearer. The client uploads conversation
// content, so a fake that answered an unauthenticated request would let a
// dropped Authorization header pass every test in this package.
func (f *fakeAPI) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if r.Header.Get("Authorization") == "" {
			writeAPIError(w, http.StatusUnauthorized, "Unauthorized", "missing bearer token")
			return
		}
		next(w, r)
	}
}

// recordedRequest is one request exactly as it arrived: the target the client
// built and the bytes it sent. It exists so a test can assert what is NOT on
// the wire, which no handler-level assertion can do.
type recordedRequest struct {
	Method string
	Target string
	Body   []byte
}

// record captures the request and restores its body for the real handler.
// It is on requireAuth because that is the single choke point every route
// passes through; a new route therefore cannot escape recording.
func (f *fakeAPI) record(r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method,
		Target: r.URL.RequestURI(),
		Body:   body,
	})
}

// Requests returns every request the fake received, in order.
func (f *fakeAPI) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *fakeAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var params CreateSessionParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bad Request", "malformed request body")
		return
	}
	if len(params.Title) > 200 {
		writeAPIError(w, http.StatusBadRequest, "Bad Request", "title must be at most 200 characters")
		return
	}
	f.mu.Lock()
	f.createAttempts++
	f.createTimes = append(f.createTimes, time.Now())
	if rej := f.rejectCreate; rej != nil {
		f.mu.Unlock()
		writeAPIError(w, rej.status, rej.code, rej.msg)
		return
	}
	s := f.newSessionLocked(params.Title)
	out := s.wire()
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeAPI) handleGet(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.failSessionReads {
		f.mu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, "Internal Server Error", "session read failed")
		return
	}
	s := f.sessions[r.PathValue("id")]
	if s == nil {
		f.mu.Unlock()
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	out := s.wire()
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// handleAppend is the whole reason this fake exists. Order of checks matters:
// a client branching on the response must be able to tell an oversize payload
// (poison, no retry can fix it) from a sequence gap (rebase and continue).
func (f *fakeAPI) handleAppend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Events []EventItem `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Bad Request", "malformed request body")
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendBatches = append(f.appendBatches, req.Events)

	s := f.sessions[r.PathValue("id")]
	if s == nil {
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	if s.status == "ended" {
		writeAPIError(w, http.StatusConflict, "Conflict", "session has ended")
		return
	}
	if s.foreignWriter {
		writeAPIError(w, http.StatusConflict, "Conflict", "session is owned by another writer")
		return
	}
	if rej := f.rejectAppend; rej != nil {
		writeAPIError(w, rej.status, rej.code, rej.msg)
		return
	}
	if status, code, msg := f.validateBatch(s, req.Events); status != 0 {
		writeAPIError(w, status, code, msg)
		return
	}

	inserted := 0
	for _, ev := range req.Events {
		// ON CONFLICT DO NOTHING: a seq the session already holds is a replay.
		if ev.Seq <= s.lastSeq {
			continue
		}
		s.events = append(s.events, StoredEvent{
			SessionID: s.id,
			Seq:       ev.Seq,
			Type:      ev.Type,
			Payload:   append(json.RawMessage(nil), ev.Payload...),
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		s.lastSeq = ev.Seq
		inserted++
	}
	writeJSON(w, http.StatusOK, AppendResult{LastSeq: s.lastSeq, InsertedCount: inserted})
}

// validateBatch returns (0, "", "") when the batch may be applied, or the
// status, error code and message to reject it with. The caller must hold f.mu.
func (f *fakeAPI) validateBatch(s *fakeSession, evs []EventItem) (int, string, string) {
	if len(evs) == 0 {
		return http.StatusBadRequest, "Bad Request", "events must not be empty"
	}
	for i, ev := range evs {
		if ev.Seq < 1 {
			return http.StatusBadRequest, "Bad Request",
				fmt.Sprintf("seq must be at least 1, got %d", ev.Seq)
		}
		if len(ev.Payload) > f.maxPayloadBytes {
			return http.StatusBadRequest, "Bad Request",
				fmt.Sprintf("payload for seq %d is %d bytes, over the %d byte ceiling",
					ev.Seq, len(ev.Payload), f.maxPayloadBytes)
		}
		if i > 0 && ev.Seq != evs[i-1].Seq+1 {
			return http.StatusBadRequest, "Bad Request",
				fmt.Sprintf("sequence is not contiguous within the batch: %d follows %d",
					ev.Seq, evs[i-1].Seq)
		}
	}
	if first := evs[0].Seq; first > s.lastSeq+1 {
		return http.StatusBadRequest, "Bad Request",
			fmt.Sprintf("sequence gap: expected %d, got %d", s.lastSeq+1, first)
	}
	return 0, "", ""
}

func (f *fakeAPI) handleReadEvents(w http.ResponseWriter, r *http.Request) {
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("afterSeq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[r.PathValue("id")]
	if s == nil {
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	out := []StoredEvent{}
	for _, ev := range s.events {
		if ev.Seq <= afterSeq {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeAPI) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[r.PathValue("id")]
	if s == nil {
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	if s.status == "ended" {
		writeAPIError(w, http.StatusConflict, "Conflict", "session has ended")
		return
	}
	if body.Status != "" {
		s.status = body.Status
	}
	writeJSON(w, http.StatusOK, s.wire())
}

func (f *fakeAPI) handleEnd(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[r.PathValue("id")]
	if s == nil {
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	s.status = "ended"
	if s.endedAt == nil {
		at := time.Now().UTC().Format(time.RFC3339)
		s.endedAt = &at
	}
	writeJSON(w, http.StatusOK, s.wire())
}

// QueueInput queues a remote input for the poller path.
func (f *fakeAPI) QueueInput(id, body string) *SessionInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	if s == nil {
		return nil
	}
	in := &SessionInput{
		ID:        fmt.Sprintf("%s-input-%d", id, len(s.inputs)+1),
		SessionID: id,
		Kind:      "message",
		Body:      body,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.inputs = append(s.inputs, in)
	return in
}

func (f *fakeAPI) handleNextInput(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[r.PathValue("id")]
	if s == nil {
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	for _, in := range s.inputs {
		if in.ConsumedAt == nil {
			writeJSON(w, http.StatusOK, NextInput{Input: in})
			return
		}
	}
	writeJSON(w, http.StatusOK, NextInput{})
}

func (f *fakeAPI) handleConsume(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[r.PathValue("id")]
	if s == nil {
		writeAPIError(w, http.StatusNotFound, "Not Found", "session not found")
		return
	}
	for _, in := range s.inputs {
		if in.ID != r.PathValue("inputID") {
			continue
		}
		if in.ConsumedAt != nil {
			// Exactly-once: the loser of the race must be able to tell it lost.
			writeAPIError(w, http.StatusConflict, "Conflict", "input already consumed")
			return
		}
		at := time.Now().UTC().Format(time.RFC3339)
		in.ConsumedAt = &at
		writeJSON(w, http.StatusOK, in)
		return
	}
	writeAPIError(w, http.StatusNotFound, "Not Found", "input not found")
}

// ---------------------------------------------------------------------------
// Encoding helpers
// ---------------------------------------------------------------------------

func (s *fakeSession) wire() Session {
	return Session{
		ID:              s.id,
		OrganizationID:  "fake-org",
		UserID:          "fake-user",
		Title:           s.title,
		Status:          s.status,
		LastSeq:         s.lastSeq,
		LastEventAt:     time.Now().UTC().Format(time.RFC3339),
		LastHeartbeatAt: time.Now().UTC().Format(time.RFC3339),
		EndedAt:         s.endedAt,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeAPIError emits the API's global exception-filter envelope, so the
// client's own parseErrorResponse is exercised rather than bypassed.
func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	encoded, _ := json.Marshal(msg)
	writeJSON(w, status, ErrorEnvelope{
		StatusCode: status,
		Error:      code,
		Message:    encoded,
	})
}

// ---------------------------------------------------------------------------
// Failure knobs
//
// These sit ON TOP of the stateful model rather than replacing it: a test that
// needs a rejection the contiguity model cannot produce (an unclassified 400,
// an unreachable session record) declares it explicitly, and every other rule
// still applies.
// ---------------------------------------------------------------------------

// EndSession ends a session server-side, as a web viewer would: later
// appends answer 409.
func (f *fakeAPI) EndSession(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.sessions[id]; s != nil {
		s.status = "ended"
	}
}

// DeleteSession removes the row, as a web delete does: later appends and
// reads answer 404.
func (f *fakeAPI) DeleteSession(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
}

// SessionIDs returns every session ever created, in creation order,
// deleted ones included.
func (f *fakeAPI) SessionIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.createdIDs...)
}

// RejectCreatesWith makes every later POST /v1/chat-sessions fail with a
// fixed response; ClearCreateRejection lifts it.
func (f *fakeAPI) RejectCreatesWith(status int, code, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectCreate = &fakeRejection{status: status, code: code, msg: msg}
}

func (f *fakeAPI) ClearCreateRejection() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectCreate = nil
}

// CreateTimes returns when each POST /v1/chat-sessions arrived, rejected
// ones included.
func (f *fakeAPI) CreateTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.createTimes...)
}

// CreateAttempts counts every POST /v1/chat-sessions, rejected ones included.
func (f *fakeAPI) CreateAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createAttempts
}

// RejectAppendsWith makes every later append fail with a fixed response.
func (f *fakeAPI) RejectAppendsWith(status int, code, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectAppend = &fakeRejection{status: status, code: code, msg: msg}
}

// ClearAppendRejection lifts RejectAppendsWith, modelling a server that came
// back.
func (f *fakeAPI) ClearAppendRejection() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectAppend = nil
}

// FailSessionReads makes GET /v1/chat-sessions/{id} fail with a 500, modelling
// a server the client cannot re-read its own state from.
func (f *fakeAPI) FailSessionReads(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSessionReads = on
}

type fakeRejection struct {
	status int
	code   string
	msg    string
}
