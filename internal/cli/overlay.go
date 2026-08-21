// Block and fleet detail are bounded modal pagers over the chat canvas.
package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type blockOverlay struct {
	title           string
	lines           []string
	yOffset         int
	prefs           DialogPrefs
	lastInnerW      int
	renderedRows    []string
	renderedSources []int
	kind            string
}

func detailDialogPrefs() DialogPrefs {
	return DialogPrefs{PreferredWPct: 90, PreferredHPct: 85, MinW: 40, MinH: 8, FrameCols: 4, FrameRows: 2, Pager: true}
}

func safeDialogText(text string) string {
	text = RedactPreview(SafeChatBlockText(text, 0))
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
}

// newBlockOverlay keeps the full redacted semantic content. Wrapping is a
// render-time operation because the terminal width can change while open.
func newBlockOverlay(block ChatBlock) *blockOverlay {
	toolName := safeDialogText(block.ToolName)
	title := ToolIconForName(toolName) + " " + toolName
	if block.Kind == ChatBlockThinking {
		title = "▾ thinking"
	}
	if block.AgentName != "" {
		title += "  ◆ " + safeDialogText(block.AgentName)
	}
	if block.Elapsed > 0 {
		title += "  · " + FormatDuration(block.Elapsed)
	}
	switch {
	case block.Failed:
		title += "  ✗"
	case block.Kind == ChatBlockTool && block.Elapsed > 0:
		title += "  ✓"
	}
	content := RedactPreview(SafeChatBlockText(block.Text, 0))
	return &blockOverlay{title: title, lines: strings.Split(content, "\n"), prefs: detailDialogPrefs(), kind: "detail"}
}

