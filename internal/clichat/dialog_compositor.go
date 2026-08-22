package clichat

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const sgrReset = "\x1b[0m"

type sgrState struct {
	values map[string]string
}

func newSGRState() sgrState { return sgrState{values: make(map[string]string)} }

func (s sgrState) reset() { clear(s.values) }

func (s sgrState) set(key, value string) { s.values[key] = value }

func (s sgrState) prefix() string {
	keys := []string{"intensity", "italic", "underline", "blink", "inverse", "conceal", "strike", "fg", "bg"}
	var b strings.Builder
	for _, key := range keys {
		if value := s.values[key]; value != "" {
			b.WriteString("\x1b[")
			b.WriteString(value)
			b.WriteByte('m')
		}
	}
	return b.String()
}

func applySGR(state *sgrState, params string) {
	if params == "" {
		state.reset()
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		code, err := strconv.Atoi(parts[i])
		if err != nil {
			continue
		}
		switch {
		case code == 0:
			state.reset()
		case code == 1 || code == 2:
			state.set("intensity", parts[i])
		case code == 22:
			delete(state.values, "intensity")
		case code == 3:
			state.set("italic", parts[i])
		case code == 23:
			delete(state.values, "italic")
		case code == 4:
			state.set("underline", parts[i])
		case code == 24:
			delete(state.values, "underline")
		case code == 5 || code == 6:
			state.set("blink", parts[i])
		case code == 25:
			delete(state.values, "blink")
		case code == 7:
			state.set("inverse", parts[i])
		case code == 27:
			delete(state.values, "inverse")
		case code == 8:
			state.set("conceal", parts[i])
		case code == 28:
			delete(state.values, "conceal")
		case code == 9:
			state.set("strike", parts[i])
		case code == 29:
			delete(state.values, "strike")
		case code == 30 || code == 31 || code == 32 || code == 33 || code == 34 || code == 35 || code == 36 || code == 37 || code == 90 || code == 91 || code == 92 || code == 93 || code == 94 || code == 95 || code == 96 || code == 97:
			state.set("fg", parts[i])
		case code == 39:
			delete(state.values, "fg")
		case code == 40 || code == 41 || code == 42 || code == 43 || code == 44 || code == 45 || code == 46 || code == 47 || code == 100 || code == 101 || code == 102 || code == 103 || code == 104 || code == 105 || code == 106 || code == 107:
			state.set("bg", parts[i])
		case code == 49:
			delete(state.values, "bg")
		case code == 38 || code == 48:
			if i+2 < len(parts) && (parts[i+1] == "5" || parts[i+1] == "2") {
				key := "fg"
				if code == 48 {
					key = "bg"
				}
				end := Min(len(parts), i+3)
				if parts[i+1] == "2" {
					end = Min(len(parts), i+5)
				}
				state.set(key, strings.Join(parts[i:end], ";"))
				i = end - 1
			}
		}
	}
}

func sgrBefore(line string) sgrState {
	state := newSGRState()
	for i := 0; i < len(line); {
		if line[i] != '\x1b' || i+2 >= len(line) || line[i+1] != '[' {
			i++
			continue
		}
		end := strings.IndexByte(line[i+2:], 'm')
		if end < 0 {
			break
		}
		end += i + 2
		applySGR(&state, line[i+2:end])
		i = end + 1
	}
	return state
}

// sliceANSI returns a cell range with active SGR re-emitted at the left seam
// and a reset at the right seam. x/ansi performs the grapheme-aware slicing.
func sliceANSI(line string, left, right int) string {
	if right <= left || right <= 0 {
		return ""
	}
	left = Max(0, left)
	prefix := ansi.Cut(line, 0, left)
	part := ansi.Cut(line, left, right)
	if part == "" && ansi.StringWidth(line) > left {
		part = ansi.Cut(line, left, left+1)
	}
	if part == "" {
		return ""
	}
	return sgrBefore(prefix).prefix() + part + sgrReset
}

