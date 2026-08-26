package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// FormatToolOutput formats raw tool outputs (commands, grep searches, file reads, JSON payloads)
// into clean, structured, readable transcript lines styled through theme roles.
func FormatToolOutput(t theme.Theme, tier theme.Tier, name, output string, ok bool, width int) (detail string, body []string, collapsible bool) {
	return FormatToolOutputWithContext(t, tier, name, nil, output, ok, width)
}

// FormatToolOutputWithContext formats raw tool outputs using both tool name and parsed tool arguments.
func FormatToolOutputWithContext(t theme.Theme, tier theme.Tier, name string, args map[string]any, output string, ok bool, width int) (detail string, body []string, collapsible bool) {
	if output == "" {
		return "", nil, false
	}
	lower := strings.ToLower(name)

	switch {
	case isCommandTool(lower):
		body, collapsible = FormatCommandOutput(t, tier, output, ok, width)
		return "", body, collapsible
	case isLedgerTool(lower):
		summary, body := FormatLedgerOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isDispatchTool(lower):
		summary, body := FormatDispatchTasksOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isRunEventsTool(lower):
		summary, body := FormatRunEventsOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isOrchestrationRunTool(lower):
		summary, body := FormatOrchestrationRunOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isInspectRepositoryTool(lower):
		summary, body := FormatInspectRepositoryOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isDiagnosticsTool(lower):
		body = FormatDiagnosticsOutput(t, tier, output, width)
		return "", body, len(body) > 6
	case isMemoryTool(lower):
		summary, body := FormatMemoryOutput(t, tier, output, width)
		return summary, body, len(body) > 4
	case isWorkflowTool(lower):
		summary, body := FormatWorkflowOutput(t, tier, output, width)
		return summary, body, len(body) > 4
	case isSearchTool(lower):
		query := extractQuery(args)
		summary, body := FormatGrepOutputWithContext(t, tier, query, output, width)
		return summary, body, len(body) > 6
	case isListDirTool(lower):
		summary, body := FormatListDirOutput(t, tier, output, width)
		return summary, body, len(body) > 6
	case isReadTool(lower):
		filePath, startLine := extractFileReadArgs(args, output)
		body, collapsible = FormatFileReadOutputWithContext(t, tier, filePath, startLine, output, width)
		return "", body, collapsible
	case isMessagingTool(lower):
		summary, body, collapsible := FormatMessagingOutput(t, tier, name, args, output, width)
		return summary, body, collapsible
	case isJSONPayload(output):
		body = FormatJSONOutput(t, tier, output, width)
		return "", body, len(body) > 6
	default:
		rawLines := strings.Split(output, "\n")
		return "", rawLines, len(rawLines) > 8
	}
}

func isCommandTool(lower string) bool {
	return lower == "run_command" || lower == "bash" || lower == "terminal" || lower == "exec" || lower == "command"
}

func isLedgerTool(lower string) bool {
	return strings.Contains(lower, "ledger") || lower == "read_output"
}

func isDispatchTool(lower string) bool {
	return lower == "dispatch_tasks"
}

func isRunEventsTool(lower string) bool {
	return lower == "list_run_events"
}

func isOrchestrationRunTool(lower string) bool {
	return lower == "inspect_agents" || lower == "spawn_agent" || lower == "join_run"
}

func isInspectRepositoryTool(lower string) bool {
	return lower == "inspect_repository"
}

func isDiagnosticsTool(lower string) bool {
	return strings.Contains(lower, "diagnostic")
}

func isMemoryTool(lower string) bool {
	return strings.Contains(lower, "memory")
}

func isWorkflowTool(lower string) bool {
	return strings.Contains(lower, "workflow")
}

func isSearchTool(lower string) bool {
	return strings.Contains(lower, "grep") || strings.Contains(lower, "find") || strings.Contains(lower, "glob") || strings.Contains(lower, "symbol")
}

func isListDirTool(lower string) bool {
	return lower == "list_dir"
}

func isReadTool(lower string) bool {
	return lower == "read_file" || lower == "view_file"
}

