package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// summarySystemMarker identifies the summarize prompt's system message, so a
// scripted completer can answer summary requests with a valid echo reply and
// every other request with plain text. The marker is the first sentence of
// summarySystemPrompt in internal/contextmgr.
const summarySystemMarker = "You summarize an earlier part of a conversation."

// summaryScriptedCompleter answers every completer method. Summary requests
// (detected by the system marker) get a reply that echoes the version and
// source_range lines the prompt mandates; every other request gets "ok". When
// garbage is set, summary requests get invalid JSON instead. Every request is
// recorded so tests can assert on the wire.
type summaryScriptedCompleter struct {
	mu           sync.Mutex
	requests     []provider.Request
	summaryCalls int
	garbage      bool
}

func (c *summaryScriptedCompleter) Name() string { return "stub" }

func (c *summaryScriptedCompleter) record(req provider.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
}

func (c *summaryScriptedCompleter) summaryRequests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []provider.Request
	for _, req := range c.requests {
		if isSummaryRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

// allRequests returns a copy so assertions cannot race a later turn.
func (c *summaryScriptedCompleter) allRequests() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.requests...)
}

func isSummaryRequest(req provider.Request) bool {
	return len(req.Messages) > 0 && req.Messages[0].Role == provider.RoleSystem &&
		strings.HasPrefix(req.Messages[0].Content, summarySystemMarker)
}

// summaryEchoReply builds a valid reply by echoing the version and
// source_range values out of the prompt's "Echo these values" block.
func summaryEchoReply(req provider.Request) string {
	version := "1"
	sourceRange := "{}"
	for _, line := range strings.Split(req.Messages[len(req.Messages)-1].Content, "\n") {
		if v, ok := strings.CutPrefix(line, "version: "); ok {
			version = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "source_range: "); ok {
			sourceRange = strings.TrimSpace(v)
		}
	}
	return fmt.Sprintf(`{"version":%s,"objective":"the user objective","state":"work continued","decisions":[],"evidence":[],"changed_surfaces":[],"open_work":[],"risks":[],"source_range":%s}`, version, sourceRange)
}

func (c *summaryScriptedCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.record(req)
	if isSummaryRequest(req) {
		c.mu.Lock()
		c.summaryCalls++
		c.mu.Unlock()
		if c.garbage {
			return &provider.Response{Content: "this is not json"}, nil
		}
		return &provider.Response{Content: summaryEchoReply(req)}, nil
	}
	return &provider.Response{Content: "ok"}, nil
}

func (c *summaryScriptedCompleter) Chat(_ context.Context, req provider.Request) (string, error) {
	c.record(req)
	return "ok", nil
}

func (c *summaryScriptedCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	c.record(req)
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}

// driveCompactingTurn seeds one large turn, then tightens the prompt budget to
// the next turn's full cost so the next preparation must compact. This is the
// seam-test technique for forcing one real compaction without a huge fixture.
func driveCompactingTurn(t *testing.T, session *chat.Session) {
	t.Helper()
	if _, err := session.SendUser(context.Background(), "first "+strings.Repeat("x", 2000), io.Discard); err != nil {
		t.Fatal(err)
	}
	next := "second question"
	cost, err := provider.EstimatePromptCost(append(session.MessagesCopy(), provider.Message{Role: provider.RoleUser, Content: next}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetPromptBudget(cost); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), next, io.Discard); err != nil {
		t.Fatal(err)
	}
}

// requestCarriesSummary reports whether any recorded outgoing request carries
// the injected context-summary message.
func requestCarriesSummary(requests []provider.Request) bool {
	for _, req := range requests {
		for _, msg := range req.Messages {
			if msg.Name == "context-summary" || strings.Contains(msg.Content, "[host-injected context summary") {
				return true
			}
		}
	}
	return false
}

// summaryPromptCarriesEnvelope reports whether a summary request rendered the
// sealed envelope fields on the configured model.
func summaryPromptCarriesEnvelope(requests []provider.Request, model string) bool {
	for _, req := range requests {
		if !isSummaryRequest(req) || req.Model != model {
			continue
		}
		body := req.Messages[len(req.Messages)-1].Content
		if strings.Contains(body, "objective:") && strings.Contains(body, "Echo these values") {
			return true
		}
	}
	return false
}

