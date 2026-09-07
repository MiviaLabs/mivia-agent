// Coverage targets for the SDK adoption rows' branches the
// happy-path adapter tests do not reach: the compaction error wrap,
// the audit closure body, the bookkeeping Prepare pass, the two
// post-run failure returns, and the telemetry writer's latch.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktrace "github.com/MiviaLabs/mivia-ai-sdk/trace"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// plainSDKCompleter is an SDK completer with NO TokenEstimator
// capability. The SDK compaction triple needs one, so adoption must
// fail closed on it.
type plainSDKCompleter struct{}

func (plainSDKCompleter) Name() string { return "plain" }

func (plainSDKCompleter) Chat(context.Context, sdkshape.Request) (sdkshape.Response, error) {
	return sdkshape.Response{}, nil
}

func (plainSDKCompleter) ChatStream(context.Context, sdkshape.Request) (<-chan sdkshape.Chunk, error) {
	return nil, nil
}

// recordingPreparationManager keeps every PrepareInput it saw and
// returns a fixed outcome, so a test can assert what the caller
// priced.
type recordingPreparationManager struct {
	inputs []contextmgr.PrepareInput
	out    contextmgr.Preparation
	err    error
}

func (m *recordingPreparationManager) Prepare(_ context.Context, in contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	m.inputs = append(m.inputs, in)
	if m.err != nil {
		return contextmgr.Preparation{}, m.err
	}
	return m.out, nil
}

func (m *recordingPreparationManager) Discard(contextmgr.Preparation) {}

// TestAdoptSDKRowsWrapsCompactionError pins the one error path of the
// adoption table: a turn that adopts the SDK compaction triple over a
// completer with no TokenEstimator fails closed, and adoptSDKRows
// names the failing row instead of returning the bare sentinel.
func TestAdoptSDKRowsWrapsCompactionError(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}}
	opts := Options{
		MaxContextTokens: 1000,
		SummaryConfig:    SummaryConfig{Summarizer: &contextmgr.Summarizer{}},
	}
	var out sdkagentloop.Options
	err := adoptSDKRows(l, &out, opts, plainSDKCompleter{}, newSDKTurnState())
	if err == nil {
		t.Fatal("adoptSDKRows = nil error over a completer with no TokenEstimator; the SDK window would size from nothing")
	}
	if !errors.Is(err, sdkagentloop.ErrNoTokenEstimator) {
		t.Fatalf("err = %v, want it to wrap ErrNoTokenEstimator", err)
	}
	if !strings.Contains(err.Error(), "adopt SDK compaction") {
		t.Fatalf("err = %v, want it to name the failing adoption row", err)
	}
	if out.Compaction.Window != nil {
		t.Fatal("Window wired although adoption failed; a half-adopted triple must not reach the SDK")
	}
}

// TestAdoptSDKRowsSucceedsWithoutCompaction pins the negative half of
// the same gate: a turn that does not adopt the triple never reaches
// the estimator requirement, so the same estimator-free completer is
// accepted.
func TestAdoptSDKRowsSucceedsWithoutCompaction(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}}
	var out sdkagentloop.Options
	if err := adoptSDKRows(l, &out, Options{SessionID: "s"}, plainSDKCompleter{}, newSDKTurnState()); err != nil {
		t.Fatalf("adoptSDKRows = %v, want nil with no compaction adopted", err)
	}
}

// TestAdoptSDKAuditForwardsRecordToSink pins the Audit row's closure
// body: the hook must hand the SDK record to the JSONL sink and
// return nil, because the SDK turns an Audit error into a hard run
// failure.
func TestAdoptSDKAuditForwardsRecordToSink(t *testing.T) {
	auditDumpDisabled.Store(false)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)

	var out sdkagentloop.Options
	adoptSDKAudit(&out, Options{SessionID: "S-audit"})
	if out.Audit == nil {
		t.Fatal("Audit = nil with an audit directory named")
	}
	rec := sdkagentloop.AuditRecord{
		Iteration: 7,
		Kind:      sdkagentloop.AuditKindCompletion,
		Request:   sdkshape.Request{Model: "audited-model"},
		Response:  sdkshape.Response{FinishReason: "stop"},
	}
	if err := out.Audit(context.Background(), rec); err != nil {
		t.Fatalf("Audit hook = %v, want nil: a returned error hard-fails the user's turn", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "sdkloop-"+auditDumpFileName("S-audit")))
	if err != nil {
		t.Fatalf("read sdkloop dump: %v", err)
	}
	var payload map[string]any
	line := strings.TrimSpace(strings.Split(string(raw), "\n")[0])
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("Unmarshal %q: %v", line, err)
	}
	if payload["session_id"] != "S-audit" {
		t.Errorf("session_id = %v, want S-audit", payload["session_id"])
	}
	if n, _ := payload["iteration"].(float64); n != 7 {
		t.Errorf("iteration = %v, want 7; the record did not reach the sink", payload["iteration"])
	}
	if payload["model"] != "audited-model" {
		t.Errorf("model = %v, want audited-model", payload["model"])
	}
}

