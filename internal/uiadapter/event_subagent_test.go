package uiadapter_test

// Translate-event coverage for the subagent kinds, split out of
// event_test.go to keep that file under the soft file-size cap. The shared
// mappingCase/runMappingCases helpers live in event_test.go.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestTranslateEvent_SubagentLifecycle covers the subagent start/end
// kinds. EventSubagentStart/End reuse the tool.start/tool.end body shape
// so the UI shows the row alongside other tool calls (attribution rides
// on the input Origin). EventSubagentDone has its own test below.
func TestTranslateEvent_SubagentLifecycle(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "subagent_start_maps_to_tool.start",
			ev: agent.Event{
				Kind: agent.EventSubagentStart, ToolCallID: "tc-1", Name: "delegate",
				Detail: "running", Input: `{"task":"audit"}`,
				Origin: agent.EventOrigin{
					TaskID: "wft-1", Agent: "audit", Depth: 1,
					TaskDescription: "audit the diff",
				},
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolStart,
				Body: uievent.ToolStartBody{
					ToolCallID: "tc-1", Name: "delegate",
					Args: map[string]any{"task": "audit"},
				},
			}},
		},
		{
			name: "subagent_end_maps_to_tool.end",
			ev: agent.Event{
				Kind: agent.EventSubagentEnd, ToolCallID: "tc-1", Name: "delegate",
				Detail: "completed", Output: "clean",
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolEnd,
				Body: uievent.ToolEndBody{
					ToolCallID: "tc-1", Name: "delegate", OK: true,
					Result: "clean",
				},
			}},
		},
	})
}

// TestTranslateEvent_SubagentHeartbeat covers EventSubagentHeartbeat: it
// maps to a keyed tool.output progress update so blocking dispatch_tasks
// rows show live liveness, and the parsed step count rides Progress.Step.
// Heartbeats WITHOUT a TaskID stay dropped because no row owns them.
func TestTranslateEvent_SubagentHeartbeat(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "subagent_heartbeat_maps_to_keyed_progress",
			ev: agent.Event{
				Kind: agent.EventSubagentHeartbeat, Detail: "elapsed=30s steps=2",
				Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolOutput,
				Body: uievent.ToolOutputBody{
					ToolCallID: "task-hb",
					Progress: &uievent.Progress{
						Status: "running", Step: 2,
						Log: []string{"elapsed=30s steps=2"},
					},
				},
			}},
		},
		{
			name: "subagent_heartbeat_without_parseable_steps_leaves_step_zero",
			ev: agent.Event{
				Kind: agent.EventSubagentHeartbeat, Detail: "raw loop step remap",
				Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolOutput,
				Body: uievent.ToolOutputBody{
					ToolCallID: "task-hb",
					Progress: &uievent.Progress{
						Status: "running", Step: 0,
						Log: []string{"raw loop step remap"},
					},
				},
			}},
		},
		{
			name: "subagent_heartbeat_with_empty_detail_has_no_log",
			ev: agent.Event{
				Kind:   agent.EventSubagentHeartbeat,
				Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolOutput,
				Body: uievent.ToolOutputBody{
					ToolCallID: "task-hb",
					Progress:   &uievent.Progress{Status: "running"},
				},
			}},
		},
		{
			name: "subagent_heartbeat_without_taskid_dropped",
			ev:   agent.Event{Kind: agent.EventSubagentHeartbeat, Detail: "tick without owner"},
			want: nil,
		},
	})
}

// TestTranslateEvent_SubagentHeartbeatBadCounts pins the two malformed
// step counts: a value Atoi rejects and a negative value both leave
// Progress.Step at zero, which downstream code reads as "no progress
// information", never as step 0 of real work.
func TestTranslateEvent_SubagentHeartbeatBadCounts(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "subagent_heartbeat_with_unparseable_count_leaves_step_zero",
			ev: agent.Event{
				Kind: agent.EventSubagentHeartbeat, Detail: "elapsed=30s steps=not-a-number",
				Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolOutput,
				Body: uievent.ToolOutputBody{
					ToolCallID: "task-hb",
					Progress: &uievent.Progress{
						Status: "running", Step: 0,
						Log: []string{"elapsed=30s steps=not-a-number"},
					},
				},
			}},
		},
		{
			name: "subagent_heartbeat_with_negative_count_leaves_step_zero",
			ev: agent.Event{
				Kind: agent.EventSubagentHeartbeat, Detail: "elapsed=30s steps=-3",
				Origin: agent.EventOrigin{TaskID: "task-hb", Agent: "auditor"},
			},
			want: []uievent.Event{{
				Kind: uievent.KindToolOutput,
				Body: uievent.ToolOutputBody{
					ToolCallID: "task-hb",
					Progress: &uievent.Progress{
						Status: "running", Step: 0,
						Log: []string{"elapsed=30s steps=-3"},
					},
				},
			}},
		},
	})
}

