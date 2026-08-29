package cliorchestrate

// Task-result producer conformance gate (DC-11, identity and comparison).
//
// A task has two identity forms: the FULL namespaced TaskID the ledger and
// its event/message index key by ("call_1:t1"), and the stripped
// model-visible RawID ("t1") that producers report. A producer lookup keyed
// by the wrong form compiles and misses silently - the recovered path
// shipped exactly that bug twice (tool_calls_ref, then task messages;
// commit b346f9d7). A per-producer test answers "does this one work?";
// only a table over EVERY producer answers "do they all agree?", which is
// the question that keeps being wrong in this repo
// (.agents/memories/sibling-implementations-drift.md).
//
// The shared fixture is one dispatch-namespaced task whose snapshot row
// records an output ref and a tool-calls ref, and whose run ledger records
// one finding message under the FULL task id. Every producer of a
// model-visible task result must join this table and surface ALL recorded
// attachments on its row, whatever identity form it reports as task_id.
// A new producer that does not join the table is visible here by its
// absence from the producers list below.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// conformanceFixture is the one namespaced-task world every producer runs in.
type conformanceFixture struct {
	repo         ledger.LedgerRepository
	tasks        []ledger.TaskSnapshot
	liveResult   subagents.Result
	outputRef    string
	toolCallsRef string
}

// producerRow is the normalized view of one produced task result: the
// attachments the contract requires, independent of the producer's own
// envelope type.
type producerRow struct {
	TaskID       string
	OutputRef    string
	ToolCallsRef string
	MessageIDs   []string
}

