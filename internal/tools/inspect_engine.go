package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// inspectEngine walks normalized scopes in deterministic order, applying the
// shared ignore/secret/glob policy once per scope, and bounds its own
// accumulation by result count and (approximately) by byte budget.
type inspectEngine struct {
	ws       *workspace.Root
	maxBytes int
	// envelopeReserve is the exact marshaled byte size of everything in the
	// output except Results (Execute measures it directly), so the budget
	// below accounts for Provenance.Paths regardless of how many/how long the
	// requested paths are.
	envelopeReserve      int
	secretPathExceptions []string
	secretPathPatterns   []string
	view                 ignoreView
	re                   *regexp.Regexp
	glob                 string
	maxResults           int
	contextLines         int
}

func (t *inspectRepositoryTool) newInspectEngine(in inspectRepositoryInput, re *regexp.Regexp, view ignoreView, envelopeBytes int) *inspectEngine {
	return &inspectEngine{
		ws:                   t.ws,
		maxBytes:             t.maxBytes,
		envelopeReserve:      envelopeBytes,
		secretPathExceptions: t.secretPathExceptions,
		secretPathPatterns:   t.secretPathPatterns,
		view:                 view,
		re:                   re,
		glob:                 in.Glob,
		maxResults:           in.MaxResults,
		contextLines:         in.ContextLines,
	}
}

func (e *inspectEngine) run(ctx context.Context, scopes []inspectScope) ([]inspectResult, bool, string, error) {
	seen := make(map[string]bool)
	var collected []inspectResult
	truncated := false
	reason := ""
	budget := e.maxBytes - e.envelopeReserve - inspectEnvelopeSlack
	if budget < 0 {
		budget = 0
	}
	used := 0
	cappedScopes := 0

	stop := func(r string) error {
		if !truncated {
			truncated = true
			reason = r
		}
		if r == inspectTruncByteLimit {
			return errMaxBytes
		}
		return errMaxMatches
	}

	for _, scope := range scopes {
		if truncated {
			break
		}
		errs := &walkErrors{maxErrs: 10}
		walkErr := walkFilteredFiles(ctx, e.ws, scope.abs, e.glob, e.secretPathExceptions, e.secretPathPatterns, e.view, true, errs, func(path, rel string, _ os.FileInfo) error {
			switch err := e.scanFileMatches(ctx, path, rel, &collected, &used, budget, seen); err {
			case errMaxBytes:
				return stop(inspectTruncByteLimit)
			case errMaxMatches:
				return stop(inspectTruncResultLimit)
			default:
				if err != nil {
					return err
				}
				return ctx.Err()
			}
		})
		if walkErr != nil && (errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded)) {
			// Do not return collected here: on a real cancellation reached
			// via walkFilteredFiles' own goroutine-abandon path (a stuck
			// syscall, not the cooperative per-file check above), the walk
			// goroutine is still running and may still be appending to
			// collected through scanFileMatches - reading it concurrently
			// would race. glob/grep's callers apply the same discipline
			// (discard partial results on real cancellation, keep them
			// only for the errMaxBytes/errMaxMatches stop sentinels).
			return nil, truncated, reason, walkErr
		}
		if errs.count() >= errs.maxErrs {
			cappedScopes++
		}
	}
	if !truncated && cappedScopes > 0 {
		truncated = true
		reason = inspectTruncWalkErrors
	}

	sort.Slice(collected, func(i, j int) bool {
		if collected[i].Path != collected[j].Path {
			return collected[i].Path < collected[j].Path
		}
		return collected[i].Line < collected[j].Line
	})
	return collected, truncated, reason, nil
}

// pendingMatch is a match awaiting its trailing "after" context lines.
type pendingMatch struct {
	line      int
	text      string
	before    []string
	after     []string
	needAfter int
}

// matchCollector owns the bounded accumulation state shared across a file
// scan: the dedup set, the marshaled-size byte counter, and the count/byte
// caps. It applies the same per-match checks run() used to apply after
// scanning, so truncation reasons and result selection are unchanged; only
// the accumulation bound moved inline, so a file with arbitrarily many
// matches is never fully scanned before the caps apply.
type matchCollector struct {
	e         *inspectEngine
	collected *[]inspectResult
	used      *int
	budget    int
	seen      map[string]bool
}