func isJSONPayload(s string) bool {
	trimmed := strings.TrimSpace(s)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

func extractQuery(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, k := range []string{"pattern", "query", "Query", "symbol"} {
		if v, ok := args[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func extractFileReadArgs(args map[string]any, output string) (string, int) {
	filePath := ""
	startLine := 1

	if len(args) > 0 {
		for _, k := range []string{"file_path", "path", "AbsolutePath", "TargetFile", "target_file", "filePath", "filename"} {
			if v, ok := args[k].(string); ok && v != "" {
				filePath = v
				break
			}
		}
		if s, ok := args["StartLine"]; ok {
			if n, ok := s.(float64); ok && n > 0 {
				startLine = int(n)
			} else if n, ok := s.(int); ok && n > 0 {
				startLine = n
			}
		}
	}

	return filePath, startLine
}

// FormatCommandOutput formats command stdout/stderr. Long successful logs are tailed;
// failure logs highlight error/panic lines.
// unifiedDiffHunkHeader matches a real unified-diff hunk header
// ("@@ -12,3 +12,4 @@..."). Its presence is the gate for diff-line
// coloring in FormatCommandOutput: a bare '+'/'-' prefix is far too common
// in ordinary command output (git log --stat's "file.go | 3 +--", a
// markdown list, a numeric delta) to color on its own, but this exact
// shape is specific to a real diff hunk and does not occur by accident.
var unifiedDiffHunkHeader = regexp.MustCompile(`^@@ -\d+(,\d+)? \+\d+(,\d+)? @@`)

// colorizeUnifiedDiff renders a literal `git diff`-shaped command output
// (run_command's raw stdout, not a structured uievent.Diff - see diff.go
// for that path) with the same added/removed styling DiffLines already
// applies to structured diffs. It returns ok=false, leaving the caller's
// existing formatting untouched, unless at least one real hunk header is
// present: only lines AFTER a detected hunk header are treated as
// add/remove content, so a file header's own "--- a/x"/"+++ b/x" lines
// (which also start with a diff-shaped prefix) are never miscolored as
// removed/added content.
func colorizeUnifiedDiff(t theme.Theme, tier theme.Tier, lines []string) ([]string, bool) {
	// The gate requires a diff --git / --- / +++ file header directly
	// preceding the first hunk header, not just a @@ ... @@-shaped
	// substring anywhere in the output: a hunk header alone is easy to
	// find inside unrelated text (a log message quoting diff syntax, a
	// docs excerpt), and coloring an entire unrelated command's output
	// off one coincidental match is worse than not coloring a genuine
	// diff that lacks a file header for some reason.
	hasRealDiffShape := false
	for i, l := range lines {
		if !unifiedDiffHunkHeader.MatchString(l) {
			continue
		}
		for j := i - 1; j >= 0 && j >= i-4; j-- {
			if strings.HasPrefix(lines[j], "diff --git ") || strings.HasPrefix(lines[j], "+++ ") {
				hasRealDiffShape = true
				break
			}
		}
		if hasRealDiffShape {
			break
		}
	}
	if !hasRealDiffShape {
		return nil, false
	}

	hunkHdr := Role(t, tier, theme.RoleDiffHunk)
	fileHdr := Role(t, tier, theme.RoleFGSubtle)
	addSt := WithBg(Role(t, tier, theme.RoleDiffAddFG), t, tier, theme.RoleDiffAddBG)
	delSt := WithBg(Role(t, tier, theme.RoleDiffDelFG), t, tier, theme.RoleDiffDelBG)

	// A content line's own first character can legitimately be '-'/'+'
	// (e.g. removing/adding a line that reads "-- old item"), which
	// collides with the "--- "/"+++ " file-header prefix. Only treat a
	// line as a file header when NOT already inside a hunk - a file
	// header only ever appears BETWEEN hunks/files, never as diff
	// content - so a real content line is never misread as one.
	inHunk := false
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		switch {
		case unifiedDiffHunkHeader.MatchString(l):
			inHunk = true
			out = append(out, hunkHdr.Render(l))
		case !inHunk && (strings.HasPrefix(l, "diff --git ") || strings.HasPrefix(l, "index ") ||
			strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ")):
			out = append(out, fileHdr.Render(l))
		case strings.HasPrefix(l, "diff --git "):
			// A new file's header always ends the previous file's hunk,
			// even mid-hunk-tracking, so the next hunk header is read
			// correctly.
			inHunk = false
			out = append(out, fileHdr.Render(l))
		case inHunk && strings.HasPrefix(l, "+"):
			out = append(out, addSt.Render(l))
		case inHunk && strings.HasPrefix(l, "-"):
			out = append(out, delSt.Render(l))
		default:
			out = append(out, l)
		}
	}
	return out, true
}

func FormatCommandOutput(t theme.Theme, tier theme.Tier, output string, ok bool, width int) ([]string, bool) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if diffLines, isDiff := colorizeUnifiedDiff(t, tier, lines); isDiff {
		return diffLines, len(diffLines) > 8
	}
	if len(lines) <= 6 {
		return lines, false
	}

	subtle := Role(t, tier, theme.RoleFGSubtle)
	danger := Role(t, tier, theme.RoleDanger)

	if !ok {
		var out []string
		for _, l := range lines {
			lower := strings.ToLower(l)
			if strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "panic") || strings.Contains(lower, "fatal") {
				out = append(out, danger.Render(l))
			} else {
				out = append(out, l)
			}
		}
		return out, true
	}

	if len(lines) > 8 {
		hidden := len(lines) - 5
		var out []string
		out = append(out, lines[0])
		out = append(out, subtle.Render(fmt.Sprintf("... %d lines omitted ...", hidden)))
		out = append(out, lines[len(lines)-4:]...)
		return out, true
	}

	return lines, true
}

type grepJSONMatch struct {
	File        string `json:"File"`
	LineNumber  int    `json:"LineNumber"`
	LineContent string `json:"LineContent"`
}

// FormatGrepOutput groups grep/search results by file.
func FormatGrepOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	return FormatGrepOutputWithContext(t, tier, "", output, width)
}

// FormatGrepOutputWithContext groups grep/search results by file supporting both NDJSON and standard ripgrep output.
func FormatGrepOutputWithContext(t theme.Theme, tier theme.Tier, query, output string, width int) (string, []string) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	var matches []grepJSONMatch

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			var m grepJSONMatch
			if err := json.Unmarshal([]byte(trimmed), &m); err == nil && m.File != "" {
				matches = append(matches, m)
				continue
			}
		}
		// Fallback to path:line:content standard format
		parts := strings.SplitN(trimmed, ":", 3)
		if len(parts) >= 3 {
			if lineNum, err := strconv.Atoi(parts[1]); err == nil && lineNum > 0 {
				matches = append(matches, grepJSONMatch{
					File:        parts[0],
					LineNumber:  lineNum,
					LineContent: parts[2],
				})
			}
		}
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	muted := Role(t, tier, theme.RoleFGMuted)

	if len(matches) > 0 {
		fileGroups := make(map[string][]grepJSONMatch)
		var fileOrder []string
		for _, m := range matches {
			if _, exists := fileGroups[m.File]; !exists {
				fileOrder = append(fileOrder, m.File)
			}
			fileGroups[m.File] = append(fileGroups[m.File], m)
		}

		var out []string
		for _, f := range fileOrder {
			group := fileGroups[f]
			out = append(out, accent.Render("• "+f)+" "+subtle.Render(fmt.Sprintf("(%d matches)", len(group))))
			for _, m := range group {
				content := strings.TrimSpace(m.LineContent)
				if ansi.StringWidth(content) > width-12 && width > 16 {
					content = ansi.Truncate(content, width-12, "…")
				}
				numStr := fmt.Sprintf("L%-4d", m.LineNumber)
				out = append(out, "  "+muted.Render(numStr)+" "+content)
			}
		}
		summary := fmt.Sprintf("%d matches in %d files", len(matches), len(fileOrder))
		return summary, out
	}

	return "", lines
}

