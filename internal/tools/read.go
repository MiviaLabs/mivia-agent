package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type readFileTool struct {
	ws                   *workspace.Root
	maxBytes             int
	secretPathExceptions []string
	secretPathPatterns   []string
}

func (t *readFileTool) Capability(args json.RawMessage) Capability {
	// Capability.MaxResultBytes is deliberately NOT declared: the agent loop
	// treats it as a wire truncation bound, and the window path's honest
	// framing (header + truncation notice) rides above maxBytes. The content
	// budget feeds the dispatcher backstop via ResultBudgetBytes instead.
	return Capability{Class: ExecutionRead, ResourceKey: pathCapabilityKey(args, t.ws)}
}

// ResultBudgetBytes declares the configured content budget for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool).
func (t *readFileTool) ResultBudgetBytes() int { return t.maxBytes }

func (t *readFileTool) Name() string { return "read_file" }
func (t *readFileTool) Description() string {
	return "Read a text file by relative workspace path. " +
		"Use offset+limit for large files or excerpts. Do not pass extra fields (content, encoding, etc.)."
}
func (t *readFileTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Relative path to the file",
		},
		"offset": map[string]any{
			"type":        "integer",
			"description": "1-based line number to start reading (default 1)",
		},
		"limit": map[string]any{
			"type":        "integer",
			"description": "Max lines to return (default: all, capped by max read size)",
		},
	}, []string{"path"})
}

func (t *readFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	in.Path = strings.TrimSpace(in.Path)
	if in.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if isSecretPath(t.ws.Rel(abs), t.secretPathExceptions, t.secretPathPatterns) {
		return "", fmt.Errorf("reading secret-like path is blocked")
	}
	st, err := requireRegularFile(abs)
	if err != nil {
		return "", dropIfGone(abs, err)
	}

	// Full-file path: small files only.
	if in.Offset <= 1 && in.Limit <= 0 {
		if t.maxBytes > 0 && st.Size() > int64(t.maxBytes) {
			return "", fmt.Errorf("file too large (%d bytes; max %d). Re-call with offset and limit to read a line window", st.Size(), t.maxBytes)
		}
		data, err := readFileWithContext(ctx, abs)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("file is not valid UTF-8")
		}
		// The agent has now seen this exact state; record it so the stale-write
		// guard on a later edit compares against what was actually shown.
		refreshFileObservation(abs)
		return string(data), nil
	}

	out, err := t.readLineWindow(ctx, abs, in.Offset, in.Limit)
	if err == nil {
		// The agent saw a valid window of this state; refresh the observation
		// so a later edit is compared against what the agent was shown.
		refreshFileObservation(abs)
	}
	// A missing path that fails here must not leave its stale observation
	// behind (see dropIfGone): the agent re-read the path and learned it is
	// gone, so a later write is an informed create, not a stuck refusal.
	return out, dropIfGone(abs, err)
}

func (t *readFileTool) readLineWindow(ctx context.Context, abs string, offset, limit int) (string, error) {
	if offset < 1 {
		offset = 1
	}
	// Non-blocking open + fstat closes the TOCTOU window where a path becomes
	// a FIFO between Stat and Open.
	f, _, err := openRegularFile(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	// When maxBytes is 0 (uncapped), the scanner max token size must still be
	// large enough to handle long lines: Go's Scanner.Buffer sets
	// maxTokenSize = max(max, cap(buf)), so max=0 falls back to 64 KiB,
	// which is a regression. Use 1 MiB as the floor, matching grep's
	// hardcoded scanner max.
	scannerMax := t.maxBytes
	if scannerMax <= 0 {
		scannerMax = 1 << 20 // 1 MiB
	}
	sc.Buffer(buf, scannerMax)

	lines, totalLines, err := t.collectWindowLines(ctx, sc, offset, limit)
	if err != nil {
		if err == bufio.ErrTooLong {
			// The scanner enforces the larger of max and cap(buf)
			// (bufio.Scanner.Buffer). Report that enforced bound, not
			// t.maxBytes: for an uncapped tool t.maxBytes is 0 while the
			// enforced floor is 1 MiB, and for a small configured bound the
			// 64 KiB initial buffer cap outranks the configured value.
			return "", fmt.Errorf("line exceeds max read size (%d bytes)", max(scannerMax, cap(buf)))
		}
		return "", err
	}
	if totalLines == 0 {
		return "", nil
	}
	if offset > totalLines {
		return "", fmt.Errorf("offset %d past end of file (%d lines)", offset, totalLines)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return t.formatWindow(lines, offset, totalLines)
}

// collectWindowLines scans the file once, counting every line (totalLines)
// while collecting the requested window under the byte budget. The budget is
// enforced during collection, not deferred to formatWindow: without it a
// window call (offset>1, limit=0) on a file far larger than maxBytes collects
// every line from offset to EOF before the post-collection cap runs, making
// peak memory grow with file size instead of staying bounded by maxBytes. The
// line that trips the budget is still appended so formatWindow's per-line
// accounting (prefix + separator + b.Len()==0 case) keeps deciding between
// the truncation notice and the "line N exceeds max read size" error exactly
// as before; later lines are skipped while the scan continues so totalLines
// stays honest for the "… lines X–Y of Z" header. Peak collection is bounded
// by maxBytes plus one line (≤ the scanner's enforced max token size),
// independent of file size. maxBytes<=0 (uncapped) is untouched.
func (t *readFileTool) collectWindowLines(ctx context.Context, sc *bufio.Scanner, offset, limit int) ([]string, int, error) {
	var windowLines []string
	collectedBytes := 0
	budgetExhausted := false
	lineNo := 0
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, lineNo, err
		}
		lineNo++
		if lineNo >= offset && (limit <= 0 || len(windowLines) < limit) {
			if t.maxBytes > 0 && budgetExhausted {
				continue
			}
			line := sc.Text()
			windowLines = append(windowLines, line)
			collectedBytes += len(line)
			if t.maxBytes > 0 && collectedBytes > t.maxBytes {
				budgetExhausted = true
			}
		}
	}
	return windowLines, lineNo, sc.Err()
}

// formatWindow renders collected window lines with right-aligned line
// number prefixes (e.g. " 42 | content") and a header reporting the
// delivered range and total line count ("… lines X–Y of Z"). Prefix
// bytes are counted against maxBytes so truncation stays honest.
func (t *readFileTool) formatWindow(lines []string, offset, totalLines int) (string, error) {
	width := len(fmt.Sprintf("%d", totalLines))
	if width < 1 {
		width = 1
	}

	var b strings.Builder
	totalBytes := 0
	formatted := 0
	for i, line := range lines {
		num := offset + i
		prefix := fmt.Sprintf("%*d | ", width, num)
		need := len(prefix) + len(line) + 1
		if t.maxBytes > 0 && totalBytes+need > t.maxBytes {
			if b.Len() == 0 {
				return "", fmt.Errorf("line %d exceeds max read size (%d bytes)", num, t.maxBytes)
			}
			fmt.Fprintf(&b, "\n... truncated at max read size (%d bytes)", t.maxBytes)
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
			totalBytes++
		}
		b.WriteString(prefix)
		b.WriteString(line)
		totalBytes += len(prefix) + len(line)
		formatted++
	}

	out := b.String()
	if !utf8.ValidString(out) {
		return "", fmt.Errorf("file is not valid UTF-8")
	}
	header := fmt.Sprintf("… lines %d–%d of %d", offset, offset+formatted-1, totalLines)
	return header + "\n" + out, nil
}

// list_dir implementation lives in list_dir.go.