// TestSDKCompactionObserverRunsBookkeepingPrepare pins the observer's
// PreparationManager branch. It must price the LIVE advertised tool
// surface, run with an unbounded Budget so the bookkeeping pass can
// never itself compact, record the outcome, capture omitted evidence,
// and report the exact per-iteration history.
func TestSDKCompactionObserverRunsBookkeepingPrepare(t *testing.T) {
	kept := []provider.Message{{Role: provider.RoleUser, Content: "kept"}}
	pm := &recordingPreparationManager{out: contextmgr.Preparation{
		Messages:       kept,
		Compacted:      true,
		ElidedMessages: 2,
		ElidedBytes:    64,
	}}
	l := &Loop{
		Completer: &fakeCompleter{name: "test"},
		TurnState: contextmgr.NewTurnState(),
	}
	advertised := []provider.ToolSpec{{"type": "function", "function": map[string]any{"name": "advertised_tool"}}}
	turn := newSDKTurnState()
	turn.setAdvertised(advertised)

	var history [][]provider.Message
	opts := Options{
		MaxContextTokens:      1000,
		PreparationManager:    pm,
		ObserveRequestHistory: func(msgs []provider.Message) { history = append(history, msgs) },
	}
	observe := sdkCompactionObserver(l, opts, turn)
	req := sdkshape.Request{Messages: []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "one"},
		{Role: sdkshape.RoleAssistant, Content: "two"},
	}}
	if err := observe(context.Background(), req); err != nil {
		t.Fatalf("ObserveRequest = %v, want nil", err)
	}

	if len(pm.inputs) != 1 {
		t.Fatalf("PreparationManager saw %d Prepare calls, want 1", len(pm.inputs))
	}
	in := pm.inputs[0]
	if in.Budget != math.MaxInt {
		t.Fatalf("Prepare Budget = %d, want math.MaxInt: the bookkeeping pass must never cross its own compaction trigger", in.Budget)
	}
	if len(in.Tools) != 1 || !reflect.DeepEqual(in.Tools[0], advertised[0]) {
		t.Fatalf("Prepare Tools = %+v, want the turn's live advertised surface", in.Tools)
	}
	if len(in.Messages) != 2 || in.Messages[0].Content != "one" {
		t.Fatalf("Prepare Messages = %+v, want the SDK request converted to CLI shape", in.Messages)
	}
	if l.LastPreparation.ElidedMessages != 2 || l.LastPreparation.ElidedBytes != 64 {
		t.Fatalf("LastPreparation = %+v, want the manager's outcome recorded", l.LastPreparation)
	}
	if len(l.preCompactSource) != 2 {
		t.Fatalf("preCompactSource = %+v, want the pre-compaction history captured as evidence", l.preCompactSource)
	}
	if len(history) != 1 || len(history[0]) != 2 {
		t.Fatalf("ObserveRequestHistory saw %+v, want one call with the iteration's two messages", history)
	}
}

// TestSDKCompactionObserverPropagatesPrepareError pins the observer's
// failure path: a Prepare error is parked on the Loop for the session
// recovery branch and returned unwrapped, so the SDK adds its single
// iteration frame and nothing else runs on that iteration.
func TestSDKCompactionObserverPropagatesPrepareError(t *testing.T) {
	want := errors.New("prepare exploded")
	pm := &recordingPreparationManager{err: want}
	l := &Loop{Completer: &fakeCompleter{name: "test"}, TurnState: contextmgr.NewTurnState()}
	var historyCalls int
	opts := Options{
		MaxContextTokens:      1000,
		PreparationManager:    pm,
		ObserveRequestHistory: func([]provider.Message) { historyCalls++ },
	}
	observe := sdkCompactionObserver(l, opts, newSDKTurnState())
	req := sdkshape.Request{Messages: []sdkshape.Message{{Role: sdkshape.RoleUser, Content: "one"}}}
	err := observe(context.Background(), req)
	if !errors.Is(err, want) {
		t.Fatalf("ObserveRequest = %v, want the manager's error unwrapped", err)
	}
	if !errors.Is(l.PreparationErr, want) {
		t.Fatalf("l.PreparationErr = %v, want the manager's error parked for the recovery branch", l.PreparationErr)
	}
	if historyCalls != 0 {
		t.Fatalf("ObserveRequestHistory ran %d times after a Prepare failure, want 0", historyCalls)
	}
	if l.LastPreparation.Compacted {
		t.Fatal("a failed Prepare recorded a preparation; HasPreparation must stay false on this path")
	}
}