// FormatListDirOutput formats directory listings into clean tree rows.
func FormatListDirOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 0 {
		return "", nil
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	fg := Role(t, tier, theme.RoleFG)

	var out []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(trimmed, "/") {
			out = append(out, accent.Render("📁 ")+fg.Render(trimmed))
		} else {
			out = append(out, subtle.Render("  • ")+fg.Render(trimmed))
		}
	}
	summary := fmt.Sprintf("%d entries", len(out))
	return summary, out
}

// FormatFileReadOutput previews file content with line numbers when long.
func FormatFileReadOutput(t theme.Theme, tier theme.Tier, output string, width int) ([]string, bool) {
	return FormatFileReadOutputWithContext(t, tier, "", 1, output, width)
}

// FormatFileReadOutputWithContext previews file content with syntax highlighting and line numbers.
func FormatFileReadOutputWithContext(t theme.Theme, tier theme.Tier, filePath string, startLine int, output string, width int) ([]string, bool) {
	content := output
	// Parse window header if present e.g. "[path lines 10-20 of 100]"
	if strings.HasPrefix(content, "[") && strings.Contains(content, "lines ") {
		idx := strings.Index(content, "]\n")
		if idx != -1 {
			hdr := content[1:idx]
			content = content[idx+2:]
			if parts := strings.Split(hdr, " "); len(parts) >= 3 {
				if filePath == "" {
					filePath = parts[0]
				}
				rangeStr := parts[2]
				if dash := strings.Index(rangeStr, "-"); dash != -1 {
					if n, err := strconv.Atoi(rangeStr[:dash]); err == nil {
						startLine = n
					}
				}
			}
		}
	}

	rawLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(rawLines) == 0 {
		return nil, false
	}
	if len(rawLines) == 1 && (strings.HasSuffix(rawLines[0], " lines") || strings.HasSuffix(rawLines[0], " line") || strings.HasSuffix(rawLines[0], " bytes")) {
		return rawLines, false
	}

	// Apply Chroma syntax highlighting
	highlighted := HighlightCode(t, tier, filePath, content)
	if len(highlighted) != len(rawLines) {
		highlighted = rawLines
	}

	subtle := Role(t, tier, theme.RoleFGSubtle)
	muted := Role(t, tier, theme.RoleFGMuted)

	if len(highlighted) <= 6 {
		var out []string
		for i, line := range highlighted {
			num := muted.Render(fmt.Sprintf("%4d │ ", startLine+i))
			out = append(out, num+line)
		}
		return out, false
	}

	var out []string
	limit := min(4, len(highlighted))
	for i := 0; i < limit; i++ {
		num := muted.Render(fmt.Sprintf("%3d │ ", startLine+i))
		out = append(out, num+highlighted[i])
	}
	if len(highlighted) > limit {
		omitted := len(highlighted) - limit
		out = append(out, subtle.Render(fmt.Sprintf("    │ ... %d more lines ...", omitted)))
	}
	return out, true
}

