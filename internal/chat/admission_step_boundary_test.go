package chat

// Wave 2 RED tests (task B): the step-boundary admission publication.
//
// w2a (PublishPendingAdmissionAtStepBoundary) publishes a stage owned by the
// CURRENT turn at a step boundary, mid-turn, before the turn's commit. The
// directly-testable scenarios (1, 3, 4, 5, 6) fail for their own reasons while
// w2a is absent; the end-to-end turn scenarios (2, 7) stay RED until w2d wires
// the hook into sendAgent with the turn-token re-capture.

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestStepBoundaryPublishMakesStagedToolLive: a stage owned by the current turn
// becomes callable the moment the step boundary publishes - it lands in the
// live registry (s.Tools) the loop runs on, and the pending stage is gone.
func TestStepBoundaryPublishMakesStagedToolLive(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	if _, err := sess.StageToolAdmission([]string{"grep"}, sess.turnID); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("the step boundary refused the current turn's own stage")
	}
	names := make([]string, 0, len(sess.Tools.OpenAITools()))
	for _, spec := range sess.Tools.OpenAITools() {
		names = append(names, spec["function"].(map[string]any)["name"].(string))
	}
	if !slices.Contains(names, "grep") {
		t.Fatalf("live registry = %v, want the staged grep admitted at the step boundary", names)
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a published stage must not remain pending")
	}
}

// TestStepBoundaryPublishDoesNotFenceOwnTurn (RED until w2d): a turn that
// publishes a staged admission at a step boundary must still be able to commit
// its own history. The publication bumps the operation fence
// (TryPublishAgentSurface -> invalidateLocked), and without the token
// re-capture w2d adds in sendAgent the commit runs under a stale token and is
// rejected with ErrStaleOperation - the turn's history is silently dropped.
func TestStepBoundaryPublishDoesNotFenceOwnTurn(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)

	turn, done, err := sess.beginAgentTurn("question", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, err := sess.StageToolAdmission([]string{"grep"}, turn.myTurn); err != nil {
		t.Fatalf("stage mid-turn: %v", err)
	}
	if !sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("the step boundary refused the executing turn's stage")
	}

	// The turn's own commit, the exact shape sendAgent uses once w2d lands: the
	// pre-publication token is stale (the publish bumped the fence) and must be
	// refused - pinning that the fence actually moved - while the re-captured
	// token (commitTurnToken) must commit under the post-publication fence.
	turnMsgs := append(cloneContextMessages(turn.messages),
		provider.Message{Role: provider.RoleUser, Content: "question"},
		provider.Message{Role: provider.RoleAssistant, Content: "turn answer"},
	)
	if err := sess.commitPreparedTurn(turnMsgs, turn.token, nil); !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("the pre-publication token must be fenced out by the step-boundary publication, got %v", err)
	}
	if err := sess.commitPreparedTurn(turnMsgs, sess.commitTurnToken(turn.myTurn, turn.token), nil); err != nil {
		t.Fatalf("a step-boundary publication fenced the turn out of its own commit: %v (want no ErrStaleOperation)", err)
	}
	if blob := historyBlob(sess); !strings.Contains(blob, "turn answer") {
		t.Fatalf("turn history was not adopted after the step-boundary publication: %s", blob)
	}
}

// TestStepBoundaryPublishDeferredWhileSiblingActive (R2-1): with a sibling turn
// in flight the step boundary must not publish - the old dispatcher may still
// be executing. The stage stays pending for a quieter boundary.
func TestStepBoundaryPublishDeferredWhileSiblingActive(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 1); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 2
	sess.mu.Unlock()
	if sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("published at a step boundary while a sibling turn was active")
	}
	if widener.count() != 0 {
		t.Fatal("the widener was asked to rebuild while a sibling turn was active")
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the deferred stage must stay pending, not be dropped")
	}
}

// TestStepBoundaryPublishSupersededTurnRejected: a turn superseded by a
// force-send can never publish its stage at a step boundary - the publication
// is fenced to the current turn (RequireTurnID mismatch) and returns false.
func TestStepBoundaryPublishSupersededTurnRejected(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 2); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// A force-sent turn supersedes the staging turn.
	sess.mu.Lock()
	sess.turnID = 3
	sess.activeTurns = 1
	sess.mu.Unlock()
	if sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("a superseded turn's stage published at a step boundary")
	}
	if widener.count() != 0 {
		t.Fatal("the widener was asked to rebuild for a superseded turn")
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the superseded turn's stage must stay pending for the superseding turn")
	}
}