func normalizeCanvas(s string, termW, termH int) []string {
	if termW <= 0 || termH <= 0 {
		return nil
	}
	raw := strings.Split(s, "\n")
	rows := make([]string, termH)
	for i := range rows {
		line := ""
		if i < len(raw) {
			line = raw[i]
		}
		rows[i] = FitDialogRow(line, termW)
	}
	return rows
}

// OverlayAt composites panel onto base at panelRect, clamped to the terminal
// bounds. Shared with internal/legacytui's view compositor.
func OverlayAt(base, panel string, panelRect Rect, termW, termH int) string {
	baseRows := normalizeCanvas(base, termW, termH)
	if termW <= 0 || termH <= 0 {
		return ""
	}
	panelRows := normalizeCanvas(panel, Max(0, panelRect.W), Max(0, panelRect.H))
	for y := Max(0, panelRect.Y); y < Min(termH, panelRect.Y+panelRect.H); y++ {
		localY := y - panelRect.Y
		if localY < 0 || localY >= len(panelRows) {
			continue
		}
		x0, x1 := Max(0, panelRect.X), Min(termW, panelRect.X+panelRect.W)
		if x1 <= x0 {
			continue
		}
		localX0 := x0 - panelRect.X
		localX1 := localX0 + (x1 - x0)
		row := baseRows[y]
		baseRows[y] = sliceANSI(row, 0, x0) + sliceANSI(panelRows[localY], localX0, localX1) + sliceANSI(row, panelRect.X+panelRect.W, termW)
		baseRows[y] = normalizeCanvas(baseRows[y], termW, 1)[0]
	}
	return strings.Join(baseRows, "\n")
}

// FitDialogRow pads or truncates row to exactly width display columns.
// Shared with internal/legacytui's dialog frame renderer.
func FitDialogRow(row string, width int) string {
	if width <= 0 {
		return ""
	}
	originalWidth := ansi.StringWidth(row)
	row = sliceANSI(row, 0, width)
	if row == "" && originalWidth > 0 {
		row = "�"
	}
	if ansi.StringWidth(row) > width {
		row = strings.Repeat("�", width)
	}
	if n := ansi.StringWidth(row); n < width {
		row += strings.Repeat(" ", width-n)
	}
	return row
}

// RenderDialogFrame owns the shared exact-width frame for block and sessions
// dialogs. frameRows=2 means title/page/footer-bottom; frameRows=3 adds an
// explicit footer row before the bottom border for sessions.
func RenderDialogFrame(title string, rows []string, footer string, layout DialogLayout) string {
	w, h := layout.Rect.W, layout.Rect.H
	if w <= 0 || h <= 0 {
		return ""
	}
	if layout.borderless {
		out := make([]string, h)
		for i := range out {
			if i < len(rows) {
				out[i] = FitDialogRow(rows[i], w)
			} else {
				out[i] = strings.Repeat(" ", w)
			}
		}
		return strings.Join(out, "\n")
	}
	inner := Max(0, w-layout.FrameCols)
	top := "┌─" + FitDialogRow(title, inner) + "─┐"
	var out []string
	out = append(out, top)
	pageRows := Max(0, h-layout.FrameRows)
	for i := 0; i < pageRows; i++ {
		row := ""
		if i < len(rows) {
			row = rows[i]
		}
		out = append(out, "│ "+FitDialogRow(row, inner)+" │")
	}
	if layout.FrameRows >= 3 {
		out = append(out, "│ "+FitDialogRow(footer, inner)+" │")
	}
	bottom := footer
	if layout.FrameRows >= 3 {
		bottom = ""
	}
	out = append(out, "└─"+FitDialogRow(bottom, inner)+"─┘")
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	if len(out) > h {
		out = out[:h]
	}
	for i := range out {
		if ansi.StringWidth(out[i]) != w {
			out[i] = FitDialogRow(out[i], w)
		}
	}
	return strings.Join(out, "\n")
}