type memoryItem struct {
	ID      string   `json:"id"`
	Scope   string   `json:"scope"`
	Created string   `json:"created"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}

// FormatMemoryOutput renders memory search/list results into clean memory cards.
func FormatMemoryOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := strings.TrimSpace(output)
	var items []memoryItem

	if strings.HasPrefix(trimmed, "[") {
		_ = json.Unmarshal([]byte(trimmed), &items)
	} else if strings.HasPrefix(trimmed, "{") {
		var single memoryItem
		if err := json.Unmarshal([]byte(trimmed), &single); err == nil && single.Summary != "" {
			items = []memoryItem{single}
		}
	}

	if len(items) == 0 {
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	fg := Role(t, tier, theme.RoleFG)

	var out []string
	for _, item := range items {
		id := item.ID
		if len(id) > 8 {
			id = id[:8]
		}
		header := "•"
		if item.Scope != "" {
			header += " [" + item.Scope + "]"
		}
		if id != "" {
			header += " " + id
		}
		line1 := accent.Render(header)
		if item.Created != "" {
			line1 += " " + subtle.Render("("+item.Created+")")
		}
		out = append(out, line1)

		summary := strings.TrimSpace(item.Summary)
		if width > 8 && ansi.StringWidth(summary) > width-4 {
			summary = ansi.Truncate(summary, width-4, "…")
		}
		if summary != "" {
			out = append(out, "  "+fg.Render(summary))
		}

		if len(item.Tags) > 0 {
			out = append(out, "  "+subtle.Render("tags: "+strings.Join(item.Tags, ", ")))
		}
	}

	summary := fmt.Sprintf("%d memory item", len(items))
	if len(items) != 1 {
		summary += "s"
	}
	return summary, out
}

// FormatJSONOutput formats JSON objects or arrays into readable key-value summary lines or syntax-highlighted json.
func FormatJSONOutput(t theme.Theme, tier theme.Tier, output string, width int) []string {
	trimmed := strings.TrimSpace(output)
	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)

	// Object format (sorted key-values)
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var out []string
		for _, k := range keys {
			valStr := fmt.Sprintf("%v", obj[k])
			if width > 16 && ansi.StringWidth(valStr) > width-20 {
				valStr = ansi.Truncate(valStr, width-20, "…")
			}
			out = append(out, accent.Render(k)+": "+subtle.Render(valStr))
		}
		if len(out) > 0 {
			return out
		}
	}

	// Array format
	var arr []any
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
		var out []string
		for i, item := range arr {
			if m, ok := item.(map[string]any); ok {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				var parts []string
				for _, k := range keys {
					parts = append(parts, accent.Render(k)+": "+subtle.Render(fmt.Sprintf("%v", m[k])))
				}
				out = append(out, subtle.Render(fmt.Sprintf("[%d] ", i+1))+strings.Join(parts, "  "))
			} else {
				out = append(out, subtle.Render(fmt.Sprintf("[%d] ", i+1))+fmt.Sprintf("%v", item))
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(trimmed), "", "  "); err == nil {
		highlighted := HighlightCode(t, tier, "json", pretty.String())
		if len(highlighted) <= 8 {
			return highlighted
		}
		var out []string
		out = append(out, highlighted[:6]...)
		out = append(out, subtle.Render(fmt.Sprintf("... (+%d lines)", len(highlighted)-6)))
		return out
	}

	return strings.Split(strings.TrimRight(output, "\n"), "\n")
}

// FormatDiagnosticsOutput formats compiler / linter diagnostic lines with severity markers.
func FormatDiagnosticsOutput(t theme.Theme, tier theme.Tier, output string, width int) []string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	danger := Role(t, tier, theme.RoleDanger)
	warn := Role(t, tier, theme.RoleWarning)

	var out []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "error") || strings.Contains(lower, "syntax error") {
			out = append(out, danger.Render("✖ ")+l)
		} else if strings.Contains(lower, "warn") {
			out = append(out, warn.Render("⚠ ")+l)
		} else {
			out = append(out, "  "+l)
		}
	}
	return out
}

type workflowPayload struct {
	Workflow string   `json:"workflow"`
	Status   string   `json:"status"`
	Steps    []string `json:"steps"`
}

// FormatWorkflowOutput formats workflow execution results into summary and step items.
func FormatWorkflowOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := strings.TrimSpace(output)
	var wf workflowPayload
	if err := json.Unmarshal([]byte(trimmed), &wf); err != nil || wf.Workflow == "" {
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
	}

	accent := Role(t, tier, theme.RoleAccent)
	subtle := Role(t, tier, theme.RoleFGSubtle)
	fg := Role(t, tier, theme.RoleFG)

	summary := "workflow " + wf.Status
	var out []string
	out = append(out, accent.Render("• workflow: ")+fg.Render(wf.Workflow))
	if wf.Status != "" {
		out = append(out, subtle.Render("  status: ")+wf.Status)
	}
	if len(wf.Steps) > 0 {
		out = append(out, subtle.Render("  steps:"))
		for _, s := range wf.Steps {
			out = append(out, "    - "+s)
		}
	}
	return summary, out
}

// shortenRef shortens a 'kind:sub:digest' content reference (ref:output:...,
// ref:error:...) to an 8-hex-char digest for display, leaving anything that
// doesn't match the three-part shape unchanged. Shared by every renderer
// that displays a content reference, so the shortening rule stays in one
// place.
func shortenRef(ref string) string {
	parts := strings.Split(ref, ":")
	if len(parts) >= 3 && len(parts[2]) > 8 {
		return fmt.Sprintf("%s:%s:%s", parts[0], parts[1], parts[2][:8])
	}
	return ref
}

// humanBytes renders a byte count as bytes or kilobytes, matching the
// precision every content-size display in this package already uses.
func humanBytes(n int64) string {
	if n >= 1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024.0)
	}
	return fmt.Sprintf("%d B", n)
}

// dispatchTaskResultView is the subset of dispatch_tasks' per-task result
// envelope (internal/cliorchestrate/dispatch.go's dispatchTaskResult) this
// renderer needs. Kept narrow and independent of that package - the UI
// layer must not import the orchestration package (mivia-ui isolation,
// INV-TUI-29) - and tolerant of fields it doesn't recognize.
type dispatchTaskResultView struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Agent     string `json:"agent"`
	Elapsed   string `json:"elapsed"`
	Synopsis  string `json:"synopsis"`
	Error     string `json:"error"`
	OutputRef string `json:"output_ref"`
	ErrorRef  string `json:"error_ref"`
}

// maxDispatchTaskRows caps how many per-task lines FormatDispatchTasksOutput
// renders before collapsing the rest into a summary tail, matching the
// truncate-with-notice idiom FormatCommandOutput already uses for long
// output.
const maxDispatchTaskRows = 6

// FormatDispatchTasksOutput formats a dispatch_tasks result (a JSON array of
// per-task envelopes) into one line per task instead of the raw array a
// generic JSON dump would otherwise produce.
func FormatDispatchTasksOutput(t theme.Theme, tier theme.Tier, output string, width int) (string, []string) {
	trimmed := strings.TrimSpace(output)
	var tasks []dispatchTaskResultView
	if err := json.Unmarshal([]byte(trimmed), &tasks); err != nil || len(tasks) == 0 {
		return "", strings.Split(strings.TrimRight(output, "\n"), "\n")
	}

	ok, failed := 0, 0
	for _, task := range tasks {
		if strings.EqualFold(task.Status, "completed") {
			ok++
		} else {
			failed++
		}
	}
	summary := fmt.Sprintf("%d tasks · %d completed, %d failed", len(tasks), ok, failed)
	return summary, renderTaskResultRows(t, tier, tasks, width, maxDispatchTaskRows, "more tasks")
}

// renderTaskResultRows renders one line per task-result-shaped entry
// (task_id, status, agent/elapsed if present, a synopsis/error snippet,
// and a shortened output/error ref), capping at maxRows and collapsing the
// rest into a "… N <tailNoun>" line. Shared by FormatDispatchTasksOutput
// and the orchestration run formatter (inspect_agents/spawn_agent/
// join_run), whose task_results entries use the same field names.
// taskResultRowStatusIcon maps a task-result status to an icon/role. Beyond
// dispatch_tasks' own completed/failed vocabulary, the orchestration family
// (inspect_agents/spawn_agent/join_run) reuses this with ledger.TaskStatus*
// values (running, blocked, timed_out, canceled, ...), so in-progress
// states get a neutral warning marker instead of being lumped in with
// failures.
func taskResultRowStatusIcon(t theme.Theme, tier theme.Tier, status string) (string, lipgloss.Style) {
	switch strings.ToLower(status) {
	case "completed":
		return "✔", Role(t, tier, theme.RoleSuccess)
	case "failed", "timed_out", "canceled", "interrupted_unrecoverable":
		return "✖", Role(t, tier, theme.RoleDanger)
	case "running", "blocked", "pending", "queued", "retry_pending", "retry_queued", "cancel_requested":
		return "⋯", Role(t, tier, theme.RoleWarning)
	default:
		return "✖", Role(t, tier, theme.RoleDanger)
	}
}

func renderTaskResultRows(t theme.Theme, tier theme.Tier, tasks []dispatchTaskResultView, width, maxRows int, tailNoun string) []string {
	subtle := Role(t, tier, theme.RoleFGSubtle)
	fg := Role(t, tier, theme.RoleFG)

	rows := tasks
	var tail string
	if len(rows) > maxRows {
		tail = fmt.Sprintf("… %d %s", len(rows)-maxRows, tailNoun)
		rows = rows[:maxRows]
	}

	var out []string
	for _, task := range rows {
		icon, iconRole := taskResultRowStatusIcon(t, tier, task.Status)
		line := iconRole.Render(icon) + " " + fg.Render(task.TaskID)
		if task.Agent != "" {
			line += subtle.Render(" agent=" + task.Agent)
		}
		if task.Elapsed != "" {
			line += subtle.Render(" " + task.Elapsed)
		}
		snippet := task.Synopsis
		if snippet == "" {
			snippet = task.Error
		}
		if snippet != "" {
			line += "  " + ansi.Truncate(snippet, max(width-40, 20), "…")
		}
		if ref := task.ErrorRef; ref != "" {
			line += subtle.Render(" [" + shortenRef(ref) + "]")
		} else if ref := task.OutputRef; ref != "" {
			line += subtle.Render(" [" + shortenRef(ref) + "]")
		}
		out = append(out, line)
	}
	if tail != "" {
		out = append(out, subtle.Render(tail))
	}
	return out
}
