package transcript

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// proseLines splits assistant prose into LOGICAL lines. It deliberately
// does not wrap: Block.bodyRows wraps at render time, so prose reflows
// when the terminal is resized. Wrapping here would freeze the measure
// taken at push time, and a later shrink would leave rows wider than the
// terminal that Height could not account for.
func proseLines(text string) []string {
	// The renderer terminates its output with a newline, which a plain
	// Split turns into a trailing empty line. That line is a real row: it
	// made a turn's prose sit two blank rows above the activity that
	// followed while activity-to-prose kept one, so the transcript's
	// spacing was asymmetric in a way no rule asked for.
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// userLines renders the user's turn: a bold marker on the first row,
// continuation rows indented two columns under it (wireframes-panes.md
// section 4).
//
// The BODY is drawn in RoleFGMuted while the marker stays at full weight,
// which inverts the emphasis the obvious way round: the reader wrote these
// words, so they need to FIND the turn, not read it again, and the marker
// is what does the finding. Recessing the body leaves the agent's reply
// the brightest prose on screen, which is what the reader is there for.
//
// No background fill. An earlier comment here described a full-bleed
// RoleBGSelection wash on every row, which no code ever applied; it is
// deleted rather than implemented, for three reasons. RoleBGSelection is
// simultaneously the drag-select fill and the sidebar's selected-row
// fill, so a user message wearing it would be indistinguishable from
// selected text - the old comment's excuse, that no selected-row chrome
// renders beside a transcript message, stopped being true when the
// sidebar began drawing one. A full-bleed fill must also paint to the
// terminal edge on every row, on every repaint. And NO_COLOR strips
// backgrounds outright (ux-rules 9.4-9.5), so a background would be a
// cue that vanishes completely for the readers who most need one, while
// weight survives.
func userLines(t theme.Theme, tier theme.Tier, width int, input string) []string {
	display := formatUserDisplay(tier, input)
	// The marker occupies two columns, so the text measure is that much
	// narrower and continuations align under the first character.
	wrapped := render.Wrap(display, render.ProseMeasure(width)-2)
	marker := render.Role(t, tier, theme.RoleAccent).Bold(true).Render("> ")
	body := render.Role(t, tier, theme.RoleFGMuted)
	out := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		if i == 0 {
			out = append(out, marker+body.Render(line))
			continue
		}
		out = append(out, "  "+body.Render(line))
	}
	return out
}

// formatUserDisplay returns a concise presentation of the user prompt.
// If the prompt is an expanded skill invocation with instructions, it returns
// a clean display with a skill icon, the slash skill name, and user arguments.
func formatUserDisplay(tier theme.Tier, input string) string {
	if !strings.Contains(input, "<skill-instructions") || !strings.Contains(input, "</skill-instructions>") {
		return input
	}
	var skillName, args string
	startTagIdx := strings.Index(input, "<skill-instructions")
	endTagIdx := strings.Index(input[startTagIdx:], ">")
	if endTagIdx != -1 {
		tagContent := input[startTagIdx : startTagIdx+endTagIdx+1]
		if nameIdx := strings.Index(tagContent, "name="); nameIdx != -1 {
			val := strings.Trim(tagContent[nameIdx+5:], " >\"'")
			if cut := strings.IndexAny(val, " >\"'"); cut != -1 {
				val = val[:cut]
			}
			skillName = strings.TrimSpace(val)
		}
	}

	if skillName == "" {
		instStart := input[startTagIdx:]
		if closeIdx := strings.Index(instStart, ">"); closeIdx != -1 {
			instBody := instStart[closeIdx+1:]
			if endClose := strings.Index(instBody, "</skill-instructions>"); endClose != -1 {
				instBody = instBody[:endClose]
				for _, line := range strings.Split(instBody, "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "# ") {
						skillName = strings.TrimSpace(strings.TrimPrefix(line, "# "))
						break
					}
				}
			}
		}
	}

	if argIdx := strings.Index(input, "\n\nArguments:\n"); argIdx != -1 {
		args = strings.TrimSpace(input[argIdx+len("\n\nArguments:\n"):])
	} else if argIdx := strings.Index(input, "Arguments:\n"); argIdx != -1 {
		args = strings.TrimSpace(input[argIdx+len("Arguments:\n"):])
	}

	if skillName == "" {
		skillName = "skill"
	}

	cmdPrefix := "/" + strings.TrimPrefix(skillName, "/")
	icon := "⚡"
	if tier == theme.TierASCII {
		icon = "*"
	}

	if args != "" {
		return fmt.Sprintf("%s %s %s", icon, cmdPrefix, args)
	}
	return fmt.Sprintf("%s %s", icon, cmdPrefix)
}

// outputLines is the body a tool.output event contributes. Subagent
// progress carries no body here - the sidebar panel owns that now (see
// handleToolOutput). A chunk carries its own lines; every line is kept,
// since discarding all but the first was a silent truncation.
func outputLines(b uievent.ToolOutputBody) []string {
	if b.Chunk == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(b.Chunk, "\n"), "\n")
}

