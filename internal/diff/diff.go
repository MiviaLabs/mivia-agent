// Package diff provides bounded, dependency-free line diffs for tool output.
package diff

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Kind uint8

const (
	Equal Kind = iota
	Delete
	Insert
)

type Op struct {
	Kind  Kind
	Lines []string
}
type Result struct{ Ops []Op }
type Options struct {
	MaxInputBytes int
	Timeout       time.Duration
}

func Compute(oldText, newText string, opts Options) (Result, error) {
	if opts.MaxInputBytes > 0 && (len(oldText) > opts.MaxInputBytes || len(newText) > opts.MaxInputBytes) {
		return Result{}, fmt.Errorf("diff input exceeds %d bytes", opts.MaxInputBytes)
	}
	oldLines, newLines := splitLines(oldText), splitLines(newText)
	// A trailing newline is represented by an empty terminal element. It is a
	// visible insertion when added, but not a deletion when the other side has
	// continued content (the terminator is metadata for that file).
	if len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" && (len(newLines) == 0 || newLines[len(newLines)-1] != "") {
		oldLines = oldLines[:len(oldLines)-1]
	}
	deadline := time.Time{}
	if opts.Timeout > 0 {
		deadline = time.Now().Add(opts.Timeout)
	}
	if len(oldLines) > 4096 || len(newLines) > 4096 || len(oldLines)*len(newLines) > 4_000_000 {
		return Result{}, fmt.Errorf("diff input has too many lines")
	}
	rows := make([][]uint32, len(oldLines)+1)
	for i := range rows {
		rows[i] = make([]uint32, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		if expired(deadline) {
			return Result{}, fmt.Errorf("diff timed out")
		}
		for j := len(newLines) - 1; j >= 0; j-- {
			if expired(deadline) {
				return Result{}, fmt.Errorf("diff timed out")
			}
			if oldLines[i] == newLines[j] {
				rows[i][j] = rows[i+1][j+1] + 1
			} else if rows[i+1][j] >= rows[i][j+1] {
				rows[i][j] = rows[i+1][j]
			} else {
				rows[i][j] = rows[i][j+1]
			}
		}
	}
	var ops []Op
	flush := func(kind Kind, line string) {
		if len(ops) > 0 && ops[len(ops)-1].Kind == kind {
			ops[len(ops)-1].Lines = append(ops[len(ops)-1].Lines, line)
		} else {
			ops = append(ops, Op{Kind: kind, Lines: []string{line}})
		}
	}
	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		if expired(deadline) {
			return Result{}, fmt.Errorf("diff timed out")
		}
		if i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			flush(Equal, oldLines[i])
			i++
			j++
		} else if j == len(newLines) || (i < len(oldLines) && rows[i+1][j] >= rows[i][j+1]) {
			flush(Delete, oldLines[i])
			i++
		} else {
			flush(Insert, newLines[j])
			j++
		}
	}
	return Result{Ops: ops}, nil
}

func Stats(r Result) (insertions, deletions int) {
	for _, op := range r.Ops {
		if op.Kind == Insert {
			insertions += len(op.Lines)
		}
		if op.Kind == Delete {
			deletions += len(op.Lines)
		}
	}
	return
}
func FormatUnified(path string, r Result) string { return FormatUnifiedAt(path, r, 1, 1, -1) }

