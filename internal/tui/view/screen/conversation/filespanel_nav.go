// filespanel_nav.go holds the sidebar's navigation model: one ordered
// description of every group the panel draws, how tall it is, and what it
// selects.
//
// It exists because that description used to live three times - in the
// renderer, in a groupLens/groupToPickerIdx arithmetic pair, and in the
// inverse used for click routing - each encoding "index 0 is the model
// row, then files, then agents" in its own way. Any change to the row
// order had to be made identically in all three or clicks landed on the
// wrong row, and nothing failed until a human noticed. Now the renderer,
// the click map and the picker list are all derived from navGroups, so
// they cannot disagree.
package conversation

// navKind classifies one drawn group in the sidebar.
type navKind uint8

const (
	navContextHeader navKind = iota
	navContextBody           // the bar, the totals, the cap note, the buckets
	navModelCaption
	navModel
	navFilesHeader
	navFile
	navAgentsHeader
	navAgent
)

// header reports a section header, the rows a fold acts on.
func (k navKind) header() bool {
	switch k {
	case navContextHeader, navFilesHeader, navAgentsHeader:
		return true
	}
	return false
}

// navGroup is one drawn group: what it is, how many terminal lines it
// owns, and which entry it refers to (-1 when it refers to none).
//
// A subagent draws two lines - name and metrics - and they are one group
// precisely so the window can never keep one without the other.
type navGroup struct {
	kind  navKind
	lines int
	at    int
	// sel is whether the picker cursor can land here. It is a property of
	// the GROUP and not of its kind, because an empty section's header is
	// drawn identically and must not be selectable: offering a fold over
	// nothing would cost a stop on the way past and do nothing when taken.
	sel bool
}

// collapsible reports a group left/right and Enter fold.
func (g navGroup) collapsible() bool { return g.sel && g.kind.header() }

// navGroups is the sidebar's row plan at a context section of contextRows
// lines. contextRows is passed in because only the Screen knows how tall
// that section draws (the terminal height and whether the budget is
// capped both feed it); everything else about the plan is the panel's.
func (p panel) navGroups(contextRows int) []navGroup {
	files, agents := p.visibleRows()
	groups := make([]navGroup, 0, contextRows+3+len(files)+len(agents))

	groups = append(groups, navGroup{kind: navContextHeader, lines: 1, at: -1, sel: true})
	for i := 1; i < contextRows; i++ {
		groups = append(groups, navGroup{kind: navContextBody, lines: 1, at: -1})
	}
	// The "model" caption and the model row are two groups: the caption
	// names the section, the row is what Enter opens the picker from.
	groups = append(groups, navGroup{kind: navModelCaption, lines: 1, at: -1})
	groups = append(groups, navGroup{kind: navModel, lines: 1, at: -1, sel: true})

	groups = append(groups, navGroup{kind: navFilesHeader, lines: 1, at: -1, sel: len(files) > 0})
	if !p.filesCollapsed {
		for i := range files {
			groups = append(groups, navGroup{kind: navFile, lines: 1, at: i, sel: true})
		}
	}
	groups = append(groups, navGroup{kind: navAgentsHeader, lines: 1, at: -1, sel: len(agents) > 0})
	if !p.agentsCollapsed {
		for i := range agents {
			groups = append(groups, navGroup{kind: navAgent, lines: 2, at: i, sel: true})
		}
	}
	return groups
}

// navSelectable is the picker's row list, in order.
//
// It is derived from navGroups at the section's MINIMUM height, and the
// context section's extra rows are all fillers, so the list is the same
// whatever height that section draws. That is the invariant that keeps a
// terminal resize from moving a picker index out from under a selection.
func (p panel) navSelectable() []navGroup {
	all := p.navGroups(1)
	out := make([]navGroup, 0, len(all))
	for _, g := range all {
		if g.sel {
			out = append(out, g)
		}
	}
	return out
}

// navKeyOf names a row by WHAT IT IS: a file's path, a subagent's id, a
// section. Not by its render label, which changes as statuses tick, and
// not by its index, which moves as rows arrive.
//
// One function, two callers - selectionKey and rebindIfOpen. They used
// to hold a switch each, and rebindIfOpen's copy sat under a comment
// claiming it avoided exactly that. Drift between them is invisible: the
// capture would name one row and the restore would look for another, so
// the selection would move on the next live update and nothing would
// fail.
func navKeyOf(g navGroup, files []fileEntry, agents []subagentRow) string {
	switch g.kind {
	case navContextHeader:
		return "s:context"
	case navModel:
		return "m:" + modelRowLabel
	case navFilesHeader:
		return "s:files"
	case navAgentsHeader:
		return "s:agents"
	case navFile:
		if g.at >= 0 && g.at < len(files) {
			return "f:" + files[g.at].Path
		}
	case navAgent:
		if g.at >= 0 && g.at < len(agents) {
			return "a:" + agents[g.at].ID
		}
	}
	return ""
}