// TestTranslateEvent_SubagentDone covers EventSubagentDone: a notice
// carrying the most informative label from Origin, plus (when
// Origin.TaskID is set) a tool.output progress update that closes out
// that subagent's own sidebar row live - see translateSubagentDone.
func TestTranslateEvent_SubagentDone(t *testing.T) {
	t.Parallel()
	runMappingCases(t, []mappingCase{
		{
			name: "subagent_done_carries_task_description",
			ev: agent.Event{
				Kind: agent.EventSubagentDone,
				Origin: agent.EventOrigin{
					TaskID: "wft-1", Agent: "audit",
					TaskDescription: "audit the diff",
				},
			},
			want: []uievent.Event{
				{
					Kind: uievent.KindNotice,
					Body: uievent.NoticeBody{Text: "subagent done: audit the diff"},
				},
				{
					Kind: uievent.KindToolOutput,
					Body: uievent.ToolOutputBody{
						ToolCallID: "wft-1",
						Progress:   &uievent.Progress{Status: "completed"},
					},
				},
			},
		},
		{
			name: "subagent_done_falls_back_to_agent",
			ev: agent.Event{
				Kind:   agent.EventSubagentDone,
				Origin: agent.EventOrigin{TaskID: "wft-1", Agent: "audit"},
			},
			want: []uievent.Event{
				{
					Kind: uievent.KindNotice,
					Body: uievent.NoticeBody{Text: "subagent done: audit"},
				},
				{
					Kind: uievent.KindToolOutput,
					Body: uievent.ToolOutputBody{
						ToolCallID: "wft-1",
						Progress:   &uievent.Progress{Status: "completed"},
					},
				},
			},
		},
		{
			name: "subagent_done_with_no_origin_bare_text_and_no_progress",
			ev:   agent.Event{Kind: agent.EventSubagentDone},
			want: []uievent.Event{{
				Kind: uievent.KindNotice,
				Body: uievent.NoticeBody{Text: "subagent done"},
			}},
		},
	})
}

// TestTranslateEvent_SubagentDoneStatusVocabulary pins the terminal-status
// vocabulary on the done event's Progress entry: the row status is the done
// event's own agent.Event.Status ("completed" | "canceled" | "timed_out" |
// "error"). Empty Status keeps the legacy optimistic fallback, so an
// unclassified emitter still settles its row the old way.
func TestTranslateEvent_SubagentDoneStatusVocabulary(t *testing.T) {
	t.Parallel()
	statuses := []struct {
		eventStatus string
		rowStatus   string
	}{
		{"completed", "completed"},
		{"canceled", "canceled"},
		{"timed_out", "timed_out"},
		{"error", "error"},
		// Legacy/unclassified emitter: today's optimistic default.
		{"", "completed"},
	}
	for _, st := range statuses {
		t.Run("status_"+st.eventStatus, func(t *testing.T) {
			runMappingCases(t, []mappingCase{
				{
					name: "subagent_done_status_" + st.eventStatus,
					ev: agent.Event{
						Kind:   agent.EventSubagentDone,
						Status: st.eventStatus,
						Origin: agent.EventOrigin{TaskID: "wft-st", Agent: "audit"},
					},
					want: []uievent.Event{
						{
							Kind: uievent.KindNotice,
							Body: uievent.NoticeBody{Text: "subagent done: audit"},
						},
						{
							Kind: uievent.KindToolOutput,
							Body: uievent.ToolOutputBody{
								ToolCallID: "wft-st",
								Progress:   &uievent.Progress{Status: st.rowStatus},
							},
						},
					},
				},
			})
		})
	}
}
