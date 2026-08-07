package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// --- R1a: budget-derived per-result tool cap --------------------------------

// hugeResultTool returns a fixed oversized body so the test can prove the
// per-result context cap (not any per-call byte cap) is what protects the
// prompt budget.
type hugeResultTool struct {
	body string
}

func (t *hugeResultTool) Name() string               { return "big_read" }
func (t *hugeResultTool) Description() string        { return "returns a huge body" }
func (t *hugeResultTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *hugeResultTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:big"}
}

// ResultBudgetBytes declares the tool's honest output budget so the runtime
// dispatcher's runaway backstop sits above the body and passes it through
// whole: the derived per-result cap is what must bound it, not the transport.
func (t *hugeResultTool) ResultBudgetBytes() int { return 512 << 10 }

func (t *hugeResultTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.body, nil
}

// remainderRefPattern extracts the full content ref a truncation notice names.
var remainderRefPattern = regexp.MustCompile(`ref:output:[0-9a-f]{64}`)

// R1a: with the shipped uncapped config (MaxToolResultChars=0 and
// BatchResultBudgetBytes=0/off) a single oversized CURRENT-turn tool result
// (a 500KB read) used to flow into history whole and blow the 8000-token
// prompt budget on the next prepareStep, hard-aborting the turn with
// ErrPromptBudgetExceeded after the tool already ran (context planning never
// elides the current turn's result). The derived per-result cap must cut it
// via capToolResult - len <= (MaxContextTokens/4)*4 chars - with the spool
// content-ref notice so the FULL body stays recoverable through read_output,
// while a small result stays byte-identical, Role/ToolCallID pairing is
// preserved, and the loop CONTINUES.
func TestLoopCapsOversizedCurrentTurnToolResult(t *testing.T) {
	loop, spool, store, big, text := cappedResultLoop(t)
	if text != "read complete" {
		t.Fatalf("text=%q, want the second-step answer (loop continued past the huge result)", text)
	}

	capChars := (8000 / 4) * 4 // derived per-result cap in chars
	var bigMsg, smallMsg *provider.Message
	for i := range loop.Messages {
		switch {
		case loop.Messages[i].Role == provider.RoleTool && loop.Messages[i].ToolCallID == "1":
			bigMsg = &loop.Messages[i]
		case loop.Messages[i].Role == provider.RoleTool && loop.Messages[i].ToolCallID == "2":
			smallMsg = &loop.Messages[i]
		}
	}
	if bigMsg == nil {
		t.Fatal("truncated RoleTool missing from history")
	}
	if smallMsg == nil {
		t.Fatal("small RoleTool missing from history")
	}
	if len(bigMsg.Content) > capChars {
		t.Fatalf("result kept %d chars, over the derived cap %d", len(bigMsg.Content), capChars)
	}
	if !strings.Contains(bigMsg.Content, "... truncated: kept ") {
		t.Fatalf("truncated result missing the spool truncation notice: %q", trunc(bigMsg.Content, 200))
	}
	ref := remainderRefPattern.FindString(bigMsg.Content)
	if ref == "" {
		t.Fatalf("truncated result names no remainder ref: %q", trunc(bigMsg.Content, 200))
	}
	recovered, err := spool.Load(t.Context(), "session-r1a", ref)
	if err != nil {
		t.Fatalf("remainder ref %q not recoverable through read_output: %v", ref, err)
	}
	if string(recovered) != big {
		t.Fatalf("remainder recovered %d bytes, want the full %d-byte body", len(recovered), len(big))
	}
	if smallMsg.Content != "small body stays untouched" {
		t.Fatalf("small result was altered: %q", trunc(smallMsg.Content, 80))
	}
	if bigMsg.ToolCallID != "1" || bigMsg.Name != "big_read" {
		t.Fatalf("truncation changed tool pairing identity: id=%q name=%q", bigMsg.ToolCallID, bigMsg.Name)
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("pairing broken by truncation: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("spool holds %d bodies, want exactly 1 (only the truncated big result)", store.Len())
	}
}

// cappedResultLoop runs the R1a fixture: a first step that calls a huge tool
// and a small tool, with the shipped uncapped per-result config and an 8000
// token prompt budget. It returns the completed loop, the spool, the store,
// the big body, and the final assistant text.
func cappedResultLoop(t *testing.T) (loop *Loop, spool *remainder.Spool, store *remainder.MemoryStore, big, text string) {
	t.Helper()
	const small = "small body stays untouched"
	big = strings.Repeat("A", 500<<10)
	spool, store = testSpool(t)
	reg := tools.NewRegistry()
	reg.Register(&hugeResultTool{body: big})
	reg.Register(&fixedElisionTool{name: "small_echo", body: small})
	comp := &scriptCompleter{steps: []provider.Response{
		{ToolCalls: []provider.ToolCall{
			tc("1", "big_read", `{"path":"huge"}`),
			tc("2", "small_echo", `{}`),
		}, FinishReason: "tool_calls"},
		{Content: "read complete", FinishReason: "stop"},
	}}
	loop = &Loop{Completer: comp, Tools: reg}
	principal, binding := elisionPrincipalBinding(t)
	var err error
	text, err = loop.Run(context.Background(), "read the huge file", Options{
		Model: "model", MaxContextTokens: 8000, MaxSteps: 5,
		SessionID: "session-r1a", RemainderSpool: spool,
		// The context manager never elides the current turn's mandatory latest
		// tool unit, so without the derived cap the 500KB result makes the
		// planner reject the mandatory set with a prompt-budget error.
		PreparationManager: contextmgr.StructuralPreparationManager{},
		PreparationInput: contextmgr.PrepareInput{
			Budget: 8000, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
	})
	if err != nil {
		t.Fatalf("turn must continue after a capped tool result, got: %v", err)
	}
	return loop, spool, store, big, text
}

// --- R1b: visible notice after prompt-too-long compaction --------------------

// R1b: after a prompt-too-long rejection, the compaction that drops earlier
// tool results must be announced in the model-visible history, not only in an
// operator EventPrune. The retry request itself must carry the notice.
func TestAgentPromptTooLongCompactionLeavesVisibleNotice(t *testing.T) {
	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("%w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}
	var pruned []Event
	text, err := loop.Run(context.Background(), "final question", Options{
		Model: "model", MaxSteps: 5,
		OnEvent: func(e Event) {
			if e.Kind == EventPrune {
				pruned = append(pruned, e)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "recovered" {
		t.Fatalf("text=%q", text)
	}
	if len(pruned) != 1 {
		t.Fatalf("expected one prune event, got %d", len(pruned))
	}
	found := false
	for _, m := range loop.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, promptTooLongCompactNotice) {
			found = true
		}
	}
	if !found {
		t.Fatal("history missing the compaction notice after prompt-too-long retry")
	}
	inRetry := false
	for _, m := range comp.lastReq.Messages {
		if strings.Contains(m.Content, promptTooLongCompactNotice) {
			inRetry = true
		}
	}
	if !inRetry {
		t.Fatal("retry request did not carry the compaction notice")
	}
	if err := provider.ValidateToolPairing(loop.Messages); err != nil {
		t.Fatalf("pairing broken by the notice: %v", err)
	}
}

// --- R2: stale preparation after prompt-too-long retry -----------------------

// recordingPrep wraps CapturePreparation and records every Prepare input so a
// test can assert what history the manager saw after a prompt-too-long retry.
type recordingPrep struct {
	inputs  []contextmgr.PrepareInput
	discard int
}

func (p *recordingPrep) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.inputs = append(p.inputs, input)
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	return contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, false, "recording-prep")
}

func (p *recordingPrep) Discard(contextmgr.Preparation) { p.discard++ }

// R2: a prompt-too-long retry prunes l.Messages in place; the preparation
// recorded by prepareStep points at the rejected (never-sent) history. After a
// successful retry the loop must discard that stale preparation and re-Prepare
// on the pruned history, so commit fingerprints the same bytes the checkpoint
// holds. The retry path must exercise a configured PreparationManager (the
// old retry tests ran without one, which is why the staleness went untested).
func TestPromptTooLongRetryRefreshesPreparation(t *testing.T) {
	principal, binding := elisionPrincipalBinding(t)
	prep := &recordingPrep{}
	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("%w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}
	text, err := loop.Run(context.Background(), "final question", Options{
		Model: "model", MaxSteps: 5,
		PreparationManager: prep,
		PreparationInput: contextmgr.PrepareInput{
			Budget: 100_000, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "recovered" {
		t.Fatalf("text=%q", text)
	}
	if len(prep.inputs) < 2 {
		t.Fatalf("manager saw %d Prepare calls, want >=2 (re-prepare after retry)", len(prep.inputs))
	}
	reprepared := prep.inputs[len(prep.inputs)-1]
	// The re-prepare must have seen the PRUNED history: smaller than what the
	// original prepareStep saw, and carrying the compaction notice.
	if got, want := provider.MessagesTokens(reprepared.Messages), provider.MessagesTokens(prep.inputs[0].Messages); got >= want {
		t.Fatalf("re-prepare saw %d tokens, not smaller than the pre-retry %d (stale preparation)", got, want)
	}
	noticeInReprepare := false
	for _, m := range reprepared.Messages {
		if strings.Contains(m.Content, promptTooLongCompactNotice) {
			noticeInReprepare = true
		}
	}
	if !noticeInReprepare {
		t.Fatal("re-prepare did not see the pruned history with the compaction notice")
	}
	// The recorded preparation must match the pruned history the retry sent:
	// LastPreparation.Messages is the manager's clone of what it saw, and the
	// live history is that plus the final assistant answer.
	if !loop.HasPreparation {
		t.Fatal("successful retry left HasPreparation false")
	}
	if len(loop.Messages) != len(reprepared.Messages)+1 {
		t.Fatalf("live history has %d messages, want re-prepared %d plus the final answer",
			len(loop.Messages), len(reprepared.Messages))
	}
	if !reflect.DeepEqual(loop.LastPreparation.Messages, reprepared.Messages) {
		t.Fatalf("LastPreparation.Messages does not match what the re-prepare saw")
	}
	if !reflect.DeepEqual(loop.Messages[:len(loop.Messages)-1], reprepared.Messages) {
		t.Fatalf("live history diverged from the re-prepared history")
	}
	if prep.discard != 1 {
		t.Fatalf("stale preparation discarded %d times, want exactly 1", prep.discard)
	}
}

// R2 (no-manager branch): with no PreparationManager configured, a successful
// prompt-too-long retry must leave nothing stale to commit: HasPreparation
// false and LastPreparation zeroed. The no-manager path never records a
// preparation in prepareStep, so this is a defensive pin on the retry path not
// introducing one (a retry prunes l.Messages; a stale record would make
// commit fingerprint bytes the checkpoint does not hold).
func TestPromptTooLongRetryClearsStalePreparationWithoutManager(t *testing.T) {
	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("%w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}
	text, err := loop.Run(context.Background(), "final question", Options{Model: "model", MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	if text != "recovered" {
		t.Fatalf("text=%q", text)
	}
	if comp.calls != 2 {
		t.Fatalf("completer called %d times, want 2 (fail + retry)", comp.calls)
	}
	if loop.HasPreparation {
		t.Fatal("stale preparation survived a retry with no manager")
	}
	if !reflect.DeepEqual(loop.LastPreparation, contextmgr.Preparation{}) {
		t.Fatalf("LastPreparation not zeroed after retry: %+v", loop.LastPreparation)
	}
}