// navAt returns the group a picker index selects.
func (p panel) navAt(pickerIdx int) (navGroup, bool) {
	sel := p.navSelectable()
	if pickerIdx < 0 || pickerIdx >= len(sel) {
		return navGroup{}, false
	}
	return sel[pickerIdx], true
}

// navCursor returns the group the picker cursor sits on.
func (p panel) navCursor() (navGroup, bool) { return p.navAt(p.list.CursorRow()) }

// navPickerIndex maps a group index in a navGroups plan to the picker
// index it selects, or -1 when the group selects nothing.
func navPickerIndex(groups []navGroup, gIdx int) int {
	if gIdx < 0 || gIdx >= len(groups) || !groups[gIdx].sel {
		return -1
	}
	idx := 0
	for i := 0; i < gIdx; i++ {
		if groups[i].sel {
			idx++
		}
	}
	return idx
}

// navSelGroup is the inverse: the group index a picker index draws at.
func navSelGroup(groups []navGroup, pickerIdx int) int {
	if pickerIdx < 0 {
		return -1
	}
	idx := 0
	for i := range groups {
		if !groups[i].sel {
			continue
		}
		if idx == pickerIdx {
			return i
		}
		idx++
	}
	return -1
}

// navGroupLens is the plan's line heights, for the windowing math.
func navGroupLens(groups []navGroup) []int {
	lens := make([]int, len(groups))
	for i, g := range groups {
		lens[i] = g.lines
	}
	return lens
}

// sectionFlag returns the fold state the group owns, or nil when it owns
// none.
//
// This is the ONLY test for "can this row fold". It used to be two - a
// collapsible() predicate and a separate flag lookup - which made every
// nil check downstream unreachable, so the guards that decide a key does
// nothing were never exercised. One lookup, one guard, and the guard is
// on the path a file or agent row actually takes.
// Callers reach this only through navCursor, which yields selectable
// groups exclusively, so selectability is not re-checked here; the
// switch's own fallthrough is what answers for a file or agent row.
func (p *panel) sectionFlag(g navGroup) *bool {
	switch g.kind {
	case navContextHeader:
		return &p.contextCollapsed
	case navFilesHeader:
		return &p.filesCollapsed
	case navAgentsHeader:
		return &p.agentsCollapsed
	}
	return nil
}

// setSectionCollapsed folds or unfolds the section the cursor sits on. It
// reports false when the cursor is not on a foldable header, so the
// caller can pass the key on rather than swallow it.
//
// The cursor is re-bound afterwards because folding a section removes its
// members from the picker list: without the rebind the cursor index would
// still point past the end of a list that just got shorter, and the
// selection would silently jump to whatever now sits at that index.
func (p *panel) setSectionCollapsed(collapsed bool) bool {
	g, ok := p.navCursor()
	if !ok {
		return false
	}
	flag := p.sectionFlag(g)
	if flag == nil || *flag == collapsed {
		return false
	}
	*flag = collapsed
	p.rebindIfOpen()
	return true
}

// toggleSection is setSectionCollapsed in whichever direction is the
// change, for Enter and for a click on the header.
func (p *panel) toggleSection() bool {
	g, ok := p.navCursor()
	if !ok {
		return false
	}
	flag := p.sectionFlag(g)
	if flag == nil {
		return false
	}
	return p.setSectionCollapsed(!*flag)
}

// sectionMarker is the fold glyph a collapsible header carries, in the
// transcript's own grammar: ">" closed, "v" open. It is the affordance,
// so a header without one must not respond to left/right either.
func sectionMarker(collapsed bool) string {
	if collapsed {
		return "> "
	}
	return "v "
}

// selectNavKind moves the cursor to the nth row of the given kind (n
// counted from 0), and reports whether such a row exists.
//
// It is how callers and tests name a row by WHAT IT IS rather than by its
// index in the picker. Index arithmetic spread through call sites is what
// made adding the section headers a change to twenty places; a caller
// that asks for "the second subagent row" keeps working when the rows
// above it move.
func (p *panel) selectNavKind(kind navKind, n int) bool {
	for i, g := range p.navSelectable() {
		if g.kind != kind {
			continue
		}
		if n == 0 {
			p.list.MoveTo(i)
			p.noteSelection()
			return true
		}
		n--
	}
	return false
}