func FormatUnifiedAt(path string, r Result, oldStart, newStart, context int) string {
	if oldStart < 1 {
		oldStart = 1
	}
	if newStart < 1 {
		newStart = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	hunks := []diffHunk{{ops: flattenOps(r.Ops), oldStart: oldStart, newStart: newStart}}
	if context >= 0 {
		hunks = contextHunks(r.Ops, context, oldStart, newStart)
	}
	for i, h := range hunks {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeHunk(&b, h)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

type diffHunk struct {
	ops                []Op
	oldStart, newStart int
}

func writeHunk(b *strings.Builder, h diffHunk) {
	oldCount, newCount := 0, 0
	for _, op := range h.ops {
		if op.Kind != Insert {
			oldCount += len(op.Lines)
		}
		if op.Kind != Delete {
			newCount += len(op.Lines)
		}
	}
	fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", h.oldStart, oldCount, h.newStart, newCount)
	for _, op := range h.ops {
		prefix := " "
		if op.Kind == Delete {
			prefix = "-"
		}
		if op.Kind == Insert {
			prefix = "+"
		}
		for _, line := range op.Lines {
			fmt.Fprintf(b, "%s%s\n", prefix, line)
		}
	}
}

func flattenOps(ops []Op) []Op {
	flat := make([]Op, 0, len(ops))
	for _, op := range ops {
		for _, line := range op.Lines {
			flat = append(flat, Op{Kind: op.Kind, Lines: []string{line}})
		}
	}
	if len(flat) > 0 && flat[len(flat)-1].Kind == Equal && flat[len(flat)-1].Lines[0] == "" {
		flat = flat[:len(flat)-1]
	}
	return flat
}

func contextHunks(ops []Op, context, oldStart, newStart int) []diffHunk {
	flat := flattenOps(ops)
	if len(flat) == 0 {
		return []diffHunk{{ops: flat, oldStart: oldStart, newStart: newStart}}
	}
	var intervals [][2]int
	for i, op := range flat {
		if op.Kind == Equal {
			continue
		}
		start, end := i, i+1
		left := context
		for start > 0 && left > 0 && flat[start-1].Kind == Equal {
			start--
			left--
		}
		right := context
		for end < len(flat) && right > 0 && flat[end].Kind == Equal {
			end++
			right--
		}
		if len(intervals) > 0 && start <= intervals[len(intervals)-1][1] {
			intervals[len(intervals)-1][1] = end
		} else {
			intervals = append(intervals, [2]int{start, end})
		}
	}
	if len(intervals) == 0 {
		return []diffHunk{{ops: flat, oldStart: oldStart, newStart: newStart}}
	}
	oldBefore, newBefore := 0, 0
	firstOld, firstNew := -1, -1
	result := make([]diffHunk, 0, len(intervals))
	for _, interval := range intervals {
		for i := 0; i < interval[0]; i++ {
			if flat[i].Kind != Insert {
				oldBefore++
			}
			if flat[i].Kind != Delete {
				newBefore++
			}
		}
		leadingOld, leadingNew := 0, 0
		for i := interval[0]; i < interval[1] && flat[i].Kind == Equal; i++ {
			leadingOld++
			leadingNew++
		}
		if firstOld < 0 {
			firstOld, firstNew = oldBefore, newBefore
		}
		result = append(result, diffHunk{ops: flat[interval[0]:interval[1]], oldStart: oldStart + oldBefore - firstOld - leadingOld, newStart: newStart + newBefore - firstNew - leadingNew})
	}
	return result
}

func trimContext(ops []Op, context int) []Op {
	// Equal operations are coalesced, but context is measured in physical lines.
	flat := make([]Op, 0, len(ops))
	for _, op := range ops {
		for _, line := range op.Lines {
			flat = append(flat, Op{Kind: op.Kind, Lines: []string{line}})
		}
	}
	// A shared trailing newline is a file terminator, not a visible context
	// line. Keep an empty terminal line only when it is an actual insert/delete.
	if len(flat) > 0 && flat[len(flat)-1].Kind == Equal && flat[len(flat)-1].Lines[0] == "" {
		flat = flat[:len(flat)-1]
	}
	ops = flat
	first, last := -1, -1
	for i, op := range ops {
		if op.Kind != Equal {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return ops
	}
	start, end := first, last+1
	left := context
	for start > 0 && left > 0 {
		start--
		if ops[start].Kind == Equal {
			left--
		}
	}
	right := context
	for end < len(ops) && right > 0 {
		if ops[end].Kind == Equal {
			right--
		}
		end++
	}
	return ops[start:end]
}
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func expired(deadline time.Time) bool { return !deadline.IsZero() && time.Now().After(deadline) }
func TruncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
