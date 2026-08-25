package transcript

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// progressBarWidth is the cell count of the subagent progress bar, from
// the wireframes-panes.md section 4 drawing.
const progressBarWidth = 30

// proseLines splits assistant prose into LOGICAL lines. It deliberately
// does not wrap: Block.bodyRows wraps at render time, so prose reflows
// when the terminal is resized. Wrapping here would freeze the measure
// taken at push time, and a later shrink would leave rows wider than the
// terminal that Height could not account for.
func proseLines(text string) []string {
	return strings.Split(text, "\n")
}

// userLines renders the user's turn: the accent marker on the first row,
// continuation rows indented two columns under it (wireframes-panes.md
// section 4).
//
// On colour tiers every row - a blank padding row above and below
// included - carries the RoleBGSelection background across the FULL
// terminal width, including under the message text itself: CSS padding
// plus a full-bleed fill, not a content-width bubble. (The old CLI's
// full-width bar, internal/cli/msgcard.go, was walked back for a
// different reason - it burned a whole row on a timestamp alone; this
// fill carries the message text itself on every row, so that tradeoff
// does not apply here.) RoleBGSelection rather than RoleBGInset: the inset
// background already marks the approval prompt and the dialogs, and a
// user message is a quotation, not a raised surface - the selection
// background is this theme set's other validated low-lift fill, and no
// selected-row chrome ever renders beside a transcript message, so the
// double duty is never ambiguous on screen.
func userLines(t theme.Theme, tier theme.Tier, width int, input string) []string {
	display := formatUserDisplay(tier, input)
	// The marker occupies two columns, so the text measure is that much
	// narrower and continuations align under the first character.
	wrapped := render.Wrap(display, render.ProseMeasure(width)-2)
	marker := render.Role(t, tier, theme.RoleAccent).Render("> ")
	out := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		if i == 0 {
			out = append(out, marker+line)
			continue
		}
		out = append(out, "  "+line)
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

// outputLines is the body a tool.output event contributes. Progress
// events carry their log; a chunk carries its own lines. Every line is
// kept: discarding all but the first was a silent truncation.
func outputLines(b uievent.ToolOutputBody) []string {
	if b.Progress != nil {
		p := b.Progress
		if bar := render.ProgressBar(progressBarWidth, p.Step, p.TotalSteps); bar != "" {
			return append([]string{bar}, p.Log...)
		}
		return p.Log
	}
	if b.Chunk == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(b.Chunk, "\n"), "\n")
}

func toolOutputBlock(t theme.Theme, tier theme.Tier, b uievent.ToolOutputBody) Block {
	if b.Progress != nil {
		p := b.Progress
		// The bar leads the body, above the step log
		// (wireframes-panes.md section 4).
		body := progressBody(t, tier, *p)
		progressCopy := *p
		return Block{
			Kind:     uievent.KindToolOutput,
			Progress: &progressCopy,
			Header: Header{
				Label:  "subagent",
				Meta:   fmt.Sprintf("%d of %d", p.Step, p.TotalSteps),
				State:  p.Status,
				Role:   theme.RoleInfo,
				Detail: fmt.Sprintf("%.0fs", p.ElapsedSeconds),
			},
			Body: body,
		}
	}
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

// progressBody is the styled body of a subagent progress block: the bar
// above its step log (wireframes-panes.md section 4). It is shared by
// the push path and the theme re-render so the two cannot diverge.
func progressBody(t theme.Theme, tier theme.Tier, p uievent.Progress) []string {
	body := make([]string, 0, len(p.Log)+1)
	if bar := render.ProgressBar(progressBarWidth, p.Step, p.TotalSteps); bar != "" {
		body = append(body, render.Role(t, tier, theme.RoleFGSubtle).Render(bar))
	}
	return append(body, p.Log...)
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

	detail, body, coll := render.FormatToolOutputWithContext(t, tier, b.Name, args, summary, b.OK, w)
	if detail == "" {
		lines := strings.Split(summary, "\n")
		detail = lines[0]
	}
	if noticeLine != "" {
		body = append(body, noticeLine)
	}

	blk := Block{
		Kind:   uievent.KindToolEnd,
		Args:   args,
		CallID: b.ToolCallID,
		Header: Header{
			Label: b.Name, Detail: detail,
			Meta: fmt.Sprintf("%dms", b.DurationMS), State: status, Role: role,
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
		blk.Header.Meta = fmt.Sprintf("%dms", b.DurationMS)
		blk.Body = render.FormatDiffLines(t, tier, w, *b.Diff)
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

func errorBlockValue(b uievent.ErrorBody) Block {
	lines := strings.Split(b.Text, "\n")
	state := ""
	if b.Fatal {
		state = "fatal"
	}
	return Block{
		Kind:   uievent.KindError,
		Header: Header{Label: "error", Detail: lines[0], State: state, Role: theme.RoleDanger},
		Body:   lines[1:],
	}
}