func toolOutputBlock(t theme.Theme, tier theme.Tier, b uievent.ToolOutputBody) Block {
	if b.Chunk == "" {
		return Block{}
	}
	// Keep every line of tool output. Discarding all but the first was a
	// silent truncation with nothing to signal it.
	return Block{
		Kind:   uievent.KindToolOutput,
		Header: Header{Label: "output"},
		Body:   strings.Split(strings.TrimRight(b.Chunk, "\n"), "\n"),
	}
}

func toolEndBlockValue(t theme.Theme, tier theme.Tier, w int, b uievent.ToolEndBody, args map[string]any) Block {
	role, status := theme.RoleSuccess, "ok"
	if !b.OK {
		role, status = theme.RoleDanger, "failed"
	}
	summary := b.Result
	if b.Err != "" {
		summary = b.Err
	}
	if w <= 0 {
		w = 80
	}

	// A batch/turn-budget degrade (internal/remainder.TruncationNotice)
	// looks alarming rendered verbatim - "kept 0 of 955 bytes" reads as a
	// failure to a human even though the model just calls read_output and
	// moves on. Recognize the exact trailer TruncationNotice emits, strip
	// it from the content FormatToolOutputWithContext sees, and render a
	// calm one-line badge instead of raw truncation prose.
	var noticeLine string
	if prefix, kept, total, ref, ok := remainder.ParseTruncationNotice(summary); ok {
		summary = prefix
		noticeLine = render.Role(t, tier, theme.RoleFGSubtle).Render(truncationBadge(kept, total, ref))
	}

	// A formatter that knows this tool returns a detail; one that does not
	// returns "", and the header keeps just the tool name. Copying the
	// first body line into the detail as a fallback printed line 1 twice
	// on the direct-push end path (no live start block): once in the
	// header, once as body row one. The body is the single home of the
	// output (transcript-polish.md R7).
	detail, body, coll := render.FormatToolOutputWithContext(t, tier, b.Name, args, summary, b.OK, w)
	if noticeLine != "" {
		body = append(body, noticeLine)
	}

	// One duration ladder everywhere (transcript-polish.md R5): the same
	// FormatElapsed a later status-line call uses, so "4.1s" never
	// appears beside "4100ms" on one screen.
	duration := render.FormatElapsed(int(b.DurationMS))
	blk := Block{
		Kind:      uievent.KindToolEnd,
		Args:      args,
		CallID:    b.ToolCallID,
		ElapsedMS: int(b.DurationMS),
		Header: Header{
			Label: b.Name, Detail: detail,
			Meta: duration, State: status, Role: role,
		},
		Body:        body,
		Collapsible: coll,
		Collapsed:   coll,
	}
	if b.Diff != nil {
		blk.Diff = b.Diff
		blk.Header.Detail = b.Diff.Path
		blk.Header.DiffAdd = b.Diff.Added
		blk.Header.DiffDel = b.Diff.Removed
		blk.Header.Meta = duration
		blk.Body = render.FormatDiffLines(t, tier, w, *b.Diff)
	}
	// A finished call collapses by default whatever its body size, so
	// consecutive calls coalesce into one summary row; failures keep the
	// failure visible. This mirrors updateLive's rule, which is the path
	// a call takes when it had a live block to merge into - and it is
	// applied HERE, after the diff branch above has replaced Body,
	// because deciding from the pre-diff body left a direct-pushed diff
	// block expanded while the merged one collapsed. Two paths, one
	// rule, and the comment claiming so was previously false.
	if len(blk.Body) > 0 && role != theme.RoleDanger {
		blk.Collapsible, blk.Collapsed = true, true
	}
	return blk
}

// truncationBadge renders a degrade notice as a short human-facing line
// instead of TruncationNotice's model-facing prose. kept/total/ref are
// exactly what remainder.ParseTruncationNotice recovered from the raw
// notice, so the three cases below mirror TruncationNotice's own: no ref
// (store failed / no spool), a full degrade (kept 0, everything moved to
// the remainder), and a partial degrade (kept some, the rest is behind
// read_output).
func truncationBadge(kept, total int, ref string) string {
	switch {
	case ref == "":
		return fmt.Sprintf("· truncated: kept %d of %d B", kept, total)
	case kept == 0:
		return fmt.Sprintf("· %d B stored → read_output", total)
	default:
		return fmt.Sprintf("· showing %d of %d B → read_output", kept, total)
	}
}

func planBlockValue(t theme.Theme, tier theme.Tier, b uievent.PlanBody) Block {
	body := make([]string, 0, len(b.Items))
	for _, item := range b.Items {
		mark, style := "[ ]", render.Role(t, tier, theme.RoleFG)
		if item.Done {
			mark, style = "[x]", render.Role(t, tier, theme.RoleFGSubtle)
		}
		body = append(body, style.Render(mark+" "+item.Text))
	}
	planCopy := b
	var state string
	var role theme.Role
	if b.Total > 0 && b.Done == b.Total {
		state, role = "done", theme.RoleSuccess
	}
	return Block{
		Kind:   uievent.KindPlan,
		Plan:   &planCopy,
		Header: Header{Label: "plan", Meta: fmt.Sprintf("%d of %d", b.Done, b.Total), State: state, Role: role},
		Body:   body,
	}
}