// TestFinishAgentLoopTurnReturnsBridgeError pins the surface-bridge
// failure path: a mid-run registry rotation error recorded on the turn
// state fails the turn, even though the SDK run itself returned no
// error.
func TestFinishAgentLoopTurnReturnsBridgeError(t *testing.T) {
	want := errors.New("rotation failed")
	turn := newSDKTurnState()
	turn.recordBridgeError(want)
	res := sdkagentloop.Result{Final: sdkshape.Message{Role: sdkshape.RoleAssistant, Content: "text the model produced"}}
	got, err := finishAgentLoopTurn(context.Background(), &Loop{}, Options{}, turn, res, nil, nil)
	if !errors.Is(err, want) {
		t.Fatalf("finishAgentLoopTurn = %v, want the recorded bridge error", err)
	}
	if got.Final.Content != res.Final.Content {
		t.Fatalf("Final = %q, want the partial result carried through", got.Final.Content)
	}
}

// TestFinishAgentLoopTurnFailsOnRepeatedToolFailureStop pins the
// failure-spiral conversion: the SDK reports the bound as a graceful
// stop with a nil error, and returning that unchanged let a turn that
// never ran its work report success with stale text.
func TestFinishAgentLoopTurnFailsOnRepeatedToolFailureStop(t *testing.T) {
	res := sdkagentloop.Result{
		Stop:       sdkagentloop.StopRepeatedToolFailures,
		Iterations: 4,
		Final:      sdkshape.Message{Role: sdkshape.RoleAssistant, Content: "stale text"},
	}
	_, err := finishAgentLoopTurn(context.Background(), &Loop{}, Options{}, newSDKTurnState(), res, nil, nil)
	if err == nil {
		t.Fatal("finishAgentLoopTurn = nil error on StopRepeatedToolFailures; the turn would report success with stale text")
	}
	if !strings.Contains(err.Error(), "failure spiral bound") {
		t.Fatalf("err = %v, want it to name the failure-spiral bound", err)
	}
	if !strings.Contains(err.Error(), "4 iterations") {
		t.Fatalf("err = %v, want it to carry the iteration count", err)
	}
}

// TestRecordSDKTurnTelemetryHonorsDisableLatch pins the shared latch:
// once an audit write failed, the telemetry writer must stay off
// instead of retrying the same target on every turn.
func TestRecordSDKTurnTelemetryHonorsDisableLatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvProviderAuditDir, dir)
	auditDumpDisabled.Store(true)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })

	turn := newSDKTurnState()
	turn.setTracer(sdktrace.New())
	recordSDKTurnTelemetry(Options{SessionID: "S-latched"}, turn)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %v with the dump latched off; the latch must be terminal", entries)
	}
}

// TestRecordSDKTurnTelemetryLatchesOffOnWriteFailure pins the
// fail-safe: an unwritable audit target must latch the dump off
// rather than stay quiet and retry mkdir on every turn for the rest
// of the process.
func TestRecordSDKTurnTelemetryLatchesOffOnWriteFailure(t *testing.T) {
	auditDumpDisabled.Store(false)
	t.Cleanup(func() { auditDumpDisabled.Store(false) })
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv(EnvProviderAuditDir, filepath.Join(blocked, "sub"))

	turn := newSDKTurnState()
	turn.setTracer(sdktrace.New())
	recordSDKTurnTelemetry(Options{SessionID: "S-unwritable"}, turn)

	if !auditDumpDisabled.Load() {
		t.Fatal("a failed telemetry target must latch off")
	}
}

// TestSDKTurnStateSettersTolerateNilReceiver pins the adoption rows'
// nil guard: adoptSDKTracer and adoptSDKUsage park state through
// these setters, and a turn that never built must not panic the run.
func TestSDKTurnStateSettersTolerateNilReceiver(t *testing.T) {
	var nilTurn *sdkTurnState
	nilTurn.setTracer(sdktrace.New())
	nilTurn.setUsage(nil)
	// Reaching this line is the assertion: either setter would panic
	// on a nil receiver without its guard.
	live := newSDKTurnState()
	tracer := sdktrace.New()
	live.setTracer(tracer)
	if live.tracer != tracer {
		t.Fatal("setTracer did not park the tracer on a live turn state")
	}
}
