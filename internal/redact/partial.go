package redact

import (
	"regexp/syntax"
	"unicode/utf8"
)

// partialMatcher answers the question a streaming redactor has to ask of each
// pattern: "from which byte of this text onward could a match still be in
// progress?" - a prefix of a match that later bytes may complete.
//
// regexp can only report COMPLETE matches. Deciding what is safe to ship
// needs the other half: text that matches nothing yet but is the beginning of
// something that would. A literal-anchor scheme ("hold any suffix that is a
// prefix of `sk-ant-`") is not enough - the shipped key-name rule matches
// `token: abc` and a model that emits `token`, `: `, `abc` as three deltas has
// the anchor fully present and the value absent at the second push. So the
// simulation runs the pattern's own NFA over the text with one thread started
// at every rune, and reports the earliest start still alive at the end.
type partialMatcher struct {
	prog *syntax.Prog
}

// compilePartial builds the matcher for one expression with the same parse
// mode regexp.Compile uses, so both agree on what the pattern means.
func compilePartial(expr string) (*partialMatcher, error) {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil, err
	}
	prog, err := syntax.Compile(re.Simplify())
	if err != nil {
		return nil, err
	}
	return &partialMatcher{prog: prog}, nil
}

// earliestOpen returns the smallest offset s at which a thread started, has
// consumed text[s:], and is waiting on a rune that has not arrived - or
// len(text) when no such thread exists. Threads that reached InstMatch do not
// count: a complete match is the safeCut fixed-point loop's business, and a
// match that could still grow has a live consuming thread of its own.
//
// Empty-width assertions (`^`, `$`, `\b`) are treated as satisfied. The buffer
// starts mid-block and ends mid-stream, so the real context of either edge is
// unknown; passing them over-approximates liveness, which holds more, never
// less.
func (m *partialMatcher) earliestOpen(text string) int {
	n := len(m.prog.Inst)
	cur := newThreadSet(n)
	next := newThreadSet(n)
	pos := 0
	for {
		m.addThread(cur, m.prog.Start, pos)
		if pos >= len(text) {
			break
		}
		r, width := utf8.DecodeRuneInString(text[pos:])
		next.clear()
		for pc, start := range cur {
			if start < 0 {
				continue
			}
			inst := &m.prog.Inst[pc]
			if consumes(inst, r) {
				m.addThread(next, int(inst.Out), start)
			}
		}
		cur, next = next, cur
		pos += width
	}
	earliest := len(text)
	for pc, start := range cur {
		if start < 0 || start >= earliest {
			continue
		}
		switch m.prog.Inst[pc].Op {
		case syntax.InstRune, syntax.InstRune1, syntax.InstRuneAny, syntax.InstRuneAnyNotNL:
			earliest = start
		}
	}
	return earliest
}

// threadSet maps a program counter to the earliest start offset of a thread
// sitting there, or -1. Two threads at one pc have identical futures, so the
// earlier start is the only one that matters.
type threadSet []int

func newThreadSet(n int) threadSet {
	s := make(threadSet, n)
	s.clear()
	return s
}

func (s threadSet) clear() {
	for i := range s {
		s[i] = -1
	}
}

// addThread places a thread at pc and follows every epsilon edge from it,
// keeping the earliest start at each pc reached.
func (m *partialMatcher) addThread(set threadSet, pc, start int) {
	stack := []int{pc}
	for len(stack) > 0 {
		pc = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if set[pc] >= 0 && set[pc] <= start {
			continue
		}
		set[pc] = start
		inst := &m.prog.Inst[pc]
		switch inst.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			stack = append(stack, int(inst.Out), int(inst.Arg))
		case syntax.InstNop, syntax.InstCapture, syntax.InstEmptyWidth:
			stack = append(stack, int(inst.Out))
		}
	}
}

// consumes reports whether a consuming instruction accepts r.
func consumes(inst *syntax.Inst, r rune) bool {
	switch inst.Op {
	case syntax.InstRune, syntax.InstRune1:
		return inst.MatchRune(r)
	case syntax.InstRuneAny:
		return true
	case syntax.InstRuneAnyNotNL:
		return r != '\n'
	}
	return false
}
