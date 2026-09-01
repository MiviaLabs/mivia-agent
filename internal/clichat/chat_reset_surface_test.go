package clichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The reset says "discard the answer you have for this turn". It reached the
// chat-sync projector and stopped there, so the two NDJSON surfaces - the
// local --json stream and the hub relay - showed the rejected attempt with
// its replacement appended after it. These tests hold both surfaces to the
// contract the projector already keeps.

// jsonLines drives the real --json event callback and decodes what it wrote.
func jsonLines(t *testing.T, evs ...agent.Event) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	cb := jsonTurnEventCallback(&buf)
	for _, ev := range evs {
		cb(ev)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %v (%q)", err, line)
		}
		out = append(out, m)
	}
	return out
}

// TestJSONModeAnnouncesTheDiscard covers the local --json consumer, which
// accumulates the answer from "chunk" lines and has no other way to learn
// that the chunks it holds belong to an attempt that no longer exists.
func TestJSONModeAnnouncesTheDiscard(t *testing.T) {
	lines := jsonLines(t, agent.Event{
		Kind: agent.EventAssistantReset, Detail: "schema_retry",
	})

	for _, line := range lines {
		if line["type"] == "assistant_reset" {
			if line["message"] != "schema_retry" {
				t.Errorf("the reason did not survive: %v", line)
			}
			return
		}
	}
	t.Fatalf("--json dropped the reset, so the answer is sent twice: %v", typesOf(lines))
}

// TestJSONModeAnnouncesASubagentRun covers the run-level opening signal. A
// --json consumer otherwise first hears of a subagent when it calls a tool,
// and a run that only thinks and answers is never announced at all.
func TestJSONModeAnnouncesASubagentRun(t *testing.T) {
	lines := jsonLines(t, agent.Event{
		Kind: agent.EventSubagentBegin, Name: "reviewer", Detail: "review the diff",
		Origin: agent.EventOrigin{TaskID: "task-1", Agent: "reviewer", Depth: 1},
	})

	for _, line := range lines {
		if line["type"] != "subagent_begin" {
			continue
		}
		if line["origin_task_id"] != "task-1" {
			t.Errorf("the run cannot be told from its siblings: %v", line)
		}
		if line["input"] != "review the diff" {
			t.Errorf("the task the run was given did not survive: %v", line)
		}
		return
	}
	t.Fatalf("--json dropped subagent_begin: %v", typesOf(lines))
}

// TestRelayAnnouncesTheDiscardOnTheRootPath is the cross-process mirror.
func TestRelayAnnouncesTheDiscardOnTheRootPath(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		rootEv(events.KindAssistantReset, "", "schema_retry"),
	)

	if !containsString(typesOf(lines), "external_assistant_reset") {
		t.Fatalf("the relay dropped the reset: %v", typesOf(lines))
	}
}

// TestResetLetsTheReplacementAggregateThrough is the defect the line alone
// would not fix.
//
// The relay suppresses a turn-end aggregate for a run that already streamed,
// so the replacement answer - which arrives as an aggregate when the retry
// does not stream - was dropped by the flag the FIRST attempt set. The
// consumer was left with a discard and no answer at all, which is worse than
// the duplicate it replaced.
func TestResetLetsTheReplacementAggregateThrough(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		rootEv(events.KindAssistant, "the rejected reply", "delta"),
		rootEv(events.KindAssistantReset, "", "schema_retry"),
		rootEv(events.KindAssistant, "the accepted reply", ""),
	)

	for _, line := range lines {
		if line["text"] == "the accepted reply" {
			return
		}
	}
	t.Fatalf("the replacement answer was suppressed by the discarded attempt's "+
		"streamed flag: %v", typesOf(lines))
}

// TestRelayKeepsASubagentResetOffTheRootPath holds the reset to the same rule
// as every other subagent kind: it must not be able to clear the ROOT run's
// state, or one subagent's retry drops the root agent's own answer.
func TestRelayKeepsASubagentResetOffTheRootPath(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		rootEv(events.KindAssistant, "root chunk", "delta"),
		attributed(events.KindAssistantReset, "", "schema_retry"),
		rootEv(events.KindAssistant, "root answer", ""),
	)

	got := typesOf(lines)
	if !containsString(got, "external_subagent_assistant_reset") {
		t.Errorf("a subagent's reset never reached the relay: %v", got)
	}
	for _, line := range lines {
		if line["text"] == "root answer" {
			t.Fatalf("a subagent's retry replayed the ROOT agent's answer: %v", got)
		}
	}
}