// flush emits one completed match (its full context window assembled) and
// reports whether a cap tripped via the sentinel errors.
func (c *matchCollector) flush(p pendingMatch, rel string) error {
	ctxLines := make([]string, 0, len(p.before)+len(p.after))
	ctxLines = append(ctxLines, p.before...)
	ctxLines = append(ctxLines, p.after...)
	_, err := c.emit(inspectResult{Path: rel, Line: p.line, Text: p.text, Context: ctxLines})
	return err
}

// emit applies run()'s former per-match checks in the same order: dedup
// first (duplicates cost no budget), then marshaled-size byte accounting,
// then the count cap. It reports whether scanning should stop because a cap
// tripped.
func (c *matchCollector) emit(m inspectResult) (bool, error) {
	key := m.Path + "\x00" + strconv.Itoa(m.Line)
	if c.seen[key] {
		return false, nil
	}
	encoded, marshalErr := json.Marshal(m)
	if marshalErr != nil {
		return false, nil
	}
	if c.e.maxBytes > 0 && *c.used+len(encoded) > c.budget {
		return true, errMaxBytes
	}
	c.seen[key] = true
	*c.collected = append(*c.collected, m)
	*c.used += len(encoded)
	if len(*c.collected) >= c.e.maxResults {
		return true, errMaxMatches
	}
	return false, nil
}

// scanFileMatches scans one file for regex matches and emits each match,
// with its exact requested context window, through the collector's dedup and
// accumulation caps (the same per-match checks run() applied after scanning
// before), so a file with arbitrarily many matches is never fully scanned and
// accumulated before the count/byte caps apply. It streams the file once:
// "before" context is a bounded ring buffer and "after" context is a bounded
// pending queue, so memory use does not grow with file size (unlike buffering
// whole-file content, which read_file's line-window path does for its own,
// different, random-access use case). The scan stops the moment a cap trips,
// returning errMaxBytes or errMaxMatches; run() maps those sentinels to the
// same truncation reasons as before.
func (e *inspectEngine) scanFileMatches(ctx context.Context, path, rel string, collected *[]inspectResult, used *int, budget int, seen map[string]bool) error {
	f, _, err := openRegularFile(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	c := &matchCollector{e: e, collected: collected, used: used, budget: budget, seen: seen}
	var before []string
	var queue []pendingMatch
	lineNo := 0

	for sc.Scan() {
		if lineNo&0xff == 0 && ctx != nil && ctx.Err() != nil {
			break
		}
		lineNo++
		if err := e.advanceLine(sc.Text(), lineNo, rel, &before, &queue, c); err != nil {
			return err
		}
	}
	for _, p := range queue {
		if err := c.flush(p, rel); err != nil {
			return err
		}
	}
	return nil
}

// advanceLine processes one scanned line: it advances the pending "after"
// context queue (flushing any match whose window just completed), records a
// fresh match when the line hits, and rolls the bounded "before" ring. It
// returns errMaxBytes/errMaxMatches the moment a cap trips so the caller
// stops scanning immediately.
func (e *inspectEngine) advanceLine(line string, lineNo int, rel string, before *[]string, queue *[]pendingMatch, c *matchCollector) error {
	remaining := (*queue)[:0]
	for _, p := range *queue {
		if p.needAfter > 0 {
			p.after = append(p.after, line)
			p.needAfter--
		}
		if p.needAfter == 0 {
			if err := c.flush(p, rel); err != nil {
				return err
			}
		} else {
			remaining = append(remaining, p)
		}
	}
	*queue = remaining

	if e.re.MatchString(line) {
		text := line
		if len(text) > 200 {
			text = truncateUTF8(text, 200) + "..."
		}
		p := pendingMatch{line: lineNo, text: text, before: append([]string(nil), *before...), needAfter: e.contextLines}
		if p.needAfter == 0 {
			if err := c.flush(p, rel); err != nil {
				return err
			}
		} else {
			*queue = append(*queue, p)
		}
	}

	if e.contextLines > 0 {
		*before = append(*before, line)
		if len(*before) > e.contextLines {
			*before = (*before)[1:]
		}
	}
	return nil
}