// noticeBlockValue renders a free-text advisory line. A multi-line notice
// (e.g. /hooks' listing) keeps its header to the first line and carries the
// rest as a collapsible body - previously every line past the first was
// silently dropped, since the block had no Body at all.
func noticeBlockValue(b uievent.NoticeBody) Block {
	lines := strings.SplitN(b.Text, "\n", 2)
	block := Block{
		Kind:   uievent.KindNotice,
		Header: Header{Label: "notice", Detail: lines[0], Role: theme.RoleInfo},
	}
	if len(lines) > 1 {
		block.Body = strings.Split(lines[1], "\n")
		block.Collapsible = true
	}
	return block
}

// hookBlockValue renders one lifecycle-hook execution: which program fired,
// for which tool and event, and its bounded/redacted input and output. A
// denied run stays uncollapsed (the operator needs to see why a call was
// blocked without an extra keypress); an advisory run collapses to its
// header, matching the reasoning block's "worth knowing it happened, not
// worth taking up space by default" contract.
func hookBlockValue(b uievent.HookBody) Block {
	program := b.Program
	if program == "" {
		program = "(unnamed hook)"
	}
	tool := b.Tool
	if tool == "" {
		tool = "(no tool)"
	}
	state, role := "ok", theme.RoleInfo
	if b.Denied {
		state, role = "blocked", theme.RoleDanger
	}
	// Split on "\n": hookRunOutput joins output and warning with a newline,
	// so a hook that both prints and warns produces multi-line Output
	// unconditionally, and Block.Body is one logical row per element -
	// an unsplit multi-line string under-counts Height() against what
	// actually renders (the same bug noticeBlockValue fixes above).
	var body []string
	// TrimRight before splitting: redactToolInput (unlike hookRunOutput)
	// does not trim its input, so a trailing newline in pretty-printed JSON
	// would otherwise become a padding-only "     " row.
	if input := strings.TrimRight(b.Input, "\n"); input != "" {
		lines := strings.Split(input, "\n")
		body = append(body, "in:  "+lines[0])
		for _, line := range lines[1:] {
			body = append(body, "     "+line)
		}
	}
	if b.Output != "" {
		lines := strings.Split(b.Output, "\n")
		body = append(body, "out: "+lines[0])
		for _, line := range lines[1:] {
			body = append(body, "     "+line)
		}
	}
	return Block{
		Kind: uievent.KindHook,
		Header: Header{
			Label:  "hook",
			Detail: fmt.Sprintf("%s (%s) -> %s", program, b.Event, tool),
			State:  state, Role: role,
		},
		Body:        body,
		Collapsible: true,
		Collapsed:   !b.Denied,
	}
}

// usageBlockValue renders the turn's token and cost accounting as one
// dim, header-less prose footer line (transcript-polish.md R6): the
// per-turn facts belong to the record - and to Dump(), so `[` and
// grep still reach them - while the live cost and context surfaces stay
// on the statusline pill and the topbar gauge. The footer keeps the
// header meta grammar: grouped token counts (render.GroupThousands)
// joined by the fixed two-column gap, cost to two decimals.
func usageBlockValue(t theme.Theme, tier theme.Tier, b uievent.UsageBody) Block {
	usage := b
	line := render.Role(t, tier, theme.RoleFGSubtle).Render(fmt.Sprintf(
		"%s in  %s out  %s cached  $%.2f",
		render.GroupThousands(int(b.InputTokens)),
		render.GroupThousands(int(b.OutputTokens)),
		render.GroupThousands(int(b.CachedTokens)),
		b.CostUSD))
	return Block{
		Kind:  uievent.KindUsage,
		Prose: true,
		Usage: &usage,
		Body:  []string{line},
	}
}

// errorHeaderDetailMax is the rune budget for the single-row error header.
// The header renderer truncates Detail to the panel width and replaces the
// tail with the clip marker; on a wrapped error chain the tail IS the cause,
// so a first line longer than this moves whole into the wrapping Body and
// the header shows the bare "error" label instead.
const errorHeaderDetailMax = 72

func errorBlockValue(b uievent.ErrorBody) Block {
	lines := strings.Split(b.Text, "\n")
	state := ""
	if b.Fatal {
		state = "fatal"
	}
	detail := lines[0]
	body := lines[1:]
	if len([]rune(detail)) > errorHeaderDetailMax {
		detail = ""
		body = lines
	}
	return Block{
		Kind:   uievent.KindError,
		Header: Header{Label: "error", Detail: detail, State: state, Role: theme.RoleDanger},
		Body:   body,
	}
}