func newConformanceFixture(t *testing.T) conformanceFixture {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()
	const runID = "r-conformance"
	const fullID = "call_1:t1"

	outputBody := []byte(`"final report"`)
	outputRef := ledger.Reference(ledger.RefKindOutput, outputBody)
	if err := repo.StoreContent(ctx, outputRef, outputBody); err != nil {
		t.Fatalf("StoreContent(output): %v", err)
	}
	toolCallsRef := storeToolCallSteps(t, repo, []subagents.ToolCallStep{
		{ToolCallID: "call-1", Name: "grep", Kind: "start", Input: "pattern"},
		{ToolCallID: "call-1", Name: "grep", Kind: "end", Output: "1 match"},
	})

	tasks := []ledger.TaskSnapshot{{
		RunID: runID, TaskID: fullID, RawID: "t1",
		Status:    string(ledger.TaskStatusCompleted),
		OutputRef: outputRef, ToolCallsRef: toolCallsRef,
	}}
	if err := repo.CreateRun(ctx, "", ledger.RunSnapshot{
		RunID: runID, Status: ledger.RunStatusRunning, Tasks: tasks,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	payload, err := json.Marshal(agentmsg.LifecyclePayload{
		MessageID: "msg-1", Kind: agentmsg.KindFinding, Synopsis: "found it",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := repo.AppendEvent(ctx, ledger.LifecycleEvent{
		ID: "evt-1", RunID: runID, Kind: coordinator.LifecycleKindTaskMessage,
		TaskID: fullID, Payload: payload, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	return conformanceFixture{
		repo:  repo,
		tasks: tasks,
		liveResult: subagents.Result{
			TaskID: fullID, Status: "completed", Output: outputBody,
		},
		outputRef:    outputRef,
		toolCallsRef: toolCallsRef,
	}
}

func rowFromModelResult(r modelTaskResult) producerRow {
	row := producerRow{TaskID: r.TaskID, OutputRef: r.OutputRef, ToolCallsRef: r.ToolCallsRef}
	for _, m := range r.Messages {
		row.MessageIDs = append(row.MessageIDs, m.MessageID)
	}
	return row
}

func rowFromDispatchResult(r dispatchTaskResult) producerRow {
	row := producerRow{TaskID: r.TaskID, OutputRef: r.OutputRef, ToolCallsRef: r.ToolCallsRef}
	for _, m := range r.Messages {
		row.MessageIDs = append(row.MessageIDs, m.MessageID)
	}
	return row
}

// taskResultProducer is one row of the conformance table: a named producer
// of model-visible task results. messagesApply is false only for the
// nil-repo entry point, which has no ledger to read message events from;
// snapshot-recorded refs must still surface there.
type taskResultProducer struct {
	name          string
	messagesApply bool
	run           func(t *testing.T, fx conformanceFixture) producerRow
}

// taskResultProducers enumerates EVERY producer. A new producer joins this
// table or its absence is visible here.
func taskResultProducers() []taskResultProducer {
	return []taskResultProducer{
		{
			name:          "dispatch_tasks encodeResults",
			messagesApply: true,
			run: func(t *testing.T, fx conformanceFixture) producerRow {
				tool := &dispatchTasksTool{repo: fx.repo, cfg: config.SubagentConfig{InlineOutputBytes: 4096}}
				raw := tool.encodeResults(fx.tasks, []subagents.Result{fx.liveResult})
				var decoded []dispatchTaskResult
				if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
					t.Fatalf("unmarshal %q: %v", raw, err)
				}
				if len(decoded) != 1 {
					t.Fatalf("decoded len = %d, want 1", len(decoded))
				}
				return rowFromDispatchResult(decoded[0])
			},
		},
		{
			name:          "ModelTaskResultsWithRepo live",
			messagesApply: true,
			run: func(t *testing.T, fx conformanceFixture) producerRow {
				got := ModelTaskResultsWithRepo(fx.repo, fx.tasks, []subagents.Result{fx.liveResult}, 4096)
				if len(got) != 1 {
					t.Fatalf("got len = %d, want 1", len(got))
				}
				return rowFromModelResult(got[0])
			},
		},
		{
			name:          "ModelTaskResults nil repo",
			messagesApply: false,
			run: func(t *testing.T, fx conformanceFixture) producerRow {
				got := ModelTaskResults(fx.tasks, []subagents.Result{fx.liveResult}, 4096)
				if len(got) != 1 {
					t.Fatalf("got len = %d, want 1", len(got))
				}
				return rowFromModelResult(got[0])
			},
		},
		{
			name:          "RunTaskResultsWithRepo recovered",
			messagesApply: true,
			run: func(t *testing.T, fx conformanceFixture) producerRow {
				result := &coordinator.RunResult{
					Snapshot: ledger.RunSnapshot{RunID: fx.tasks[0].RunID, Tasks: fx.tasks},
					Results:  []subagents.Result{{TaskID: fx.tasks[0].TaskID, Status: "completed"}},
				}
				result.Results[0].Provenance.Kind = "recovered"
				got := RunTaskResultsWithRepo(fx.repo, result, 4096)
				if len(got) != 1 {
					t.Fatalf("got len = %d, want 1", len(got))
				}
				return rowFromModelResult(got[0])
			},
		},
		{
			name:          "joinSalvageEnvelope partial join",
			messagesApply: true,
			run: func(t *testing.T, fx conformanceFixture) producerRow {
				salvaged := &coordinator.RunResult{
					Snapshot: ledger.RunSnapshot{RunID: fx.tasks[0].RunID, Tasks: fx.tasks},
				}
				raw := joinSalvageEnvelope(fx.repo, nil, nil, salvaged, nil, false)
				var envelope struct {
					TaskResults []modelTaskResult `json:"task_results"`
				}
				if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
					t.Fatalf("unmarshal %q: %v", raw, err)
				}
				if len(envelope.TaskResults) != 1 {
					t.Fatalf("task_results len = %d, want 1", len(envelope.TaskResults))
				}
				return rowFromModelResult(envelope.TaskResults[0])
			},
		},
	}
}

// TestTaskResultProducerConformance holds every model-visible task-result
// producer to the same attachment contract against one namespaced task.
func TestTaskResultProducerConformance(t *testing.T) {
	for _, p := range taskResultProducers() {
		t.Run(p.name, func(t *testing.T) {
			fx := newConformanceFixture(t)
			row := p.run(t, fx)

			if row.TaskID == "" {
				t.Fatal("produced row reports no task id")
			}
			if row.OutputRef != fx.outputRef {
				t.Fatalf("OutputRef = %q, want the snapshot-recorded %q", row.OutputRef, fx.outputRef)
			}
			if row.ToolCallsRef != fx.toolCallsRef {
				t.Fatalf("ToolCallsRef = %q, want the snapshot-recorded %q", row.ToolCallsRef, fx.toolCallsRef)
			}
			if p.messagesApply {
				if len(row.MessageIDs) != 1 || row.MessageIDs[0] != "msg-1" {
					t.Fatalf("MessageIDs = %v, want [msg-1] (message recorded under the FULL task id must attach)", row.MessageIDs)
				}
			}
			// INV-AG-10: every reference this producer handed the model
			// must resolve.
			for _, ref := range []string{row.OutputRef, row.ToolCallsRef} {
				if _, err := fx.repo.LoadContent(context.Background(), ref); err != nil {
					t.Fatalf("produced ref %q does not resolve: %v", ref, err)
				}
			}
		})
	}
}