// TestConsecutiveStepBoundaryPublishesCountAgainstBudget: each step-boundary
// publication is one batch and charges the publication bound exactly once,
// mirroring the existing counter semantics.
func TestConsecutiveStepBoundaryPublishesCountAgainstBudget(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()

	if _, err := sess.StageToolAdmission([]string{"grep"}, sess.turnID); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	if !sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("first step-boundary publication refused")
	}
	if _, err := sess.StageToolAdmission([]string{"glob"}, sess.turnID); err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	if !sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("second step-boundary publication refused")
	}
	sess.mu.RLock()
	published := sess.admissionPublications
	sess.mu.RUnlock()
	if published != 2 {
		t.Fatalf("admissionPublications = %d, want 2 for two consecutive step-boundary batches", published)
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep", "glob"}) {
		t.Fatalf("admitted = %v, want both batches", got)
	}
}

// TestStepBoundaryPublishSkipsMessageRewrite: a step-boundary publication is
// mid-turn, where s.Messages is the loop's history until the turn commit. The
// system prompt is byte-identical across admissions, so the rewrite is
// redundant, and the memory frame must not be rewritten mid-turn. The messages
// must be byte-identical before and after publication (SkipMessageRewrite).
func TestStepBoundaryPublishSkipsMessageRewrite(t *testing.T) {
	sess := newAdmissionSession(t)
	sess.mu.Lock()
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys-prompt"},
		{Role: provider.RoleUser, Content: "memory-frame"},
		{Role: provider.RoleUser, Content: "question"},
	}
	sess.mu.Unlock()
	widener := &recordingWidener{publish: func(req AgentSurfacePublication) (bool, error) {
		return sess.TryPublishAgentSurface(req), nil
	}}
	sess.SetSurfaceWidener(widener.fn)
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()

	before := sess.MessagesCopy()
	if _, err := sess.StageToolAdmission([]string{"grep"}, sess.turnID); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("step-boundary publication refused")
	}
	after := sess.MessagesCopy()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("step-boundary publication rewrote the turn history (SkipMessageRewrite violated):\nbefore: %+v\nafter:  %+v", before, after)
	}
}

// TestErroredTurnKeepsExecutedMidTurnAdmission (RED until w2d): a turn that
// published a staged admission at a step boundary then ERRORS keeps the
// admission live - pinned semantics, like any tool side effect. The drop paths
// in finishAgentTurn apply only to stages never published.
func TestErroredTurnKeepsExecutedMidTurnAdmission(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{err: errors.New("provider down")})
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)

	turn, done, err := sess.beginAgentTurn("question", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, err := sess.StageToolAdmission([]string{"grep"}, turn.myTurn); err != nil {
		t.Fatalf("stage mid-turn: %v", err)
	}
	if !sess.PublishPendingAdmissionAtStepBoundary() {
		t.Fatal("the step boundary refused the executing turn's stage")
	}

	loop := &agent.Loop{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "question"},
	}}
	turnErr := errors.New("provider down")
	ctx := context.Background()
	// The plain path (no context manager) so the drop happens in
	// finishAgentTurn's error branch.
	if err := sess.finishAgentTurn(ctx, loop, sess.Tools, "question", "question",
		sess.commitTurnToken(turn.myTurn, turn.token), nil, contextTurnConfig{}, turnErr); err != nil {
		t.Fatalf("finishAgentTurn: %v", err)
	}

	names := make([]string, 0, len(sess.Tools.OpenAITools()))
	for _, spec := range sess.Tools.OpenAITools() {
		names = append(names, spec["function"].(map[string]any)["name"].(string))
	}
	if !slices.Contains(names, "grep") {
		t.Fatalf("an errored turn rolled back its executed mid-turn admission; live registry = %v", names)
	}
	if got := sess.AdmittedTools(); !slices.Contains(got, "grep") {
		t.Fatalf("admitted = %v, want the published grep to survive the errored turn", got)
	}
}

var _ = tools.MaxAdmissionPublications // keep the tools import if bounds change