// loadActiveSnapshot loads the durable checkpoint for the session's principal.
func loadActiveSnapshot(t *testing.T, store *storage.SQLite, session *chat.Session) (active SummarySnapshot) {
	t.Helper()
	_, input, ok := session.ContextPreparation()
	if !ok {
		t.Fatal("session is not context-enabled")
	}
	snapshot, err := store.Load(context.Background(), input.Principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return SummarySnapshot{Metadata: snapshot.Active.SummaryMetadata}
}

// SummarySnapshot carries only what the integration assertions need.
type SummarySnapshot struct {
	Metadata []byte
}

// TestContextSummaryIntegrationEndToEnd drives the real production wiring with
// no seam overrides: [context.summary] enabled plus [privacy] and an endpoint
// produce a Summarizer; a compacting turn sends a real summary request through
// the LLMSummaryProvider; the reply is validated and injected into the next
// provider request. The commit path stays structural-only (summary failures
// must never fail a finished turn), so the durable checkpoint carries no
// summary metadata.
func TestContextSummaryIntegrationEndToEnd(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}

	driveCompactingTurn(t, session)

	if len(completer.summaryRequests()) == 0 {
		t.Fatal("compacting turn sent no summary request to the completer")
	}
	if !summaryPromptCarriesEnvelope(completer.allRequests(), res.Model) {
		t.Fatal("summary request did not carry the envelope fields on the configured model")
	}
	if !requestCarriesSummary(completer.allRequests()) {
		t.Fatal("no outgoing request carried the injected context-summary message")
	}
	if active := loadActiveSnapshot(t, store, session); len(active.Metadata) != 0 {
		t.Fatal("production wiring persisted summary metadata through the commit seam")
	}
}

// TestContextSummaryIntegrationDegradesOnBadReply proves the no-new-failure-
// modes contract end to end: when the model returns invalid summary JSON, the
// turn still succeeds, no summary message reaches any provider request, and
// the durable checkpoint carries no summary metadata.
func TestContextSummaryIntegrationDegradesOnBadReply(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{garbage: true}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}

	driveCompactingTurn(t, session)

	if len(completer.summaryRequests()) == 0 {
		t.Fatal("degradation test never sent a summary request")
	}
	if requestCarriesSummary(completer.allRequests()) {
		t.Fatal("invalid summary reply still produced an injected summary message")
	}
	if active := loadActiveSnapshot(t, store, session); len(active.Metadata) != 0 {
		t.Fatal("invalid summary reply still persisted summary metadata")
	}
}

// padSessionHistory appends old turns directly to the session's in-memory
// history so a forced compact has volume to reclaim. This mirrors the
// structural compact test's fixture technique.
func padSessionHistory(session *chat.Session) {
	for i := 0; i < 12; i++ {
		session.Messages = append(session.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("old question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("old answer ", 20)},
		)
	}
}

// TestContextSummaryIntegrationManualCompact drives the /compact path
// (Session.Compact) through the production wiring: the compacted preparation
// produces one summary request, the validated reply is stamped onto the
// durable checkpoint as SummaryMetadata, and the rendered summary message is
// appended to the live session history for the next request.
func TestContextSummaryIntegrationManualCompact(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), "first question", io.Discard); err != nil {
		t.Fatal(err)
	}
	padSessionHistory(session)

	if err := session.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(completer.summaryRequests()) == 0 {
		t.Fatal("manual compact sent no summary request to the completer")
	}
	if last := session.Messages[len(session.Messages)-1]; last.Name != "context-summary" && !strings.Contains(last.Content, "[host-injected context summary") {
		t.Fatalf("manual compact left %q as the last message, want the context summary", last.Content)
	}
	if active := loadActiveSnapshot(t, store, session); len(active.Metadata) == 0 {
		t.Fatal("manual compact persisted no summary metadata on the checkpoint")
	}
}

// TestContextSummaryIntegrationManualCompactDegrades pins the /compact
// never-fail rule end to end: an invalid summary reply keeps the compact
// successful, appends no summary message, and persists no metadata.
func TestContextSummaryIntegrationManualCompactDegrades(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{garbage: true}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUser(context.Background(), "first question", io.Discard); err != nil {
		t.Fatal(err)
	}
	padSessionHistory(session)

	if err := session.Compact(context.Background()); err != nil {
		t.Fatalf("manual compact failed on an invalid summary reply: %v", err)
	}
	if last := session.Messages[len(session.Messages)-1]; last.Name == "context-summary" || strings.Contains(last.Content, "[host-injected context summary") {
		t.Fatal("invalid summary reply still appended a context-summary message")
	}
	if active := loadActiveSnapshot(t, store, session); len(active.Metadata) != 0 {
		t.Fatal("invalid summary reply still persisted summary metadata")
	}
}
