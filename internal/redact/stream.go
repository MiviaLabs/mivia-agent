package redact

import "unicode/utf8"

// StreamHoldBack is the size of the flat window a [Stream] used to withhold
// from the wire. It bounds nothing any more; it is kept because fixtures size
// a block against it to prove the hold is no longer a window.
//
// Redacting each streamed fragment on its own cannot see a secret that arrives
// split - `sk-` at the end of one delta and the rest at the start of the next
// matches neither half - so a policy used to suppress streaming outright. A
// Stream keeps the boundary AND the liveness: it redacts the held tail joined
// to each new fragment and ships only what no future byte can still complete
// into a match.
//
// The hold is CONTENT-AWARE: safeCut runs each pattern's own automaton over
// the buffer and withholds exactly the suffix that is still the possible
// beginning of a match (partialMatcher). For ordinary prose that is zero to a
// dozen bytes - the longest word the shipped rules open on - so the viewer
// trails the model by a fraction of a delta instead of by 256 bytes, and a
// mid-message pause no longer leaves the held text invisible. The window was
// a byte bound standing in for a time bound: at four bytes a delta it sat two
// seconds behind at full speed and unbounded during every pause. It also had
// a hole the automaton does not: a match longer than the window could begin
// further back than the window reached and ship its opening bytes. Under the
// automaton an open match pins the cut at its start for as long as it stays
// open, whatever its length (TestAMatchLongerThanTheOldWindowIsHeldWhole).
//
// Full rationale and the operator-facing statement: docs/product/chat-sync-wire.md.
const StreamHoldBack = 256

// Stream redacts a fragmented text stream across fragment boundaries.
//
// The zero value is ready to use and reads the process-wide policy on each
// call, so a policy installed or removed mid-stream takes effect on the next
// fragment rather than being frozen at construction. A Stream is not safe for
// concurrent use; one belongs to one prose block.
type Stream struct {
	held string
}

// Push accepts one fragment and returns the redacted text that is now safe to
// ship. The return may be empty - the fragment is then entirely inside the
// hold-back window and will be shipped by a later Push or by Flush. Text is
// never re-emitted: every byte is returned by exactly one Push or Flush.
func (s *Stream) Push(chunk string) string {
	p := Current()
	// The fast path must also require an empty hold: a policy removed while a
	// tail is held would otherwise ship the new fragment ahead of the older
	// bytes still sitting in the buffer, reordering the stream.
	if p.empty() && s.held == "" {
		return chunk
	}
	buf := s.held + chunk
	cut := p.safeCut(buf)
	s.held = buf[cut:]
	return p.Text(buf[:cut])
}

// Flush returns the redacted remainder and empties the buffer. Callers must
// invoke it when the block closes: a held tail that is neither shipped nor
// flushed is TEXT SILENTLY LOST, which is worse than the delay the hold-back
// exists to trade for.
func (s *Stream) Flush() string {
	p := Current()
	out := p.Text(s.held)
	s.held = ""
	return out
}

// Pending reports whether the stream is holding text that has not shipped.
func (s *Stream) Pending() bool { return s.held != "" }

// Discard drops the held tail without emitting it. Only for a block whose text
// the consumer is being told to throw away wholesale (an assistant reset);
// anywhere else, use Flush.
func (s *Stream) Discard() { s.held = "" }

// safeCut returns the length of the prefix of buf that can be shipped now.
//
// Two rules, both conservative:
//
//  1. Every byte from the earliest still-open partial match onward is
//     withheld (partialMatcher.earliestOpen), so the opening bytes of a match
//     are still in the buffer when the rest of it arrives.
//  2. No cut is made INSIDE a match that already exists in buf; the cut moves
//     back to that match's start so the match is redacted as one unit. Moving
//     back can expose a further crossing match, so this iterates to a fixed
//     point - cut strictly decreases, so it terminates.
//
// Matches are located in the RAW buffer even though Policy.Text applies the
// patterns in sequence over each other's output. That is deliberate and errs
// towards holding more: a position that is inside a raw match is refused as a
// cut point regardless of which pattern would eventually rewrite it.
func (p *Policy) safeCut(buf string) int {
	if p.empty() {
		return len(buf)
	}
	// A delta that ends inside a multi-byte rune must hold that rune's opening
	// bytes AND keep them out of the simulation: decoded alone they are
	// U+FFFD, which matches nothing, so a thread that had reached them died
	// and the closing bytes then arrived with no context. `api_` + the first
	// byte of KELVIN SIGN, then its last two bytes + `ey: hunter2` folds to
	// `api_key: hunter2` under (?i) - and shipped both halves in the clear.
	cut := incompleteRuneStart(buf)
	for _, m := range p.partial {
		if open := m.earliestOpen(buf[:cut]); open < cut {
			cut = open
		}
	}
	if cut <= 0 {
		return 0
	}
	for {
		moved := false
		for _, re := range p.patterns {
			for _, m := range re.FindAllStringIndex(buf, -1) {
				if m[0] < cut && m[1] > cut {
					cut = m[0]
					moved = true
				}
			}
		}
		if !moved {
			break
		}
		if cut <= 0 {
			return 0
		}
	}
	// Never split a rune. A delta carrying half a multi-byte character is
	// invalid UTF-8 on a JSON wire, and the two halves would be reassembled by
	// a consumer that concatenates - but only after one of them has already
	// been rendered as a replacement character.
	for cut > 0 && cut < len(buf) && !utf8.RuneStart(buf[cut]) {
		cut--
	}
	return cut
}

// incompleteRuneStart returns the offset of a truncated multi-byte rune at
// the end of buf, or len(buf) when the buffer ends on a rune boundary.
func incompleteRuneStart(buf string) int {
	for i := len(buf) - 1; i >= 0 && i >= len(buf)-utf8.UTFMax; i-- {
		if utf8.RuneStart(buf[i]) {
			if !utf8.FullRuneInString(buf[i:]) {
				return i
			}
			return len(buf)
		}
	}
	return len(buf)
}
