package jschema

import "strings"

// OutputEnvelopeTag is the tag name a subagent reply is asked to wrap its
// JSON in when a schema is in force. Asking for a fixed text envelope, rather
// than relying on a provider-specific structured-output or forced-tool-call
// feature, keeps extraction identical across every model provider (Anthropic,
// OpenAI, DeepSeek, local models, ...), since none of those APIs guarantee a
// compatible mechanism for every provider this harness must support.
const OutputEnvelopeTag = "mivia_output"

// EnvelopeAppendixBody is the shared instruction text asking the model to
// wrap its schema-conformant JSON reply in <mivia_output> tags. Both prompt-
// appendix renderers - PromptAppendix in jschema.go (user turn) and
// schemaSystemAppendix in internal/cli (system prompt) - delegate to this
// single function so the tag name and wrapping instruction can never drift
// out of sync between the two surfaces, which independently hand-built
// near-identical strings before this function existed.
func EnvelopeAppendixBody(contract string) string {
	return "\n\nReturn ONLY this exact envelope, with no prose before, after, or outside it: " +
		"a <" + OutputEnvelopeTag + "> opening tag on its own line, then valid JSON matching " +
		"this schema, then a </" + OutputEnvelopeTag + "> closing tag on its own line.\n" + contract
}

// ExtractEnvelope locates the content between a line-bound opening
// <mivia_output> tag and the LAST line-bound closing </mivia_output> tag that
// follows it, and returns it trimmed.
//
// "Line-bound" means the tag occupies its own line, allowing only horizontal
// whitespace before or after it on that line. This rejects a tag merely
// mentioned in prose (e.g. "I'll wrap this in <mivia_output> tags:") - a
// model narrating compliance before the real envelope is expected behavior,
// and the prompt instruction itself necessarily contains the tag text, so a
// naive first-occurrence match would extract the narration instead of the
// real payload.
//
// The LAST line-bound closing tag is used, not the first, so a
// closing-tag-shaped line inside the JSON payload's own string content
// cannot truncate the real payload early.
//
// When no unambiguous line-bound pair is found, s is returned unchanged -
// the same fail-open philosophy as StripOneCodeFence, so a reply that
// ignores the envelope instruction and emits bare JSON still validates.
func ExtractEnvelope(s string) string {
	openTag := "<" + OutputEnvelopeTag + ">"
	closeTag := "</" + OutputEnvelopeTag + ">"
	openIdx := findLineBoundTag(s, openTag, 0)
	if openIdx < 0 {
		return s
	}
	contentStart := openIdx + len(openTag)
	closeIdx := findLastLineBoundTag(s, closeTag, contentStart)
	if closeIdx < 0 {
		return s
	}
	return strings.TrimSpace(s[contentStart:closeIdx])
}

// ExtractOutputCandidate is the single entry point for turning a raw model
// reply into a JSON-parse candidate: it isolates the <mivia_output> envelope
// (if present, else the reply is left unchanged) and then strips one
// wrapping code fence, so a reply that fences its JSON inside the envelope -
// or skips the envelope but still fences the JSON - both resolve to bare
// JSON text.
func ExtractOutputCandidate(reply string) string {
	return StripOneCodeFence(ExtractEnvelope(reply))
}

// findLineBoundTag returns the byte index of tag on the first line at or
// after from whose trimmed content is exactly tag (see ExtractEnvelope).
// Returns -1 if none is found.
//
// This walks s line by line instead of re-testing every substring match of
// tag against its surrounding text: a per-match scan back to the previous
// newline (as a naive implementation would do) re-reads an overlapping
// prefix on every match and is quadratic on input that repeats the tag text
// many times on one line - reachable from an untrusted model reply, which is
// exactly the input this function parses. Each byte of s is visited at most
// once across the whole walk, so this is O(len(s)) regardless of how many
// times tag appears.
func findLineBoundTag(s, tag string, from int) int {
	pos, ok := firstLineBoundTag(s, tag, from)
	if !ok {
		return -1
	}
	return pos
}

// findLastLineBoundTag is findLineBoundTag but returns the last line-bound
// match at or after from, scanning to the end of s in one O(len(s)) walk.
func findLastLineBoundTag(s, tag string, from int) int {
	last := -1
	pos := from
	for pos <= len(s) {
		found, ok := firstLineBoundTag(s, tag, pos)
		if !ok {
			return last
		}
		last = found
		pos = nextLineStart(s, found)
	}
	return last
}

// firstLineBoundTag scans lines starting at from and returns the byte offset
// of tag on the first line whose trimmed content equals tag exactly.
func firstLineBoundTag(s, tag string, from int) (int, bool) {
	pos := from
	for pos <= len(s) {
		lineEnd := len(s)
		if nl := strings.IndexByte(s[pos:], '\n'); nl >= 0 {
			lineEnd = pos + nl
		}
		line := s[pos:lineEnd]
		if strings.TrimSpace(line) == tag {
			return pos + strings.Index(line, tag), true
		}
		pos = nextLineStart(s, lineEnd)
	}
	return 0, false
}

// nextLineStart returns the byte offset just past the newline at fromLineEnd
// (a position returned as a line's end by firstLineBoundTag), or an offset
// past the end of s when fromLineEnd is already the end of the string -
// terminating the caller's walk.
func nextLineStart(s string, fromLineEnd int) int {
	if fromLineEnd >= len(s) {
		return len(s) + 1
	}
	return fromLineEnd + 1
}
