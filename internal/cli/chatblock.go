package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (m *tuiModel) appendBlock(block ChatBlock) {
	block.Sequence = uint64(len(m.blocks) + 1)
	block.ID = chatBlockID(block.TurnID, block.Sequence)
	m.blocks = append(m.blocks, block)
	// Block-based truncation: keep at most maxBlocks whole blocks.
	// Dropping whole blocks preserves block identity, kind order,
	// and hit ranges — unlike old line-based truncation which
	// sliced blocks by a line count that bore no relation to blocks.
	const maxBlocks = 1000
	if len(m.blocks) > maxBlocks {
		dropped := len(m.blocks) - maxBlocks
		m.blocks = m.blocks[dropped:]
		// Re-sequenced dropped blocks start at 1.
		for i := range m.blocks {
			m.blocks[i].Sequence = uint64(i + 1)
			m.blocks[i].ID = chatBlockID(m.blocks[i].TurnID, m.blocks[i].Sequence)
		}
		if m.msgOffset > 0 && m.session != nil {
			m.msgOffset = min(m.session.MessagesCount(), m.msgOffset+dropped)
		}
	}
	// Rebuild messages from blocks (single source of truth).
	rendered := RenderChatBlocks(m.blocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault)
	m.messages = rendered.Lines
	m.chatBlockRanges = rendered.Ranges
}

func (m *tuiModel) buildViewportContent() string {
	if len(m.blocks) == 0 && len(m.messages) > 0 {
		for _, line := range m.messages {
			m.appendBlock(ChatBlock{Kind: ChatBlockSystem, Text: line, Rendered: line})
		}
	}
	if len(m.blocks) == 0 {
		return ""
	}
	rendered := RenderChatBlocks(m.blocks, m.modelName, max(20, m.width-2), m.thinkingExpandDefault)
	m.messages = rendered.Lines
	m.chatBlockRanges = rendered.Ranges
	return strings.Join(rendered.Lines, "\n")
}

type ChatBlockKind string

const (
	ChatBlockUser      ChatBlockKind = "user"
	ChatBlockAssistant ChatBlockKind = "assistant"
	ChatBlockTool      ChatBlockKind = "tool"
	ChatBlockThinking  ChatBlockKind = "thinking"
	ChatBlockSystem    ChatBlockKind = "system"
	ChatBlockDivider   ChatBlockKind = "turn_divider"
)

type ChatBlock struct {
	ID         string
	TurnID     uint64
	Sequence   uint64
	Kind       ChatBlockKind
	Text       string
	ToolName   string
	ToolCallID string
	Collapsed  bool
	// ScrollOffset is the scrolled position for windowed rendering
	// (e.g. thinking blocks). 0 = show the most recent lines.
	ScrollOffset int
	// SentAt is when the user sent this message (local wall clock).
	// Zero for non-user blocks or hydrated history without a timestamp.
	SentAt time.Time
	// Rendered preserves existing local UI formatting for compatibility-only
	// lines. Structured history and stream blocks leave it empty.
	Rendered string
}

func chatBlockFromMessage(turn, seq uint64, msg provider.Message) ChatBlock {
	kind := chatBlockKind(msg.Role)
	return ChatBlock{ID: chatBlockID(turn, seq), TurnID: turn, Sequence: seq, Kind: kind, Text: msg.Content, ToolName: msg.Name, ToolCallID: msg.ToolCallID}
}

type ChatBlockEvent struct {
	TurnID   uint64
	Sequence uint64
	BlockID  string
	Kind     ChatBlockKind
	Text     string
	ToolName string
}

func HydrateChatBlocks(messages []provider.Message) []ChatBlock {
	blocks := make([]ChatBlock, 0, len(messages))
	var turn, seq uint64
	for _, msg := range messages {
		if msg.Role == provider.RoleSystem {
			continue
		}
		if msg.Role == provider.RoleUser {
			if turn > 0 {
				// Insert turn divider before each new turn after the first.
				blocks = append(blocks, ChatBlock{
					ID:     chatBlockID(turn+1, 0),
					TurnID: turn + 1,
					Kind:   ChatBlockDivider,
				})
			}
			turn++
		}
		kind := chatBlockKind(msg.Role)
		if kind == "" {
			continue
		}
		if msg.Content != "" || len(msg.ToolCalls) == 0 {
			seq++
			blocks = append(blocks, ChatBlock{
				ID:         chatBlockID(turn, seq),
				TurnID:     turn,
				Sequence:   seq,
				Kind:       kind,
				Text:       msg.Content,
				ToolName:   msg.Name,
				ToolCallID: msg.ToolCallID,
				Collapsed:  kind == ChatBlockTool,
				SentAt:     msg.CreatedAt,
			})
		}
		for _, call := range msg.ToolCalls {
			seq++
			blocks = append(blocks, ChatBlock{ID: chatBlockID(turn, seq), TurnID: turn, Sequence: seq, Kind: ChatBlockTool, Text: call.Function.Arguments, ToolName: call.Function.Name, ToolCallID: call.ID, Collapsed: true})
		}
	}
	return blocks
}

func chatBlockKind(role string) ChatBlockKind {
	switch role {
	case provider.RoleUser:
		return ChatBlockUser
	case provider.RoleAssistant:
		return ChatBlockAssistant
	case provider.RoleTool:
		return ChatBlockTool
	}
	return ""
}

func chatBlockID(turn, seq uint64) string { return fmt.Sprintf("turn-%d-block-%d", turn, seq) }

func ApplyChatBlockEvent(blocks []ChatBlock, event ChatBlockEvent) []ChatBlock {
	if event.TurnID == 0 || event.Sequence == 0 {
		return blocks
	}
	for i := range blocks {
		if blocks[i].ID == event.BlockID && event.Sequence >= blocks[i].Sequence {
			if event.Sequence == blocks[i].Sequence && event.Text == blocks[i].Text {
				return blocks
			}
			blocks[i].Text, blocks[i].Sequence = event.Text, event.Sequence
			return blocks
		}
	}
	if event.BlockID == "" {
		event.BlockID = chatBlockID(event.TurnID, event.Sequence)
	}
	for _, block := range blocks {
		if block.TurnID == event.TurnID && block.Sequence >= event.Sequence {
			return blocks
		}
	}
	return append(blocks, ChatBlock{ID: event.BlockID, TurnID: event.TurnID, Sequence: event.Sequence, Kind: event.Kind, Text: event.Text, ToolName: event.ToolName})
}

var chatANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

func SafeChatBlockText(text string, maxChars int) string {
	text = chatANSI.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\x00", "")
	if maxChars > 0 && len([]rune(text)) > maxChars {
		return string([]rune(text)[:maxChars]) + "…"
	}
	return text
}
