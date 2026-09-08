package conversation

import (
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/approval"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/blackboard"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/history"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/queue"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func (s Screen) newSessionState(conv ports.Conversation) *sessionState {
	st := &sessionState{
		conv:         conv,
		transcript:   transcript.New(s.Theme, s.Tier),
		composer:     composer.New(s.Theme, s.Tier, s.chatWidth()),
		statusline:   statusline.New(s.Theme, s.Tier),
		approval:     approval.New(s.Theme, s.Tier),
		history:      history.New(s.Theme, s.Tier),
		queueOverlay: queue.New(s.Theme, s.Tier),
		blackboard:   blackboard.New(s.Theme, s.Tier),
		panel:        newPanel(s.Theme, s.Tier),
		threads:      s.threads,
	}
	st.transcript.SetSize(s.chatWidth(), s.transcriptHeight())
	st.composer.SetCommands(s.commands)
	st.composer.SetMentions(s.mentions)
	st.approval.SetWidth(contentWidth(s.width))
	st.history.SetWidth(contentWidth(s.width))
	st.queueOverlay.SetWidth(contentWidth(s.width))
	st.blackboard.SetWidth(contentWidth(s.width))
	if conv != nil {
		for i, m := range conv.History() {
			isLastMsg := (i == len(conv.History())-1)
			switch m.Role {
			case "user":
				st.history.Push(m.Text)
				ev := uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: m.Text}}
				st.transcript, _ = st.transcript.HandleEvent(ev)
			default:
				if m.Reasoning != "" {
					st.transcript, _ = st.transcript.HandleEvent(uievent.Event{
						Kind: uievent.KindReasoning,
						Body: uievent.ReasoningDeltaBody{Text: m.Reasoning, WordCount: len(m.Reasoning)},
					})
				}
				for _, tc := range m.ToolCalls {
					st.transcript = replayHistoricalToolCall(st.transcript, tc)
				}
				if m.Text != "" {
					st.transcript, _ = st.transcript.HandleEvent(uievent.Event{
						Kind: uievent.KindTextDelta,
						Body: uievent.TextDeltaBody{Text: m.Text},
					})
					st.transcript, _ = st.transcript.HandleEvent(uievent.Event{
						Kind: uievent.KindTextEnd,
						Body: uievent.TextEndBody{},
					})
				}
				reason := "completed"
				if !isLastMsg {
					reason = "end_turn"
				}
				st.transcript, _ = st.transcript.HandleEvent(uievent.Event{
					Kind: uievent.KindTurnEnd,
					Body: uievent.TurnEndBody{Reason: reason},
				})
			}
		}
	}
	return st
}

func replayHistoricalToolCall(transcript transcript.Model, tc ports.ToolCall) transcript.Model {
	transcript, _ = transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: tc.ID, Name: tc.Name, Args: parseToolArgs(tc.Arguments)},
	})
	transcript, _ = transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: tc.ID, Name: tc.Name, OK: historicalToolCallOK(tc),
			Result: tc.Output, Diff: tc.Diff,
		},
	})
	return transcript
}

func historicalToolCallOK(tc ports.ToolCall) bool {
	if strings.EqualFold(tc.Name, "run_command") {
		for _, line := range strings.Split(tc.Output, "\n") {
			status, ok := strings.CutPrefix(strings.TrimSpace(line), "exit=")
			if !ok {
				continue
			}
			code, err := strconv.Atoi(status)
			return err == nil && code == 0
		}
	}
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(tc.Output)), "error:")
}