func (o *blockOverlay) layout(w, h int) DialogLayout {
	layout := MakeDialogLayout(w, h, o.prefs, func(innerW int) (int, int) {
		rows := WrapDisplayRows(o.lines, innerW)
		maxW := 0
		for _, row := range rows {
			maxW = Max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
	return layout
}

func (o *blockOverlay) displayRows(innerW int) []string {
	rows := WrapDisplayRows(o.lines, innerW)
	if len(rows) == 0 {
		return []string{""}
	}
	return rows
}

func (o *blockOverlay) rowsForLayout(innerW, pageH int) []string {
	if o.kind != "status" {
		return o.displayRows(innerW)
	}
	rawRows := o.displayRows(innerW)
	if len(rawRows) <= pageH {
		return rawRows
	}
	compact := make([]string, 0, len(o.lines))
	agents := 0
	for _, row := range o.lines {
		plain := stripANSI(row)
		if strings.HasPrefix(strings.TrimSpace(plain), "◆ ") {
			agents++
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(plain), "agents") {
			continue
		}
		compact = append(compact, row)
	}
	if agents > 0 {
		compact = append(compact, fmt.Sprintf("  agents: %d (open fleet for details)", agents))
	}
	rows := WrapDisplayRows(compact, innerW)
	if len(rows) > pageH {
		// Keep the core facts in the non-paging fallback. A narrow terminal
		// may not have enough rows for the normal compact layout, but replacing
		// it with an agent-only sentence silently loses session state.
		facts := make([]string, 0, len(compact))
		for _, row := range compact {
			if plain := strings.TrimSpace(stripANSI(row)); plain != "" {
				if strings.HasPrefix(plain, "agents:") {
					continue
				}
				facts = append(facts, strings.Join(strings.Fields(plain), " "))
			}
		}
		if agents > 0 {
			facts = append(facts, fmt.Sprintf("agents: %d (open fleet for details)", agents))
		}
		return packStatusFacts(facts, innerW, pageH)
	}
	return rows
}

func packStatusFacts(facts []string, innerW, pageH int) []string {
	innerW = Max(1, innerW)
	pageH = Max(1, pageH)
	rows := packStatusWords(append([]string{"status: compact summary"}, facts...), innerW)
	if len(rows) <= pageH {
		return rows
	}
	compact := make([]string, 0, len(facts))
	for _, fact := range facts {
		fields := strings.Fields(fact)
		if len(fields) == 0 || fields[0] == "Session" || fields[0] == "Current" {
			continue
		}
		if fields[0] == "agents:" {
			compact = append(compact, "agents: "+strings.Join(fields[1:], " "))
			continue
		}
		value := strings.Join(fields[1:], " ")
		if value == "" {
			compact = append(compact, fields[0])
			continue
		}
		compact = append(compact, fields[0]+"="+ansi.Truncate(value, Max(4, innerW/2), "…"))
	}
	rows = packStatusWords(append([]string{"status:"}, compact...), innerW)
	if len(rows) <= pageH {
		return rows
	}
	// At this point the terminal is smaller than the semantic fact set. Keep
	// every fact label visible in the bounded summary, rather than allowing a
	// long workspace value to push later labels out of the canvas.
	labels := make([]string, 0, len(compact)+1)
	labels = append(labels, "status:")
	for _, fact := range compact {
		label := strings.SplitN(fact, "=", 2)[0]
		if strings.HasPrefix(fact, "agents:") {
			fields := strings.Fields(fact)
			if len(fields) >= 2 {
				label = strings.Join(fields[:2], " ")
			}
		}
		labels = append(labels, label)
	}
	rows = packStatusWords(labels, innerW)
	if len(rows) <= pageH {
		return rows
	}
	if innerW <= 12 && pageH >= 3 {
		return narrowStatusRows(facts, innerW, pageH)
	}
	// A one-row canvas cannot carry every label horizontally. Return one
	// bounded, explicit summary row so ViewAt never silently pages a
	// non-paging dialog beyond its available height.
	return []string{ansi.Truncate(strings.Join(labels, " "), innerW, "…")}
}

func narrowStatusRows(facts []string, innerW, pageH int) []string {
	values := make(map[string]string, len(facts))
	for _, fact := range facts {
		fields := strings.Fields(fact)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value := strings.Join(fields[1:], " ")
		switch key {
		case "tools":
			value = fields[len(fields)-1]
		case "queued":
			value = fields[1]
		case "agents":
			value = fields[1]
		}
		values[key] = value
	}
	short := func(key, alias string) string {
		value := values[key]
		if value == "" {
			return ""
		}
		return alias + ansi.Truncate(value, Max(1, innerW-len(alias)), "…")
	}
	if pageH == 3 {
		rows := []string{
			ansi.Truncate("m="+values["model"]+" w="+values["workspace"], innerW, "…"),
			ansi.Truncate("n"+values["messages"]+"t"+values["turns"]+"b"+values["blocks"], innerW, "…"),
			ansi.Truncate("e"+values["elapsed"]+"o"+values["tools"]+"a"+values["agents"]+"q"+values["queued"], innerW, "…"),
		}
		return rows
	}
	rows := []string{short("model", "m="), short("workspace", "w=")}
	counters := "n" + values["messages"] + "t" + values["turns"] + "b" + values["blocks"]
	current := "e" + values["elapsed"] + "o" + values["tools"] + "a" + values["agents"] + "q" + values["queued"]
	rows = append(rows, ansi.Truncate(counters, innerW, "…"), ansi.Truncate(current, innerW, "…"))
	for i := range rows {
		if rows[i] == "" {
			rows[i] = "·"
		}
	}
	return rows
}

func packStatusWords(facts []string, innerW int) []string {
	rows := make([]string, 0, len(facts))
	current := ""
	for _, fact := range facts {
		for _, word := range strings.Fields(fact) {
			if ansi.StringWidth(word) > innerW {
				word = ansi.Truncate(word, innerW, "…")
			}
			candidate := strings.TrimSpace(current + " " + word)
			if ansi.StringWidth(candidate) > innerW && current != "" {
				rows = append(rows, current)
				current = word
			} else {
				current = candidate
			}
		}
	}
	if current != "" {
		rows = append(rows, current)
	}
	return rows
}

func (o *blockOverlay) clamp(pageH int) {
	if pageH < 1 {
		pageH = 1
	}
	rows := o.renderedRows
	if len(rows) == 0 {
		rows = o.displayRows(Max(1, o.lastInnerW))
	}
	maxOffset := len(rows) - pageH
	if maxOffset < 0 {
		maxOffset = 0
	}
	o.yOffset = Min(Max(0, o.yOffset), maxOffset)
}

// scroll receives the already-rendered page height, so key and View paths
// cannot disagree about the final reachable row.
func (o *blockOverlay) scroll(delta, pageH int) {
	o.yOffset += delta
	if o.yOffset < 0 {
		o.yOffset = 0
	}
	// ViewAt clamps against its exact rendered display-row snapshot. Keeping
	// the offset pending here preserves compatibility with callers that used a
	// terminal height before the geometry handoff was introduced.
	if pageH < 1 {
		o.yOffset = 0
	}
}

func (o *blockOverlay) ViewAt(w, h int) (string, DialogLayout) {
	layout := o.layout(w, h)
	previousSource := -1
	if o.lastInnerW > 0 && o.lastInnerW != layout.InnerW && o.yOffset < len(o.renderedSources) {
		previousSource = o.renderedSources[o.yOffset]
	}
	rows := o.rowsForLayout(Max(1, layout.InnerW), layout.PageH)
	o.renderedRows = rows
	_, o.renderedSources = WrapDisplayRowsWithSources(o.lines, Max(1, layout.InnerW))
	if previousSource >= 0 {
		for i, source := range o.renderedSources {
			if source == previousSource {
				o.yOffset = i
				break
			}
		}
	}
	o.lastInnerW = Max(1, layout.InnerW)
	o.clamp(layout.PageH)
	start := Min(o.yOffset, len(rows))
	end := Min(len(rows), start+layout.PageH)
	visible := rows[start:end]
	pos := "all"
	if len(rows) > layout.PageH {
		pct := 100 * (o.yOffset + layout.PageH) / len(rows)
		pos = fmt.Sprintf("%d%%", Min(100, pct))
	}
	return RenderDialogFrame(o.title, visible, dialogFooter(pos, len(rows), o.prefs.Pager), layout), layout
}

// dialogFooter builds the dialog footer hint line. Relocated from
// dialog_compositor.go: this file is its sole caller.
func dialogFooter(pos string, total int, pager bool) string {
	if !pager {
		return " esc close "
	}
	return fmt.Sprintf(" %s · %d lines · j/k scroll · pgup/pgdn · esc close ", pos, total)
}

func (o *blockOverlay) View(w, h int) string {
	view, _ := o.ViewAt(Max(1, w), Max(1, h))
	return view
}

func (m *tuiModel) setOverlay(o *blockOverlay) {
	m.closeSuggest()
	m.closeHistory()
	m.overlay = o
	m.hitMap.invalidate()
}

// routeModalKey dispatches a key to whichever modal dialog is currently open.
// Returns (handled, skipTextarea, cmds).
func (m *tuiModel) routeModalKey(key string) (bool, bool, []tea.Cmd) {
	if m.modelDlg != nil {
		return m.handleModelDialogKey(key)
	}
	if m.agentDlg != nil {
		return m.handleAgentDialogKey(key)
	}
	if m.effortDlg != nil {
		return m.handleEffortDialogKey(key)
	}
	if m.workflowRunDlg != nil {
		return m.handleWorkflowRunDialogKey(key)
	}
	if m.worktreeDlg != nil {
		return m.handleWorktreeDialogKey(key)
	}
	return false, false, nil
}

func (m *tuiModel) closeModal() {
	if m.overlay == nil && m.modelDlg == nil && m.agentDlg == nil && m.effortDlg == nil && m.worktreeDlg == nil && m.workflowRunDlg == nil && !m.queueMgr.open {
		return
	}
	m.overlay = nil
	m.modelDlg = nil
	m.agentDlg = nil
	m.effortDlg = nil
	m.worktreeDlg = nil
	m.workflowRunDlg = nil
	m.queueMgr = queueMgrState{}
	m.editingQueued = false
	m.editMemory = queuedItem{}
	m.hitMap.invalidate()
}

func (m *tuiModel) handleOverlayKey(key string) (bool, bool, []tea.Cmd) {
	layout := m.overlay.layout(Max(1, m.width), Max(1, m.height))
	pageH := Max(1, layout.PageH)
	switch key {
	case "esc", "q":
		m.setOverlay(nil)
	case "j", "down", "k", "up", "pgdown", " ", "f", "pgup", "b", "home", "g", "end", "G":
		if !m.overlay.prefs.Pager {
			return true, true, nil
		}
		switch key {
		case "j", "down":
			m.overlay.scroll(1, pageH)
		case "k", "up":
			m.overlay.scroll(-1, pageH)
		case "pgdown", " ", "f":
			m.overlay.scroll(pageH, pageH)
		case "pgup", "b":
			m.overlay.scroll(-pageH, pageH)
		case "home", "g":
			m.overlay.scroll(-1<<30, pageH)
		case "end", "G":
			m.overlay.scroll(1<<30, pageH)
		}
	}
	return true, true, nil
}

func (m *tuiModel) openSelectedBlockOverlay() bool {
	if m.selectedBlockID == "" {
		return false
	}
	for i := range m.blocks {
		if m.blocks[i].ID == m.selectedBlockID {
			m.setOverlay(newBlockOverlay(m.blocks[i]))
			return true
		}
	}
	return false
}
