package transcript

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestToggleReasoningLeaderHead(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)
	m.SetSize(80, 24)
	m.blocks = []Block{
		{
			Kind:        uievent.KindReasoning,
			Collapsible: true,
			Collapsed:   true,
			Header:      Header{Label: "Thought"},
			Body:        []string{"reasoning line 1", "reasoning line 2"},
		},
		{
			Kind:        uievent.KindToolEnd,
			Collapsible: true,
			Collapsed:   true,
			Header:      Header{Label: "tool1"},
		},
		{
			Kind:        uievent.KindToolEnd,
			Collapsible: true,
			Collapsed:   true,
			Header:      Header{Label: "tool2"},
		},
	}
	m.focus = 0
	next, ok := m.toggleReasoningFocused()
	if !ok {
		t.Fatalf("expected toggleReasoningFocused to return true")
	}
	if next.blocks[0].Collapsed {
		t.Fatalf("expected head block to be expanded after run expansion")
	}
}

func TestDiffBodyResliceCorruptedPrefixDefensive(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)
	m.SetSize(80, 24)
	b := Block{
		Kind: uievent.KindToolEnd,
		Diff: &uievent.Diff{
			Path: "a.txt",
			Hunks: []uievent.DiffHunk{
				{Lines: []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: "added"}}},
			},
		},
		DiffBodyPrefixLen: 100, // greater than len(b.Body)
		Body:              []string{"line 1"},
	}
	res := m.restyle(b)
	if len(res.Body) == 0 {
		t.Fatalf("expected non-empty body after restyling diff")
	}
}
